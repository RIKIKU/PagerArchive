// Package reparse re-parses stored messages when an alias, template or
// profile changes (R23a). Requests come from the reparse_requests table,
// written by any tool; the worker wakes on a PostgreSQL notification and
// also polls, so a change is never missed. Each re-parse is a run kept in
// the database (run.go, R23c), so it carries on after a restart, and an
// admin can pause, carry on, stop or start it again.
package reparse

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/streets"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

const (
	channel    = "pagerarchive_reparse"
	hashKey    = "profile_hash"
	batchSize  = 500
	everything = "%"
)

// Worker re-parses messages.
type Worker struct {
	Store       *store.Store
	Network     *profile.Network
	Agencies    map[string]*profile.Agency
	Streets     *streets.Index
	Geo         *geocode.Geocoder
	ProfileHash string // hash of the profiles and address dataset in use (ProfileHashes)
	// LegacyProfileHash is the same as releases before ADR 0045 worked it
	// out, from each agency profile's whole file: a saved hash equal to it
	// means nothing changed since, so the upgrade re-parses nothing. ""
	// matches no saved hash.
	LegacyProfileHash string
	Log               *slog.Logger
	Poll              time.Duration
	// Tracer traces each re-parse that has work (R72); nil traces nothing.
	Tracer trace.Tracer
	// Regroup rebuilds the large events around the re-parsed pages after
	// each re-parse (R87; owner, 2026-11-14; ADR 0027 §9); nil rebuilds none.
	Regroup *events.Regrouper
	// Progress is how far the re-parse has got, and its state, for /metrics
	// and the admin's status (R23b, R23c).
	Progress Progress
	// ProgressEvery is how often a run logs its progress: 30 s if 0.
	ProgressEvery time.Duration
}

// CheckProfiles queues a full re-parse if the profiles or address dataset
// changed since the last start (section 14: compared by file hashes). The
// new hash is saved in the same transaction. A run that hadn't finished
// (running, paused or stopped: interrupted by the restart, or held by an
// admin) has parsed its pages with the old profiles, so it starts again
// from the beginning, over every message, in the state it was in (R23c).
//
// A saved hash worked out as releases before ADR 0045 did, from the same
// files, is the same profiles: the new hash is saved in its place and
// nothing is re-parsed, so the upgrade that changed how it's worked out
// doesn't re-parse the archive.
func (w *Worker) CheckProfiles(ctx context.Context) error {
	old, err := w.Store.GetSetting(ctx, hashKey)
	if err == nil && old == w.ProfileHash {
		return nil
	}
	if err == nil && w.LegacyProfileHash != "" && old == w.LegacyProfileHash {
		if err := w.Store.SetSetting(ctx, db.SetSettingParams{Key: hashKey, Value: w.ProfileHash}); err != nil {
			return err
		}
		w.Log.InfoContext(ctx, "profile hash now leaves out display-only keys: nothing to re-parse")
		return nil
	}
	var again *db.ReparseRun
	err = w.Store.InTx(ctx, func(q *db.Queries) error {
		if err := q.RequestReparse(ctx, db.RequestReparseParams{CapcodePattern: everything, Reason: "profiles changed"}); err != nil {
			return err
		}
		r, err := q.LockReparseRun(ctx)
		switch {
		case err == nil && r.State != finished:
			if r, err = restart(ctx, q, r, stateOf(r.State)); err != nil {
				return err
			}
			again = &r
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		return q.SetSetting(ctx, db.SetSettingParams{Key: hashKey, Value: w.ProfileHash})
	})
	if err == nil && again != nil {
		w.Log.InfoContext(ctx, "re-parse starts again from the beginning: the profiles changed", "state", again.State, "reasons", again.Reasons, "total", again.Total)
	}
	return err
}

// Run processes requests until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := w.listen(ctx); err != nil && ctx.Err() == nil {
			w.Log.Error("re-parse worker", "error", store.SafeError(err))
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (w *Worker) listen(ctx context.Context) error {
	conn, err := w.Store.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return err
	}
	for {
		if _, err := w.RunOnce(ctx); err != nil {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, w.Poll)
		_, err := conn.Conn().WaitForNotification(wait)
		cancel()
		if err != nil && ctx.Err() != nil {
			return nil
		}
		if err != nil && wait.Err() == nil {
			return err // the connection failed, not the poll timeout
		}
	}
}

