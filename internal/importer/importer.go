// Package importer moves pages from an existing archive into PagerArchive
// (R83-R83f, ADR 0018). It reads JSON Lines, puts each page through the same
// code as a live page (internal/pipeline, ingest.Service), and is quiet: no
// integration, live update or client's last-seen time hears of it.
//
// It runs in two stages, so the result doesn't depend on the order of the
// file. The first reads and checks every line and puts the redacted pages in
// a temporary table. The second takes them from that table in time order and
// stores them in batches. Page text is never logged or reported, and the
// unredacted text never leaves this process (P1).
package importer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

const (
	// BatchSize is how many pages one transaction stores. A live copy of a
	// page in the batch waits for it (ingest.Save's lock), so it stays short.
	BatchSize = 500
	// maxLine is the longest line read, as for a page posted to the API.
	maxLine = 64 << 10
	// maxSourceID is the longest source_id kept: an ID, not a document.
	maxSourceID = 200
	// advisoryLock allows one import at a time.
	advisoryLock = 4242_0083
)

// Why a line is refused. Each is a fixed text: a reason never quotes the line
// or a decoder's error, which can quote it (P1).
const (
	reasonLong    = "the line is longer than 64 KiB"
	reasonUTF8    = "the line isn't valid UTF-8"
	reasonJSON    = "not valid JSON"
	reasonFields  = "not a JSON object of strings with only received_at, capcode, text and source_id"
	reasonTime    = "received_at must be RFC 3339 with its offset"
	reasonFuture  = "received_at is more than 60 s ahead of the server's clock"
	reasonCapcode = "capcode must be 1 to 7 digits"
	reasonText    = "text must be 1 to 4096 bytes"
	reasonTextNUL = "text holds a NUL character, which can't be stored"
	reasonSource  = "source_id must be 1 to 200 bytes, with no NUL character"
)

var capcodeRe = regexp.MustCompile(`^[0-9]{1,7}$`)

// ErrRunning says another import holds the lock, and nothing was done.
var ErrRunning = errors.New("another import is running")

// Report is what a run did (R83e). It holds counts only.
type Report struct {
	DryRun          bool
	Lines           int64 // lines read
	Stored          int64 // new messages
	Duplicates      int64 // copies merged into a message (R19)
	AlreadyImported int64 // lines an earlier run stored (R83c)
	ParseFailed     int64 // of Stored, flagged parse_failed (R4)
	TooOld          int64 // older than the retention period (R83d)
	Ignored         int64 // ignored capcodes (R16)
	Refused         int64 // each reported by line number
	SourceIDsGone   int64 // a redaction rule matched them: known by dedup key and time
}

// Write prints the report. A dry run says what a real run would do.
func (r Report) Write(w io.Writer) {
	stored, merged := "pages stored", "duplicates merged"
	if r.DryRun {
		fmt.Fprintln(w, "dry run: nothing was stored")
		stored, merged = "pages that would be stored", "duplicates that would be merged"
	}
	for _, l := range []struct {
		name string
		n    int64
	}{
		{"lines read", r.Lines},
		{stored, r.Stored},
		{merged, r.Duplicates},
		{"already imported", r.AlreadyImported},
		{"parse failures", r.ParseFailed},
		{"skipped by retention", r.TooOld},
		{"ignored capcodes", r.Ignored},
		{"refused lines", r.Refused},
		{"source IDs not kept (a redaction rule matches them)", r.SourceIDsGone},
	} {
		fmt.Fprintf(w, "%-52s %d\n", l.name, l.n)
	}
}

// Importer imports one file.
type Importer struct {
	Store    *store.Store
	Ingest   *ingest.Service // the live path; its OnStored is never called (R83b)
	Network  *profile.Network
	Cutoff   time.Time // pages sent before it are skipped (R83d)
	Now      func() time.Time
	DryRun   bool
	Report   io.Writer     // refused lines, as they're found
	Progress io.Writer     // how far it has got
	LockWait time.Duration // how long to wait for another import to end
	// AfterBatch, for tests, runs after each batch is committed; an error
	// stops the import as if it had been killed.
	AfterBatch func(done int64) error
}

