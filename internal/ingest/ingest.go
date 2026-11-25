// Package ingest stores pages posted by clients: network rules, parsing,
// deduplication and storage (SPEC.md R16–R23). Page text is never logged and
// never included in returned errors (P1).
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/neardup"
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

// Dedup is the kind of duplicate a copy was (R19, R19a): the metric's label.
type Dedup string

const (
	// Exact: the same text as its message's (R19).
	Exact Dedup = "exact"
	// Near: nearly the same text (R19a, ADR 0050).
	Near Dedup = "near"
)

// Result is what happened to a page, in full.
type Result struct {
	Outcome   Outcome
	MessageID int64 // 0 for an ignored page
	// Dedup is a duplicate's kind; "" when the page wasn't one.
	Dedup Dedup
	// Kept and Shape are a near duplicate's: why its message has the text it
	// has, and how the copies differ.
	Kept  neardup.Kept
	Shape neardup.Shape
	// Swapped: the copy's text replaced its message's (R19a), which was
	// re-parsed as a live page is: the live views show it again.
	Swapped bool
	// Variant: the copy was kept as its client's reception with its own text.
	Variant bool
	// Variants are the clients whose earlier copies now keep the message's
	// old text, after a swap.
	Variants []string
	// EventOpened: the page opened a large event (R87), which the side menu's
	// new-event dot is told of once it's committed (R77c).
	EventOpened bool
	// regroup is the sent time of a swapped message in a large event: the
	// events around it are rebuilt once the page is committed (R87f).
	regroup *time.Time
}

// Service ingests pages.
type Service struct {
	Store    *store.Store
	Network  *profile.Network
	Agencies map[string]*profile.Agency
	Streets  *streets.Index
	Geo      *geocode.Geocoder // nil: messages aren't placed
	// OnStored runs in the transaction that stores a new page (not a
	// duplicate copy), so work it queues commits with the page: the
	// integrations' deliveries (ADR 0006 §3). It runs again when a better
	// copy's text replaces a message's (R19a): it queues at most one
	// delivery for each integration and page (R51b), so an integration
	// already told isn't told again, and one the better copy now matches is.
	OnStored func(ctx context.Context, q *db.Queries, messageID int64) error
	// Events groups a new page into a large event, after OnStored and in the
	// same transaction (R87, ADR 0027); nil groups nothing.
	Events *events.Detector
	// Regroup rebuilds the large events around a message in one whose text a
	// better copy replaced (R19a, R87f), after the page is committed; nil
	// rebuilds none.
	Regroup *events.Regrouper
	// NearRatio is how near a copy's text must be to a message's to be the
	// same transmission (R19a, neardup.Allowed): 0 is neardup.DefaultRatio,
	// and below 0 turns near duplicates off.
	NearRatio float64
	// Log logs a failure after a page was stored (never its text, P1); nil
	// logs nothing.
	Log *slog.Logger
	// Tracer traces each step, under the caller's "ingest" span (R71); nil
	// traces nothing.
	Tracer trace.Tracer
}

// Ratio is the near-duplicate ratio in use (R19a): below 0, near
// duplicates are off.
func (s *Service) Ratio() float64 {
	if s.NearRatio == 0 {
		return neardup.DefaultRatio
	}
	return s.NearRatio
}

// Ingest processes one page from a client.
func (s *Service) Ingest(ctx context.Context, clientID string, pg Page) (Result, error) {
	// Every page, ignored ones included, updates the client's last-seen time (R16).
	if err := s.Store.TouchClient(ctx, db.TouchClientParams{ID: clientID, SeenAt: pg.ReceivedAt}); err != nil {
		return Result{}, store.SafeErr("touch client", err)
	}
	tr := telemetry.Or(s.Tracer)
	_, span := tr.Start(ctx, "ingest.redact") // control tokens, redaction, priority
	prep := pipeline.Prepare(s.Network, pg.Capcode, pg.Text)
	span.End()
	if prep.Dropped {
		return Result{Outcome: Ignored}, nil
	}

	pctx, span := tr.Start(ctx, "ingest.parse") // alias, template, agency profile
	defer span.End()                            // already ended, unless it failed
	cands, err := s.Candidates(pctx, s.Store.Queries, pg.Capcode)
	if err != nil {
		return Result{}, store.SafeErr("aliases", err)
	}
	draft, err := s.Draft(prep, cands, pg)
	if err != nil {
		return Result{}, err
	}
	draft.Client = clientID
	span.End()

	var res Result
	ctx, span = tr.Start(ctx, "ingest.store")
	defer span.End()
	err = s.Store.InTx(ctx, func(q *db.Queries) error {
		var err error
		res, err = s.Save(ctx, q, draft, func(id int64, text *string) error {
			return q.InsertReception(ctx, db.InsertReceptionParams{MessageID: id, ClientID: clientID, ReceivedAt: pg.ReceivedAt, Text: text})
		})
		return err
	})
	if err != nil {
		return Result{}, store.SafeErr("store page", err)
	}
	span.End()
	if res.regroup != nil && s.Regroup != nil {
		// The page is stored whatever this does: a failure is logged, and the
		// next re-parse's rebuild puts the events right.
		var aff events.Affected
		aff.Add(*res.regroup)
		ectx, espan := tr.Start(ctx, "ingest.events")
		if _, err := s.Regroup.Rebuild(ectx, s.Store, &aff); err != nil && s.Log != nil {
			s.Log.ErrorContext(ctx, "rebuild large events after a better copy", "message_id", res.MessageID, "error", store.SafeError(err))
		}
		espan.End()
	}
	return res, nil
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
	// Agency is the agency profile it was parsed with (nil: text only), and
	// Quality how well it parsed: a near duplicate's stored copy is parsed
	// with the same profile to compare them (R19a).
	Agency  *profile.Agency
	Quality neardup.Quality
	// Client is the receiving client: a message it already has a copy of
	// isn't one its near copy can be (R19a). "" for an import.
	Client string
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
	return Draft{Params: params, Fields: fields, ReceivedAt: pg.ReceivedAt, Ignored: ignored, Agency: agency, Quality: QualityOf(parsed)}, nil
}