// RunOnce does what the re-parse has to do now (R23c): it carries on the
// run that's running, from the last batch done, or starts one from the
// queued requests, until that run finishes or an admin pauses, stops or
// starts it again. A paused or stopped run does nothing more (a stopped one
// first rebuilds the large events around the pages it did), and the
// requests wait. It returns how many messages it re-parsed.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	n := 0
	for {
		r, err := w.Store.GetReparseRun(ctx)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			r = db.ReparseRun{State: finished}
		case err != nil:
			return n, err
		}
		created := false
		if r.State == finished {
			if r.ID != 0 && w.Progress.Snapshot().Run == 0 {
				w.Progress.Hold(Idle, r.ID, r.Total, r.Done, r.StartedAt) // the last run's, after a restart
			}
			var ok bool
			if r, ok, err = claim(ctx, w.Store); err != nil || !ok {
				return n, err
			}
			created = true
		}
		if st := stateOf(r.State); st != Running {
			if st == Stopped {
				if err := w.rebuildEvents(ctx, r.ID); err != nil {
					return n, err
				}
			}
			w.Progress.Hold(st, r.ID, r.Total, r.Done, r.StartedAt)
			return n, nil
		}
		k, again, err := w.walk(ctx, r, created)
		n += k
		if err != nil || !again {
			return n, err
		}
	}
}

// walk re-parses run r's messages from where each pattern got to, a batch
// at a time, and finishes the run once they're all done. again is true when
// it stopped because the run changed (paused, stopped or started again).
func (w *Worker) walk(ctx context.Context, r db.ReparseRun, created bool) (n int, again bool, err error) {
	// Traced only when there's work, so the minute's poll makes no spans (R72).
	ctx, span := telemetry.Or(w.Tracer).Start(ctx, "reparse", trace.WithNewRoot())
	defer span.End()
	cands, err := w.candidates(ctx)
	if err != nil {
		return 0, false, err
	}
	pats, err := w.Store.ReparseRunPatterns(ctx, r.ID)
	if err != nil {
		return 0, false, err
	}
	w.Progress.Begin(r.ID, r.Total, r.Done, r.StartedAt, time.Now())
	if created {
		w.Log.InfoContext(ctx, "re-parse started", "reasons", r.Reasons, "requests", r.Requests, "patterns", len(pats), "total", r.Total)
	} else {
		w.Log.InfoContext(ctx, "re-parse carried on", "state", string(Running), "reasons", r.Reasons, "done", r.Done, "total", r.Total)
	}
	stopLog := w.logProgress(ctx)
	defer stopLog()
	for _, p := range pats {
		if p.Done {
			continue
		}
		after := p.AfterID
		for {
			if w.Progress.Interrupted() {
				return n, true, nil
			}
			rows, err := w.Store.MessagesToReparse(ctx, db.MessagesToReparseParams{Pattern: p.CapcodePattern, AfterID: after, Batch: batchSize})
			if err != nil {
				return n, false, err
			}
			b, err := w.batch(ctx, r, cands, p.CapcodePattern, after, rows, len(rows) < batchSize)
			n += b.n
			if err != nil {
				return n, false, err
			}
			if b.changed || b.cut {
				return n, true, nil
			}
			after = b.last
			if len(rows) < batchSize {
				break
			}
		}
	}
	stopLog()
	// Every pattern is done: the large events around its pages are rebuilt,
	// and it's finished. Until then it's running (R87f).
	if err := w.rebuildEvents(ctx, r.ID); err != nil {
		return n, false, err
	}
	changed := false
	err = w.Store.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.LockReparseRun(ctx)
		if err != nil {
			return err
		}
		if cur.ID != r.ID || cur.Epoch != r.Epoch {
			changed = true
			return nil
		}
		return q.FinishReparseRun(ctx, r.ID)
	})
	if err != nil || changed {
		return n, changed, err
	}
	w.Progress.Finish()
	span.SetAttributes(attribute.Int("pagerarchive.reparse.messages", n))
	perMin, _ := w.Progress.Snapshot().Rate(time.Now())
	w.Log.InfoContext(ctx, "re-parsed messages", "count", n, "requests", r.Requests, "seconds", int64(time.Since(r.StartedAt).Seconds()), "per_minute", int64(perMin))
	return n, false, nil
}