// Run imports r. A failed or interrupted run returns what it did so far and
// an error that holds no page text; running the file again finishes it (R83c).
func (im *Importer) Run(ctx context.Context, r io.Reader) (Report, error) {
	rep := Report{DryRun: im.DryRun}
	// R83b: quiet, whatever service it's given.
	quiet := *im.Ingest
	quiet.OnStored = nil
	// Nor do they group into large events (ADR 0027): the import stays as
	// fast as R83f asks, and an imported page is no news.
	quiet.Events = nil
	im.Ingest = &quiet

	conn, err := im.Store.Pool.Acquire(ctx)
	if err != nil {
		return rep, fmt.Errorf("database: %s", store.SafeError(err))
	}
	// The temporary tables and the lock go with the session: end it now, so
	// the next run needn't wait for the connection's backend to notice.
	defer func() {
		_, _ = conn.Exec(context.Background(), `DISCARD ALL`)
		_ = conn.Conn().Close(context.Background())
		conn.Release()
	}()
	if err := im.lock(ctx, conn); err != nil {
		return rep, err
	}

	started := time.Now()
	n, err := im.stage(ctx, conn, r, &rep)
	if err != nil {
		return rep, err
	}
	fmt.Fprintf(im.Progress, "read %d lines in %s; indexing them by time\n", rep.Lines, time.Since(started).Round(time.Second))
	indexed := time.Now()
	total, size, err := im.index(ctx, conn, n, &rep)
	if err != nil {
		return rep, err
	}
	fmt.Fprintf(im.Progress, "indexed %d pages in %s, %s on the database's disk until the import ends\n", total, time.Since(indexed).Round(time.Second), size)
	if err := im.store(ctx, conn, total, &rep); err != nil {
		return rep, err
	}
	if im.DryRun {
		return rep, nil
	}
	// Leave nothing to catch up. Searches plan by these statistics (ADR 0017),
	// and straight after an import, before vacuum had run, a search took 17 s
	// (ADR 0018). When the command returns, they meet R33b.
	fmt.Fprintln(im.Progress, "vacuuming and analysing the messages")
	if _, err := conn.Exec(ctx, `VACUUM (ANALYZE) messages`); err != nil {
		return rep, fmt.Errorf("vacuum: %s", store.SafeError(err))
	}
	return rep, nil
}

