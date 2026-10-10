// Package ingest stores pages posted by clients: network rules, parsing,
// deduplication and storage (SPEC.md R16–R23). Page text is never logged and
// never included in returned errors (P1).
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

// DedupWindow is how far apart two copies of a page can be received and
// still be one transmission (R19).
const DedupWindow = 10 * time.Second

// Page is one page as a client posted it.
type Page struct {
	Capcode    string // zero-padded, 7 digits
	Text       string // decoder text, separator trimmed, not yet cleaned
	ReceivedAt time.Time
	Function   int
	Baud       int
}

// Outcome says what happened to a page.
type Outcome string

const (
	Stored    Outcome = "stored"
	Duplicate Outcome = "duplicate"
	Ignored   Outcome = "ignored"
)

// Service ingests pages.
type Service struct {
	Store    *store.Store
	Network  *profile.Network
	Agencies map[string]*profile.Agency
	Streets  *streets.Index
}

// Ingest processes one page from a client. It returns the message ID, or 0
// for an ignored page.
func (s *Service) Ingest(ctx context.Context, clientID string, pg Page) (Outcome, int64, error) {
	// Every page, ignored ones included, updates the client's last-seen time (R16).
	if err := s.Store.TouchClient(ctx, db.TouchClientParams{ID: clientID, SeenAt: pg.ReceivedAt}); err != nil {
		return "", 0, fmt.Errorf("touch client: %s", store.SafeError(err))
	}
	prep := pipeline.Prepare(s.Network, pg.Capcode, pg.Text)
	if prep.Dropped {
		return Ignored, 0, nil
	}

	rows, err := s.Store.AliasesMatching(ctx, pg.Capcode)
	if err != nil {
		return "", 0, fmt.Errorf("aliases: %s", store.SafeError(err))
	}
	cands := make([]Candidate, len(rows))
	for i, r := range rows {
		cands[i] = Candidate{ID: r.ID, Pattern: r.Pattern, AgencyProfile: r.AgencyProfile}
	}
	alias := BestAlias(cands)
	var aliasID *int64
	var agency *profile.Agency
	if alias != nil {
		aliasID = &alias.ID
		agency = s.agencyFor(alias.AgencyProfile)
	}
	parsed := pipeline.Parse(s.Network, agency, s.Streets, prep)
	params, err := messageParams(prep, parsed, pg)
	if err != nil {
		return "", 0, err
	}
	params.AliasID = aliasID

	outcome, id := Stored, int64(0)
	err = s.Store.InTx(ctx, func(q *db.Queries) error {
		// Serialise copies of the same page so two clients can't both insert it.
		if err := q.LockDedupKey(ctx, params.DedupKey); err != nil {
			return err
		}
		dup, err := q.FindDuplicate(ctx, db.FindDuplicateParams{DedupKey: params.DedupKey, ReceivedAt: pg.ReceivedAt})
		switch {
		case err == nil:
			outcome, id = Duplicate, dup.ID
			if err := q.KeepEarliestSentAt(ctx, db.KeepEarliestSentAtParams{ID: id, ReceivedAt: pg.ReceivedAt}); err != nil {
				return err
			}
		case errors.Is(err, pgx.ErrNoRows):
			if id, err = q.InsertMessage(ctx, params); err != nil {
				return err
			}
		default:
			return err
		}
		return q.InsertReception(ctx, db.InsertReceptionParams{MessageID: id, ClientID: clientID, ReceivedAt: pg.ReceivedAt})
	})
	if err != nil {
		return "", 0, fmt.Errorf("store page: %s", store.SafeError(err))
	}
	return outcome, id, nil
}

// agencyFor resolves a template's agency profile name (R3). nil means text
// only (R4); a template pointing at a missing profile is text only too
// (section 14).
func (s *Service) agencyFor(name *string) *profile.Agency {
	if name == nil {
		return nil
	}
	return s.Agencies[*name]
}