// QualityOf is how well a page parsed, for choosing between near
// duplicates (R19a).
func QualityOf(p pipeline.Parsed) neardup.Quality {
	return neardup.Quality{Matched: !p.ParseFailed, Split: !p.AddressUnsplit, Fields: len(p.Fields)}
}

// Save merges a page into the message it duplicates (R19, R21), exactly or
// nearly (R19a), or stores it as a new one, in the caller's transaction.
// record notes who received this copy, with its own text when that isn't
// its message's: a client's reception, or an import's.
func (s *Service) Save(ctx context.Context, q *db.Queries, d Draft, record func(id int64, text *string) error) (Result, error) {
	tr := telemetry.Or(s.Tracer)
	// Serialise copies of pages to one capcode, so two clients can't both
	// store one page, whether their copies are the same or nearly so.
	dctx, dspan := tr.Start(ctx, "ingest.dedup")
	defer dspan.End()
	if err := q.LockCapcode(dctx, d.Params.Capcode); err != nil {
		return Result{}, err
	}
	dup, err := q.FindDuplicate(dctx, db.FindDuplicateParams{DedupKey: d.Params.DedupKey, ReceivedAt: d.ReceivedAt})
	switch {
	case err == nil:
		dspan.End()
		if err := q.KeepEarliestSentAt(ctx, db.KeepEarliestSentAtParams{ID: dup.ID, ReceivedAt: d.ReceivedAt}); err != nil {
			return Result{}, err
		}
		return Result{Outcome: Duplicate, MessageID: dup.ID, Dedup: Exact}, record(dup.ID, nil)
	case !errors.Is(err, pgx.ErrNoRows):
		return Result{}, err
	}
	near, m, ok, err := s.nearest(dctx, q, d)
	dspan.End()
	if err != nil {
		return Result{}, err
	}
	if ok {
		return s.merge(ctx, q, d, near, m, record)
	}

	// Placed before it's stored, in one write: an update afterwards would
	// write the page to every index twice.
	lctx, lspan := tr.Start(ctx, "ingest.locate")
	res, err := place(lctx, s.Geo, q, d.Fields)
	lspan.End()
	if err != nil {
		return Result{}, err
	}
	loc := geocode.SetLocationParams(0, res)
	d.Params.Lon, d.Params.Lat, d.Params.LocationSource, d.Params.LocationMismatch = loc.Lon, loc.Lat, loc.Source, loc.Mismatch
	id, err := q.InsertMessage(ctx, d.Params)
	if err != nil {
		return Result{}, err
	}
	if l := derivedLocality(d.Fields, res); l != "" {
		if err := q.SetMessageLocality(ctx, db.SetMessageLocalityParams{ID: id, Locality: l}); err != nil {
			return Result{}, err
		}
	}
	// The area it's in (R88a), from where it was placed.
	if loc.Lon != nil {
		if err := q.SetMessageArea(ctx, id); err != nil {
			return Result{}, err
		}
	}
	if err := record(id, nil); err != nil {
		return Result{}, err
	}
	opened, err := s.stored(ctx, q, d, id, d.Params.SentAt)
	if err != nil {
		return Result{}, err
	}
	return Result{Outcome: Stored, MessageID: id, EventOpened: opened}, nil
}