// batchResult is how a batch went: n pages done and the last one's ID;
// changed when the run was started again meanwhile (the pages are done, its
// place isn't written), cut when an admin's change stopped it part way or it
// was paused or stopped meanwhile (the pages done so far are kept, with
// their place).
type batchResult struct {
	n            int
	last         int64
	changed, cut bool
}

// batch re-parses one batch of a pattern's messages, and writes where the
// pattern got to, its run's done and the days its pages were sent, in one
// transaction (R23c): each page is done or not, never half done, and what
// the run says is done is.
func (w *Worker) batch(ctx context.Context, r db.ReparseRun, cands []ingest.Candidate, pattern string, after int64, rows []db.MessagesToReparseRow, last bool) (batchResult, error) {
	var b batchResult
	err := w.Store.InTx(ctx, func(q *db.Queries) error {
		b = batchResult{last: after}
		var days events.Affected
		for _, m := range rows {
			if w.Progress.Interrupted() {
				b.cut = true
				break
			}
			if err := w.reparse(ctx, q, cands, m, &days); err != nil {
				return err
			}
			b.n++
			b.last = m.ID
			w.Progress.Add()
		}
		cur, err := q.LockReparseRun(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			b.changed = true
			return nil
		} else if err != nil {
			return err
		}
		if cur.ID != r.ID || cur.Epoch != r.Epoch {
			b.changed = true
			return nil
		}
		for _, s := range days.Spans() {
			if err := q.AddReparseRunDay(ctx, db.AddReparseRunDayParams{RunID: r.ID, Day: s.From.UTC().Unix() / 86400, FromAt: s.From, ToAt: s.To}); err != nil {
				return err
			}
		}
		if err := q.SetReparseRunPattern(ctx, db.SetReparseRunPatternParams{AfterID: b.last, Done: last && !b.cut, RunID: r.ID, Pattern: pattern}); err != nil {
			return err
		}
		if err := q.AddReparseRunDone(ctx, db.AddReparseRunDoneParams{N: int64(b.n), ID: r.ID}); err != nil {
			return err
		}
		if stateOf(cur.State) != Running {
			b.cut = true
		}
		return nil
	})
	if err != nil {
		w.Progress.Undo(b.n)
		b.n = 0
	}
	return b, err
}

// rebuildEvents rebuilds the large events around the pages run id has done
// (R87f), from the days its batches kept, then forgets the days: done again
// after a crash part way, it gives the same events.
func (w *Worker) rebuildEvents(ctx context.Context, id int64) error {
	days, err := w.Store.ReparseRunDays(ctx, id)
	if err != nil || len(days) == 0 {
		return err
	}
	var aff events.Affected
	for _, d := range days {
		aff.Add(d.FromAt)
		aff.Add(d.ToAt)
	}
	if err := w.regroup(ctx, &aff); err != nil {
		return err
	}
	return w.Store.DeleteReparseRunDays(ctx, id)
}

// candidates are every alias, for picking each message's best one.
func (w *Worker) candidates(ctx context.Context) ([]ingest.Candidate, error) {
	aliases, err := w.Store.AllAliases(ctx)
	if err != nil {
		return nil, err
	}
	cands := make([]ingest.Candidate, len(aliases))
	for i, a := range aliases {
		cands[i] = ingest.Candidate{ID: a.ID, Pattern: a.Pattern, AgencyProfile: a.AgencyProfile, Ignored: a.Ignored}
	}
	return cands, nil
}

// Candidates are every alias, for picking each message's best one: read
// once for a batch of ReparseInTx.
func (w *Worker) Candidates(ctx context.Context) ([]ingest.Candidate, error) {
	return w.candidates(ctx)
}

// ReparseInTx re-parses (and re-places) message m in q's transaction, with
// the text it has in that transaction: retroactive redaction (P1, ADR 0044)
// writes a page's new text and the fields parsed from it in one. A page the
// large events are rebuilt around (R87f) is added to aff, for RebuildEvents.
func (w *Worker) ReparseInTx(ctx context.Context, q *db.Queries, cands []ingest.Candidate, m db.MessagesToReparseRow, aff *events.Affected) error {
	return w.reparse(ctx, q, cands, m, aff)
}

// RebuildEvents rebuilds the large events around the pages aff holds (R87f).
func (w *Worker) RebuildEvents(ctx context.Context, aff *events.Affected) error {
	return w.regroup(ctx, aff)
}

