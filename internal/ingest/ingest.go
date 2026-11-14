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
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/streets"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// DedupWindow is how far apart two copies of a page can be received and
// still be one transmission (R19).
const DedupWindow = 10 * time.Second

const (
	// MaxFuture is how far ahead of the server's clock a receive time may be.
	MaxFuture = 60 * time.Second
	// MaxTextBytes caps a page's text. POCSAG pages are far shorter.
	MaxTextBytes = 4096
)

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
	Geo      *geocode.Geocoder // nil: messages aren't placed
	// OnStored runs in the transaction that stores a new page (not a
	// duplicate copy), so work it queues commits with the page: the
	// integrations' deliveries (ADR 0006 §3).
	OnStored func(ctx context.Context, q *db.Queries, messageID int64) error
	// Events groups a new page into a large event, after OnStored and in the
	// same transaction (R87, ADR 0027); nil groups nothing.
	Events *events.Detector
	// Tracer traces each step, under the caller's "ingest" span (R71); nil
	// traces nothing.
	Tracer trace.Tracer
}

// Ingest processes one page from a client. It returns the message ID, or 0
// for an ignored page.
func (s *Service) Ingest(ctx context.Context, clientID string, pg Page) (Outcome, int64, error) {
	// Every page, ignored ones included, updates the client's last-seen time (R16).
	if err := s.Store.TouchClient(ctx, db.TouchClientParams{ID: clientID, SeenAt: pg.ReceivedAt}); err != nil {
		return "", 0, fmt.Errorf("touch client: %s", store.SafeError(err))
	}
	tr := telemetry.Or(s.Tracer)
	_, span := tr.Start(ctx, "ingest.redact") // control tokens, redaction, priority
	prep := pipeline.Prepare(s.Network, pg.Capcode, pg.Text)
	span.End()
	if prep.Dropped {
		return Ignored, 0, nil
	}

	pctx, span := tr.Start(ctx, "ingest.parse") // alias, template, agency profile
	defer span.End()                            // already ended, unless it failed
	cands, err := s.Candidates(pctx, s.Store.Queries, pg.Capcode)
	if err != nil {
		return "", 0, fmt.Errorf("aliases: %s", store.SafeError(err))
	}
	draft, err := s.Draft(prep, cands, pg)
	if err != nil {
		return "", 0, err
	}
	span.End()

	var outcome Outcome
	var id int64
	ctx, span = tr.Start(ctx, "ingest.store")
	defer span.End()
	err = s.Store.InTx(ctx, func(q *db.Queries) error {
		var err error
		outcome, id, err = s.Save(ctx, q, draft, func(id int64) error {
			return q.InsertReception(ctx, db.InsertReceptionParams{MessageID: id, ClientID: clientID, ReceivedAt: pg.ReceivedAt})
		})
		return err
	})
	if err != nil {
		return "", 0, fmt.Errorf("store page: %s", store.SafeError(err))
	}
	return outcome, id, nil
}

// Candidates are the aliases whose pattern matches a capcode.
func (s *Service) Candidates(ctx context.Context, q *db.Queries, capcode string) ([]Candidate, error) {
	rows, err := q.AliasesMatching(ctx, capcode)
	if err != nil {
		return nil, err
	}
	cands := make([]Candidate, len(rows))
	for i, r := range rows {
		cands[i] = Candidate{ID: r.ID, Pattern: r.Pattern, AgencyProfile: r.AgencyProfile, Ignored: r.Ignored}
	}
	return cands, nil
}

// Draft is a page after the network rules and parsing, ready for Save.
type Draft struct {
	Params     db.InsertMessageParams
	Fields     Fields
	ReceivedAt time.Time
	Ignored    bool // its alias is marked ignored (R27): it doesn't count towards large events
}

// Draft parses a prepared page with the alias that wins among cands (R3, R27).
func (s *Service) Draft(prep pipeline.Prepared, cands []Candidate, pg Page) (Draft, error) {
	alias := BestAlias(cands)
	var aliasID *int64
	var agency *profile.Agency
	ignored := false
	if alias != nil {
		aliasID = &alias.ID
		agency = s.agencyFor(alias.AgencyProfile)
		ignored = alias.Ignored
	}
	parsed := pipeline.Parse(s.Network, agency, s.Streets, prep)
	fields, err := FieldsOf(parsed)
	if err != nil {
		return Draft{}, err
	}
	params := messageParams(prep, fields, pg)
	params.AliasID = aliasID
	return Draft{Params: params, Fields: fields, ReceivedAt: pg.ReceivedAt, Ignored: ignored}, nil
}