// stored tells the integrations and the large events of a page stored as
// message id, or of a message that took a better copy's text (R19a).
func (s *Service) stored(ctx context.Context, q *db.Queries, d Draft, id int64, sentAt time.Time) (opened bool, err error) {
	if s.OnStored != nil {
		if err := s.OnStored(ctx, q, id); err != nil {
			return false, err
		}
	}
	if s.Events == nil {
		return false, nil
	}
	ectx, espan := telemetry.Or(s.Tracer).Start(ctx, "ingest.events")
	defer espan.End()
	return s.Events.Stored(ectx, q, events.Page{ID: id, SentAt: sentAt, Text: d.Params.Text, Priority: d.Params.Priority,
		AliasID: d.Params.AliasID, Ignored: d.Ignored, Heartbeat: d.Params.Heartbeat, Job: d.Params.JobNumber, LargeEventField: d.Fields.LargeEventField})
}

// nearest is the message within the dedup window whose text the page's is
// nearest to, if it's near enough to be the same transmission (R19a).
func (s *Service) nearest(ctx context.Context, q *db.Queries, d Draft) (db.NearCandidatesRow, neardup.Match, bool, error) {
	ratio := s.Ratio()
	if ratio < 0 {
		return db.NearCandidatesRow{}, neardup.Match{}, false, nil
	}
	rows, err := q.NearCandidates(ctx, db.NearCandidatesParams{ClientID: d.Client, Capcode: d.Params.Capcode, ReceivedAt: d.ReceivedAt})
	if err != nil {
		return db.NearCandidatesRow{}, neardup.Match{}, false, err
	}
	best := -1
	var bm neardup.Match
	for i, r := range rows {
		// A receiver hears a transmission once: a message its client has a
		// copy of already is another transmission, a repeat or a different
		// page, and a page that differs from it (a second alarm, another
		// station) must not be merged into it. Nor a page of another job.
		if r.Heard || OtherJob(r.JobNumber, d.Params.JobNumber) {
			continue
		}
		m := neardup.Compare(r.Text, d.Params.Text, ratio)
		// The nearest text wins; a tie goes to the nearest in time (the
		// rows' order).
		if m.Near && (best < 0 || m.Distance < bm.Distance) {
			best, bm = i, m
		}
	}
	if best < 0 {
		return db.NearCandidatesRow{}, neardup.Match{}, false, nil
	}
	return rows[best], bm, true, nil
}

// OtherJob reports whether two copies name different job numbers (R23):
// then they're different pages, however near their texts (R19a, ADR 0050).
// Two incidents paged to one capcode within seconds in one format differ
// little but in the job, and merging them would lose the second's alert;
// a garbled job number that's still a job number is rare, and costs only
// a copy stored twice, as before near duplicates.
func OtherJob(a, b *string) bool {
	return a != nil && b != nil && *a != *b
}