// lock takes the one import's lock. A run that was just killed holds it until
// its backend notices, so it waits for LockWait before saying so.
func (im *Importer) lock(ctx context.Context, conn *pgxpool.Conn) error {
	end := time.Now().Add(im.LockWait)
	for {
		var got bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, int64(advisoryLock)).Scan(&got); err != nil {
			return fmt.Errorf("database: %s", store.SafeError(err))
		}
		if got {
			return nil
		}
		if time.Now().After(end) {
			return ErrRunning
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// staged is one page in the temporary table: redacted, never the raw text.
type staged struct {
	at       time.Time
	prep     pipeline.Prepared
	sourceID string // "" means none
}

// stage reads the file into import_lines. Only what Prepare returns reaches
// the database, so a redacted detail never does (P1).
func (im *Importer) stage(ctx context.Context, conn *pgxpool.Conn, r io.Reader, rep *Report) (int64, error) {
	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE import_lines (
		received_at timestamptz NOT NULL, capcode text NOT NULL, text text NOT NULL,
		priority text NOT NULL, heartbeat boolean NOT NULL, source_id text NOT NULL)`); err != nil {
		return 0, fmt.Errorf("database: %s", store.SafeError(err))
	}
	src := &lineSource{im: im, r: bufio.NewReaderSize(r, maxLine+1), rep: rep, now: im.Now(), last: time.Now()}
	n, err := conn.Conn().CopyFrom(ctx, pgx.Identifier{"import_lines"},
		[]string{"received_at", "capcode", "text", "priority", "heartbeat", "source_id"}, src)
	if src.err != nil {
		return 0, src.err
	}
	if err != nil {
		return 0, fmt.Errorf("database: %s", store.SafeError(err))
	}
	return n, nil
}

// lineSource feeds the file to COPY, one checked line at a time.
type lineSource struct {
	im   *Importer
	r    *bufio.Reader
	rep  *Report
	now  time.Time
	last time.Time
	row  []any
	err  error
}

func (s *lineSource) Next() bool {
	for {
		line, long, end, err := s.readLine()
		if err != nil {
			s.err = fmt.Errorf("reading the file: %w", err)
			return false
		}
		if end {
			return false
		}
		s.rep.Lines++
		if s.rep.Lines == 1 {
			line = bytes.TrimPrefix(line, []byte(string(rune(0xFEFF))))
		}
		if time.Since(s.last) > 10*time.Second {
			s.last = time.Now()
			fmt.Fprintf(s.im.Progress, "read %d lines\n", s.rep.Lines)
		}
		page, reason := checked{}, reasonLong
		if !long {
			page, reason = check(line, s.now)
		}
		if reason != "" {
			s.rep.Refused++
			fmt.Fprintf(s.im.Report, "line %d: %s\n", s.rep.Lines, reason)
			continue
		}
		if page.at.Before(s.im.Cutoff) {
			s.rep.TooOld++
			continue
		}
		prep := pipeline.Prepare(s.im.Network, decoder.PadCapcode(page.capcode), page.text)
		if prep.Dropped {
			s.rep.Ignored++
			continue
		}
		// An ID a redaction rule matches isn't kept (P1). The page is, and
		// is known by its dedup key and receive time, as one with no ID.
		if page.sourceID != "" && pipeline.Redact(s.im.Network, page.sourceID) != page.sourceID {
			page.sourceID = ""
			s.rep.SourceIDsGone++
		}
		s.row = []any{page.at, prep.Capcode, prep.Text, string(prep.Priority), prep.Heartbeat, page.sourceID}
		return true
	}
}

func (s *lineSource) Values() ([]any, error) { return s.row, nil }
func (s *lineSource) Err() error             { return s.err }

// readLine returns the next line without its ending. long says it was over
// the limit and has been skipped; end says there are no more.
func (s *lineSource) readLine() (line []byte, long, end bool, err error) {
	line, err = s.r.ReadSlice('\n')
	for errors.Is(err, bufio.ErrBufferFull) {
		long = true
		_, err = s.r.ReadSlice('\n')
	}
	switch {
	case err != nil && !errors.Is(err, io.EOF):
		return nil, false, false, err
	case long || len(line) > maxLine:
		return nil, true, false, nil
	case len(line) == 0:
		return nil, false, true, nil
	}
	return bytes.TrimRight(line, "\r\n"), false, false, nil
}

// checked is one line that passed check.
type checked struct {
	at                      time.Time
	capcode, text, sourceID string
}

// check validates one line, by the same limits as a page posted to the API.
// It returns the reason it's refused, or "". The reason is always one of the
// fixed texts above, so it can't carry the line's text (P1).
func check(line []byte, now time.Time) (checked, string) {
	if !utf8.Valid(line) {
		return checked{}, reasonUTF8
	}
	var rec struct {
		ReceivedAt *string `json:"received_at"`
		Capcode    *string `json:"capcode"`
		Text       *string `json:"text"`
		SourceID   *string `json:"source_id"`
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return checked{}, reasonJSON
		}
		return checked{}, reasonFields
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return checked{}, reasonJSON
	}
	if rec.ReceivedAt == nil {
		return checked{}, reasonTime
	}
	at, err := time.Parse(time.RFC3339, *rec.ReceivedAt)
	if err != nil {
		return checked{}, reasonTime
	}
	// As PostgreSQL keeps it, so a second run compares equal.
	at = at.UTC().Truncate(time.Microsecond)
	switch {
	case at.After(now.Add(ingest.MaxFuture)):
		return checked{}, reasonFuture
	case rec.Capcode == nil || !capcodeRe.MatchString(*rec.Capcode):
		return checked{}, reasonCapcode
	case rec.Text == nil || *rec.Text == "" || len(*rec.Text) > ingest.MaxTextBytes:
		return checked{}, reasonText
	case strings.IndexByte(*rec.Text, 0) >= 0:
		return checked{}, reasonTextNUL
	}
	c := checked{at: at, capcode: *rec.Capcode, text: *rec.Text}
	if rec.SourceID != nil {
		if *rec.SourceID == "" || len(*rec.SourceID) > maxSourceID || strings.IndexByte(*rec.SourceID, 0) >= 0 {
			return checked{}, reasonSource
		}
		c.sourceID = *rec.SourceID
	}
	return c, ""
}

// index makes the staged pages readable in time order, without sorting them all:
// a sort of a whole file writes it to disk twice more, in a burst that holds up
// a live page's commit (ADR 0018). Each batch is one short query, so no snapshot
// is held for hours and vacuum can run throughout. A dry run removes a repeated
// source ID here, which a real run finds in its earlier batches.
func (im *Importer) index(ctx context.Context, conn *pgxpool.Conn, staged int64, rep *Report) (int64, string, error) {
	for _, sql := range []string{`CREATE INDEX ON import_lines (received_at)`, `ANALYZE import_lines`} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			return 0, "", fmt.Errorf("indexing: %s", store.SafeError(err))
		}
	}
	if im.DryRun {
		tag, err := conn.Exec(ctx, `DELETE FROM import_lines WHERE ctid IN (
			SELECT ctid FROM (
				SELECT ctid, row_number() OVER (PARTITION BY source_id ORDER BY received_at, capcode COLLATE "C", text COLLATE "C") AS n
				FROM import_lines
				WHERE source_id IN (SELECT source_id FROM import_lines WHERE source_id <> '' GROUP BY source_id HAVING count(*) > 1)) r
			WHERE n > 1)`)
		if err != nil {
			return 0, "", fmt.Errorf("indexing: %s", store.SafeError(err))
		}
		rep.AlreadyImported += tag.RowsAffected()
		staged -= tag.RowsAffected()
	}
	var size string
	if err := conn.QueryRow(ctx, `SELECT pg_size_pretty(pg_total_relation_size('import_lines'))`).Scan(&size); err != nil {
		return 0, "", fmt.Errorf("indexing: %s", store.SafeError(err))
	}
	return staged, size, nil
}

// store takes the staged pages in time order, in batches.
func (im *Importer) store(ctx context.Context, conn *pgxpool.Conn, total int64, rep *Report) error {
	var done int64
	after := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	started, last := time.Now(), time.Now()
	dry := &dryRun{first: map[[32]byte]time.Time{}, copies: map[copyID]bool{}}
	for {
		pages, err := nextPages(ctx, conn, after)
		if err != nil {
			return fmt.Errorf("reading the staged pages: %s", store.SafeError(err))
		}
		if len(pages) == 0 {
			return nil
		}
		after = pages[len(pages)-1].at
		for batch := range slices.Chunk(pages, BatchSize) {
			if im.DryRun {
				err = im.checkBatch(ctx, batch, rep, dry)
			} else {
				err = im.storeBatch(ctx, batch, rep)
			}
			if err != nil {
				return err
			}
			done += int64(len(batch))
			if im.AfterBatch != nil {
				if err := im.AfterBatch(done); err != nil {
					return err
				}
			}
			if time.Since(last) > 10*time.Second {
				last = time.Now()
				fmt.Fprintf(im.Progress, "%d of %d pages, %.0f a second\n", done, total, float64(done)/time.Since(started).Seconds())
			}
		}
	}
}

// nextPages returns the next batch's worth of pages after a time, and every
// other page with the last one's time. The order goes down to the text, so
// pages with one time are stored in one order whatever the file's order was.
func nextPages(ctx context.Context, conn *pgxpool.Conn, after time.Time) ([]staged, error) {
	upto := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC) // none that far on: the rest
	err := conn.QueryRow(ctx, `SELECT received_at FROM import_lines WHERE received_at > $1 ORDER BY received_at OFFSET $2 LIMIT 1`, after, BatchSize-1).Scan(&upto)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	rows, err := conn.Query(ctx, `SELECT received_at, capcode, text, priority, heartbeat, source_id
		FROM import_lines WHERE received_at > $1 AND received_at <= $2
		ORDER BY received_at, capcode COLLATE "C", text COLLATE "C", source_id COLLATE "C"`, after, upto)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (staged, error) {
		var p staged
		var priority string
		err := row.Scan(&p.at, &p.prep.Capcode, &p.prep.Text, &priority, &p.prep.Heartbeat, &p.sourceID)
		p.prep.Priority = profile.Priority(priority)
		p.at = p.at.UTC()
		return p, err
	})
}

// counts is what one batch did, added to the report once it's committed.
type counts struct{ stored, duplicates, already, parseFailed int64 }

func (r *Report) add(c counts) {
	r.Stored += c.stored
	r.Duplicates += c.duplicates
	r.AlreadyImported += c.already
	r.ParseFailed += c.parseFailed
}

// storeBatch stores one batch in one transaction, so a run that stops leaves
// whole batches behind.
func (im *Importer) storeBatch(ctx context.Context, batch []staged, rep *Report) error {
	var c counts
	err := pgx.BeginFunc(ctx, im.Store.Pool, func(tx pgx.Tx) error {
		// A batch lost in a crash is stored by running the file again (R83c),
		// so its commit needn't wait for the disk, or hold up a live page's.
		if _, err := tx.Exec(ctx, `SET LOCAL synchronous_commit = off`); err != nil {
			return err
		}
		q := im.Store.Queries.WithTx(tx)
		var err error
		c, err = im.walk(ctx, q, batch, func(p staged, draft ingest.Draft) (ingest.Outcome, error) {
			outcome, _, err := im.Ingest.Save(ctx, q, draft, func(id int64) error {
				return q.InsertImportReception(ctx, db.InsertImportReceptionParams{MessageID: id, ReceivedAt: p.at, SourceID: sourceParam(p.sourceID)})
			})
			return outcome, err
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("storing pages: %s", store.SafeError(err))
	}
	rep.add(c)
	return nil
}

// checkBatch is storeBatch's dry run: the same checks, read only.
func (im *Importer) checkBatch(ctx context.Context, batch []staged, rep *Report, seen *dryRun) error {
	q := im.Store.Queries
	c, err := im.walk(ctx, q, batch, func(p staged, draft ingest.Draft) (ingest.Outcome, error) {
		return seen.outcome(ctx, q, p, draft)
	})
	if err != nil {
		return fmt.Errorf("checking pages: %s", store.SafeError(err))
	}
	rep.add(c)
	seen.forgetBefore(batch[len(batch)-1].at.Add(-ingest.DedupWindow))
	return nil
}

// walk goes through a batch in order: a page an earlier run stored is counted
// and skipped (R83c), and the others are parsed and given to save.
func (im *Importer) walk(ctx context.Context, q *db.Queries, batch []staged, save func(staged, ingest.Draft) (ingest.Outcome, error)) (counts, error) {
	var c counts
	seen, err := seenSources(ctx, q, batch)
	if err != nil {
		return c, err
	}
	aliases := map[string][]ingest.Candidate{}
	for _, p := range batch {
		if p.sourceID != "" && seen[p.sourceID] {
			c.already++
			continue
		}
		draft, err := im.draft(ctx, q, aliases, p)
		if err != nil {
			return c, err
		}
		if p.sourceID == "" {
			stored, err := q.ImportedCopyExists(ctx, db.ImportedCopyExistsParams{DedupKey: draft.Params.DedupKey, ReceivedAt: p.at})
			if err != nil {
				return c, err
			}
			if stored {
				c.already++
				continue
			}
		}
		outcome, err := save(p, draft)
		if err != nil {
			return c, err
		}
		if p.sourceID != "" {
			seen[p.sourceID] = true
		}
		switch outcome {
		case ingest.Stored:
			c.stored++
			if draft.Params.ParseFailed {
				c.parseFailed++
			}
		case ingest.Duplicate:
			c.duplicates++
		case alreadyInFile:
			c.already++
		}
	}
	return c, nil
}

// alreadyInFile is a dry run's outcome for a copy with no source ID that an
// earlier line holds: a real run finds it stored by then.
const alreadyInFile ingest.Outcome = "already in the file"

// dryRun remembers what a real run would have stored by now, as those pages
// aren't in the database. The pages come in time order, so only the last 10
// seconds matter (R19).
type dryRun struct {
	first  map[[32]byte]time.Time // each dedup key's first copy
	copies map[copyID]bool        // the copies with no source ID
}

type copyID struct {
	key [32]byte
	at  time.Time
}

func (d *dryRun) outcome(ctx context.Context, q *db.Queries, p staged, draft ingest.Draft) (ingest.Outcome, error) {
	key := [32]byte(draft.Params.DedupKey)
	if p.sourceID == "" {
		id := copyID{key, p.at}
		if d.copies[id] {
			return alreadyInFile, nil
		}
		d.copies[id] = true
	}
	first, inFile := d.first[key]
	_, err := q.FindDuplicate(ctx, db.FindDuplicateParams{DedupKey: draft.Params.DedupKey, ReceivedAt: p.at})
	switch {
	case err == nil, inFile && p.at.Sub(first) <= ingest.DedupWindow:
		return ingest.Duplicate, nil
	case errors.Is(err, pgx.ErrNoRows):
		d.first[key] = p.at
		return ingest.Stored, nil
	}
	return "", err
}

func (d *dryRun) forgetBefore(t time.Time) {
	for k, at := range d.first {
		if at.Before(t) {
			delete(d.first, k)
		}
	}
	for id := range d.copies {
		if id.at.Before(t) {
			delete(d.copies, id)
		}
	}
}

// draft parses a page as ingest does, with the batch's copy of its capcode's
// aliases.
func (im *Importer) draft(ctx context.Context, q *db.Queries, aliases map[string][]ingest.Candidate, p staged) (ingest.Draft, error) {
	cands, ok := aliases[p.prep.Capcode]
	if !ok {
		var err error
		if cands, err = im.Ingest.Candidates(ctx, q, p.prep.Capcode); err != nil {
			return ingest.Draft{}, err
		}
		aliases[p.prep.Capcode] = cands
	}
	draft, err := im.Ingest.Draft(p.prep, cands, ingest.Page{Capcode: p.prep.Capcode, ReceivedAt: p.at})
	if err != nil {
		return ingest.Draft{}, err
	}
	// A file says nothing of the radio (function code and baud rate).
	draft.Params.Function, draft.Params.Baud = nil, nil
	return draft, nil
}

func seenSources(ctx context.Context, q *db.Queries, batch []staged) (map[string]bool, error) {
	ids := make([]string, 0, len(batch))
	for _, p := range batch {
		if p.sourceID != "" {
			ids = append(ids, p.sourceID)
		}
	}
	seen := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return seen, nil
	}
	found, err := q.ImportedSourceIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range found {
		seen[id] = true
	}
	return seen, nil
}

func sourceParam(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