// Save merges a page into the message it duplicates (R19, R21) or stores it
// as a new one, in the caller's transaction. record notes who received this
// copy: a client's reception, or an import's.
func (s *Service) Save(ctx context.Context, q *db.Queries, d Draft, record func(id int64) error) (Outcome, int64, error) {
	tr := telemetry.Or(s.Tracer)
	// Serialise copies of the same page so two clients can't both insert it.
	dctx, dspan := tr.Start(ctx, "ingest.dedup")
	defer dspan.End()
	if err := q.LockDedupKey(dctx, d.Params.DedupKey); err != nil {
		return "", 0, err
	}
	dup, err := q.FindDuplicate(dctx, db.FindDuplicateParams{DedupKey: d.Params.DedupKey, ReceivedAt: d.ReceivedAt})
	dspan.End()
	switch {
	case err == nil:
		if err := q.KeepEarliestSentAt(ctx, db.KeepEarliestSentAtParams{ID: dup.ID, ReceivedAt: d.ReceivedAt}); err != nil {
			return "", 0, err
		}
		return Duplicate, dup.ID, record(dup.ID)
	case errors.Is(err, pgx.ErrNoRows):
		// Placed before it's stored, in one write: an update afterwards would
		// write the page to every index twice.
		lctx, lspan := tr.Start(ctx, "ingest.locate")
		res, err := place(lctx, s.Geo, q, d.Fields)
		lspan.End()
		if err != nil {
			return "", 0, err
		}
		loc := geocode.SetLocationParams(0, res)
		d.Params.Lon, d.Params.Lat, d.Params.LocationSource, d.Params.LocationMismatch = loc.Lon, loc.Lat, loc.Source, loc.Mismatch
		id, err := q.InsertMessage(ctx, d.Params)
		if err != nil {
			return "", 0, err
		}
		if err := record(id); err != nil {
			return "", 0, err
		}
		if s.OnStored != nil {
			if err := s.OnStored(ctx, q, id); err != nil {
				return "", 0, err
			}
		}
		if s.Events != nil {
			ectx, espan := tr.Start(ctx, "ingest.events")
			err := s.Events.Stored(ectx, q, events.Page{ID: id, SentAt: d.Params.SentAt, Text: d.Params.Text, Priority: d.Params.Priority,
				AliasID: d.Params.AliasID, Ignored: d.Ignored, Heartbeat: d.Params.Heartbeat, Job: d.Params.JobNumber, LargeEventField: d.Fields.LargeEventField})
			espan.End()
			if err != nil {
				return "", 0, err
			}
		}
		return Stored, id, nil
	default:
		return "", 0, err
	}
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
	// LargeEventField: a field holds a value its agency profile lists in
	// large_event_fields (R87). Stored, so it is what the page was parsed
	// with until it is re-parsed (R23a).
	LargeEventField bool
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
		Format:          nilIfEmpty(parsed.Format),
		JobNumber:       str("job_number"),
		EventType:       str("event_type"),
		Description:     str("description"),
		Address:         str("address"),
		Locality:        str("locality"),
		GridRef:         str("grid_ref"),
		Group:           str("group"),
		Units:           units,
		Extra:           extraJSON,
		ParseFailed:     parsed.ParseFailed,
		AddressUnsplit:  parsed.AddressUnsplit,
		LargeEventField: parsed.LargeEventField,
	}, nil
}

// Candidate is an alias that may apply to a capcode.
type Candidate struct {
	ID            int64
	Pattern       string
	AgencyProfile *string
	Ignored       bool // marked ignored (R27)
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

func messageParams(prep pipeline.Prepared, f Fields, pg Page) db.InsertMessageParams {
	fn, baud := int16(pg.Function), int32(pg.Baud)
	return db.InsertMessageParams{
		Capcode:         prep.Capcode,
		Text:            prep.Text,
		Priority:        nilIfEmpty(string(prep.Priority)),
		Heartbeat:       prep.Heartbeat,
		SentAt:          pg.ReceivedAt,
		DedupKey:        pipeline.DedupKey(prep),
		Format:          f.Format,
		JobNumber:       f.JobNumber,
		EventType:       f.EventType,
		Description:     f.Description,
		Address:         f.Address,
		Locality:        f.Locality,
		GridRef:         f.GridRef,
		PagingGroup:     f.Group,
		Units:           f.Units,
		Extra:           f.Extra,
		ParseFailed:     f.ParseFailed,
		AddressUnsplit:  f.AddressUnsplit,
		LargeEventField: f.LargeEventField,
		Function:        &fn,
		Baud:            &baud,
	}
}

// Locate places message id from its parsed fields (R24-R26). geo nil
// leaves it unplaced.
func Locate(ctx context.Context, geo *geocode.Geocoder, q *db.Queries, id int64, f Fields) error {
	if geo == nil {
		return nil
	}
	res, err := place(ctx, geo, q, f)
	if err != nil {
		return err
	}
	return q.SetLocation(ctx, geocode.SetLocationParams(id, res))
}

// place finds where parsed fields go. geo nil places nothing.
func place(ctx context.Context, geo *geocode.Geocoder, q *db.Queries, f Fields) (geocode.Result, error) {
	if geo == nil {
		return geocode.Result{}, nil
	}
	val := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return geo.Locate(ctx, q, geocode.Input{Address: val(f.Address), Locality: val(f.Locality), Description: val(f.Description), GridRef: val(f.GridRef)})
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