// merge records a near duplicate (R19a, ADR 0050): the message keeps the
// copy that parses better, or the longer of a truncated pair, or the one it
// has; the other is kept as its client's reception with its own text. A
// better copy's text replaces the message's, which is then re-parsed,
// placed and grouped as a live page is.
func (s *Service) merge(ctx context.Context, q *db.Queries, d Draft, c db.NearCandidatesRow, m neardup.Match, record func(id int64, text *string) error) (Result, error) {
	prep := pipeline.Prepared{Capcode: d.Params.Capcode, Text: c.Text, Heartbeat: c.Heartbeat}
	if c.Priority != nil {
		prep.Priority = profile.Priority(*c.Priority)
	}
	storedQ := QualityOf(pipeline.Parse(s.Network, d.Agency, s.Streets, prep))
	swap, why := neardup.Choose(neardup.Copy{Text: c.Text, Quality: storedQ}, neardup.Copy{Text: d.Params.Text, Quality: d.Quality}, m)
	res := Result{Outcome: Duplicate, MessageID: c.ID, Dedup: Near, Kept: why, Shape: m.Shape}
	if !swap {
		if err := q.KeepEarliestSentAt(ctx, db.KeepEarliestSentAtParams{ID: c.ID, ReceivedAt: d.ReceivedAt}); err != nil {
			return Result{}, err
		}
		text := d.Params.Text
		res.Variant = true
		return res, record(c.ID, &text)
	}

	// The copies that had the old text keep it as their own, before this
	// copy's reception is written with none.
	rows, err := q.SwapReceptionTexts(ctx, db.SwapReceptionTextsParams{OldText: c.Text, ID: c.ID, NewText: d.Params.Text})
	if err != nil {
		return Result{}, err
	}
	for _, r := range rows {
		if r.Variant {
			res.Variants = append(res.Variants, r.ClientID)
		}
	}
	if err := q.SwapImportReceptionTexts(ctx, db.SwapImportReceptionTextsParams{OldText: c.Text, ID: c.ID, NewText: d.Params.Text}); err != nil {
		return Result{}, err
	}
	p, f := d.Params, d.Fields
	if err := q.SwapMessageText(ctx, db.SwapMessageTextParams{
		Text: p.Text, DedupKey: p.DedupKey, Priority: p.Priority, AliasID: p.AliasID, Format: f.Format, JobNumber: f.JobNumber,
		EventType: f.EventType, Description: f.Description, Address: f.Address, Locality: f.Locality, GridRef: f.GridRef,
		CallSign: f.CallSign, Units: f.Units, CallSigns: f.CallSigns, Extra: f.Extra, ParseFailed: f.ParseFailed,
		AddressUnsplit: f.AddressUnsplit, LargeEventField: f.LargeEventField, FiregroundChannels: f.Fireground, ReceivedAt: d.ReceivedAt, ID: c.ID,
	}); err != nil {
		return Result{}, err
	}
	// Where it's placed, its area and derived locality (R24, R88a), as a
	// re-parse writes them.
	lctx, lspan := telemetry.Or(s.Tracer).Start(ctx, "ingest.locate")
	err = Locate(lctx, s.Geo, q, c.ID, f)
	lspan.End()
	if err != nil {
		return Result{}, err
	}
	if err := record(c.ID, nil); err != nil {
		return Result{}, err
	}
	res.Swapped = true
	sentAt := c.SentAt
	if d.ReceivedAt.Before(sentAt) {
		sentAt = d.ReceivedAt
	}
	if c.InEvent {
		// Already in an event: the integrations hear of the new text, and the
		// events around it are rebuilt once it's committed, as after a
		// re-parse (R87f), which tells no integration again (R87d).
		res.regroup = &sentAt
		if s.OnStored != nil {
			return res, s.OnStored(ctx, q, c.ID)
		}
		return res, nil
	}
	// In no event: the new text may open or join one, as a live page's would.
	res.EventOpened, err = s.stored(ctx, q, d, c.ID, sentAt)
	return res, err
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
	Format, JobNumber, EventType, Description, Address, Locality, GridRef, CallSign *string
	Units, CallSigns                                                                []string
	Extra                                                                           []byte
	ParseFailed, AddressUnsplit                                                     bool
	// LargeEventField: a field holds a value its agency profile lists in
	// large_event_fields (R87). Stored, so it is what the page was parsed
	// with until it is re-parsed (R23a).
	LargeEventField bool
	// Fireground: the fireground channel numbers it names (R92a); nil for none.
	Fireground []int32
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
	callSigns, _ := parsed.Fields[profile.StandardCallSigns].([]string)
	return Fields{
		Format:          nilIfEmpty(parsed.Format),
		JobNumber:       str("job_number"),
		EventType:       str("event_type"),
		Description:     str("description"),
		Address:         str("address"),
		Locality:        str("locality"),
		GridRef:         str("grid_ref"),
		CallSign:        str("call_sign"),
		Units:           units,
		CallSigns:       callSigns,
		Extra:           extraJSON,
		ParseFailed:     parsed.ParseFailed,
		AddressUnsplit:  parsed.AddressUnsplit,
		LargeEventField: parsed.LargeEventField,
		Fireground:      parsed.Fireground,
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
		Capcode:            prep.Capcode,
		Text:               prep.Text,
		Priority:           nilIfEmpty(string(prep.Priority)),
		Heartbeat:          prep.Heartbeat,
		SentAt:             pg.ReceivedAt,
		DedupKey:           pipeline.DedupKey(prep),
		Format:             f.Format,
		JobNumber:          f.JobNumber,
		EventType:          f.EventType,
		Description:        f.Description,
		Address:            f.Address,
		Locality:           f.Locality,
		GridRef:            f.GridRef,
		CallSign:           f.CallSign,
		Units:              f.Units,
		CallSigns:          f.CallSigns,
		Extra:              f.Extra,
		ParseFailed:        f.ParseFailed,
		AddressUnsplit:     f.AddressUnsplit,
		LargeEventField:    f.LargeEventField,
		FiregroundChannels: f.Fireground,
		Function:           &fn,
		Baud:               &baud,
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
	if err := q.SetLocation(ctx, geocode.SetLocationParams(id, res)); err != nil {
		return err
	}
	// The area it's in follows where it's placed (R88a, R88d).
	if err := q.SetMessageArea(ctx, id); err != nil {
		return err
	}
	if l := derivedLocality(f, res); l != "" {
		return q.SetMessageLocality(ctx, db.SetMessageLocalityParams{ID: id, Locality: l})
	}
	return q.DeleteMessageLocality(ctx, id)
}

// derivedLocality is the locality kept beside a page (R24, ADR 0034): the
// dataset's locality the geocoder settled on, for a page whose profile gave
// no locality field. "" keeps none: a profile's own field wins.
func derivedLocality(f Fields, res geocode.Result) string {
	if f.Locality != nil {
		return ""
	}
	return res.Locality
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