// regroup rebuilds the large events around the re-parsed pages that aff
// holds (R87; owner, 2026-11-14). It sends nothing (R87d).
func (w *Worker) regroup(ctx context.Context, aff *events.Affected) error {
	if w.Regroup == nil || aff.Len() == 0 {
		return nil
	}
	ctx, span := telemetry.Or(w.Tracer).Start(ctx, "reparse.events")
	defer span.End()
	start := time.Now()
	r, err := w.Regroup.Rebuild(ctx, w.Store, aff)
	if err != nil {
		return err
	}
	span.SetAttributes(attribute.Int("pagerarchive.reparse.event_batches", r.Batches), attribute.Int("pagerarchive.reparse.event_pages", r.Pages))
	w.Log.InfoContext(ctx, "rebuilt large events", "batches", r.Batches, "pages", r.Pages, "events", r.Events,
		"created", r.Created, "deleted", r.Deleted, "moved", r.Moved, "ms", time.Since(start).Milliseconds(), "longest_batch_ms", r.Longest.Milliseconds())
	return nil
}

// reparse re-parses one message. A page in a large event, or a live page
// that now counts and has a job or a code, is added to aff: the events around
// it are rebuilt afterwards (Worker.regroup). Its fields are written only
// beside the text they were parsed from (P1): if retroactive redaction
// changed the text after it was read, the page is read again and parsed
// from what it has now.
func (w *Worker) reparse(ctx context.Context, q *db.Queries, cands []ingest.Candidate, m db.MessagesToReparseRow, aff *events.Affected) error {
	for range 3 {
		ok, err := w.reparseAs(ctx, q, cands, m, aff)
		if err != nil || ok {
			return err
		}
		rows, err := q.MessagesByIDs(ctx, []int64{m.ID})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil // deleted meanwhile (retention)
		}
		m = db.MessagesToReparseRow(rows[0])
	}
	return errors.New("a page's text kept changing while it was re-parsed")
}

// reparseAs re-parses message m as read. ok is false, and nothing is
// written, when its text is no longer m's.
func (w *Worker) reparseAs(ctx context.Context, q *db.Queries, cands []ingest.Candidate, m db.MessagesToReparseRow, aff *events.Affected) (ok bool, err error) {
	var matching []ingest.Candidate
	for _, c := range cands {
		if ingest.LikeMatch(c.Pattern, m.Capcode) {
			matching = append(matching, c)
		}
	}
	var aliasID *int64
	var agency *profile.Agency
	ignored := false
	if best := ingest.BestAlias(matching); best != nil {
		aliasID = &best.ID
		ignored = best.Ignored
		if best.AgencyProfile != nil {
			agency = w.Agencies[*best.AgencyProfile]
		}
	}
	prep := pipeline.Prepared{Capcode: m.Capcode, Text: m.Text, Heartbeat: m.Heartbeat}
	if m.Priority != nil {
		prep.Priority = profile.Priority(*m.Priority)
	}
	f, err := ingest.FieldsOf(pipeline.Parse(w.Network, agency, w.Streets, prep))
	if err != nil {
		return false, err
	}
	written, err := q.UpdateParsed(ctx, db.UpdateParsedParams{
		ID: m.ID, AliasID: aliasID, Format: f.Format, JobNumber: f.JobNumber, EventType: f.EventType,
		Description: f.Description, Address: f.Address, Locality: f.Locality, GridRef: f.GridRef,
		CallSign: f.CallSign, Units: f.Units, CallSigns: f.CallSigns, Extra: f.Extra, ParseFailed: f.ParseFailed, AddressUnsplit: f.AddressUnsplit,
		LargeEventField: f.LargeEventField, Text: m.Text,
	})
	if err != nil || written == 0 {
		return false, err
	}
	// R35h (d): a page with a format was really heard, so none of its
	// capcode's pages is probably noise (UpdateParsed cleared its own mark).
	if f.Format != nil {
		if err := q.ClearCapcodeNoise(ctx, m.Capcode); err != nil {
			return false, err
		}
	}
	p := events.Page{ID: m.ID, SentAt: m.SentAt, Text: m.Text, Priority: m.Priority, AliasID: aliasID, Ignored: ignored, Heartbeat: m.Heartbeat, Job: f.JobNumber}
	if m.InEvent || (m.Live && p.Counts() && (p.Job != nil || f.LargeEventField || events.HasCode(w.Network, m.Text))) {
		aff.Add(m.SentAt)
	}
	return true, ingest.Locate(ctx, w.Geo, q, m.ID, f)
}