// LikeMatch reports whether capcode matches an alias pattern, with SQL LIKE
// semantics: % is any run of characters, _ is one character (R27).
func LikeMatch(pattern, s string) bool {
	if pattern == "" {
		return s == ""
	}
	switch pattern[0] {
	case '%':
		for i := 0; i <= len(s); i++ {
			if LikeMatch(pattern[1:], s[i:]) {
				return true
			}
		}
		return false
	case '_':
		return s != "" && LikeMatch(pattern[1:], s[1:])
	default:
		return s != "" && s[0] == pattern[0] && LikeMatch(pattern[1:], s[1:])
	}
}

// Fields maps parsed fields to columns, shared by insert and re-parse.
type Fields struct {
	Format, JobNumber, EventType, Description, Address, Locality, GridRef, Group *string
	Units                                                                        []string
	Extra                                                                        []byte
	ParseFailed, AddressUnsplit                                                  bool
}

// FieldsOf converts a parse result to column values.
func FieldsOf(parsed pipeline.Parsed) (Fields, error) {
	str := func(name string) *string { return nilIfEmpty(parsed.String(name)) }
	extra := map[string]any{}
	std := map[string]bool{}
	for _, f := range profile.StandardFields {
		std[f] = true
	}
	for k, v := range parsed.Fields {
		if !std[k] {
			extra[k] = v
		}
	}
	extraJSON, err := json.Marshal(extra)
	if err != nil {
		return Fields{}, errors.New("encode extra fields")
	}
	units, _ := parsed.Fields["units"].([]string)
	return Fields{
		Format:         nilIfEmpty(parsed.Format),
		JobNumber:      str("job_number"),
		EventType:      str("event_type"),
		Description:    str("description"),
		Address:        str("address"),
		Locality:       str("locality"),
		GridRef:        str("grid_ref"),
		Group:          str("group"),
		Units:          units,
		Extra:          extraJSON,
		ParseFailed:    parsed.ParseFailed,
		AddressUnsplit: parsed.AddressUnsplit,
	}, nil
}

// Candidate is an alias that may apply to a capcode.
type Candidate struct {
	ID            int64
	Pattern       string
	AgencyProfile *string
}

// BestAlias picks the alias that wins for a capcode among those whose pattern
// matches it (R27): an exact capcode, then fewer wildcards, with patterns that
// start with a wildcard last.
func BestAlias(rows []Candidate) *Candidate {
	if len(rows) == 0 {
		return nil
	}
	wild := func(p string) int { return strings.Count(p, "%") + strings.Count(p, "_") }
	leading := func(p string) bool { return p[0] == '%' || p[0] == '_' }
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].Pattern, rows[j].Pattern
		if leading(a) != leading(b) {
			return !leading(a)
		}
		if wild(a) != wild(b) {
			return wild(a) < wild(b)
		}
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		return rows[i].ID < rows[j].ID
	})
	return &rows[0]
}

func messageParams(prep pipeline.Prepared, parsed pipeline.Parsed, pg Page) (db.InsertMessageParams, error) {
	f, err := FieldsOf(parsed)
	if err != nil {
		return db.InsertMessageParams{}, err
	}
	fn, baud := int16(pg.Function), int32(pg.Baud)
	return db.InsertMessageParams{
		Capcode:        prep.Capcode,
		Text:           prep.Text,
		Priority:       nilIfEmpty(string(prep.Priority)),
		Heartbeat:      prep.Heartbeat,
		SentAt:         pg.ReceivedAt,
		DedupKey:       pipeline.DedupKey(prep),
		Format:         f.Format,
		JobNumber:      f.JobNumber,
		EventType:      f.EventType,
		Description:    f.Description,
		Address:        f.Address,
		Locality:       f.Locality,
		GridRef:        f.GridRef,
		PagingGroup:    f.Group,
		Units:          f.Units,
		Extra:          f.Extra,
		ParseFailed:    f.ParseFailed,
		AddressUnsplit: f.AddressUnsplit,
		Function:       &fn,
		Baud:           &baud,
	}, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
