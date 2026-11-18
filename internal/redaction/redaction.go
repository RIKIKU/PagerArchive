// Package redaction is retroactive redaction (P1, R45a, ADR 0044): when the
// network profile's redaction rules change, the nightly run applies them to
// every stored page, re-parses each page whose text they change, and drops
// the imported source IDs they now match. It's a long job (R91): a run kept
// in the database, each batch of pages done in one transaction with the
// run's place, so each page's new text and the fields parsed from it are
// written together, or neither is. A crash, restart or upgrade carries it
// on from its last batch, and an admin can pause, carry on, stop or start it
// again. Counts and codes only (P1-P3): never a page's text, a rule's terms,
// a capcode, an address or a locality.
package redaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/longjob"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

const (
	channel   = "pagerarchive_redaction"
	hashKey   = "redaction_hash" // the rules last applied to every stored page
	batchSize = 1000
)

// The reasons a run starts, as codes (R23b's way): the rules changed since
// they were last applied to every page, or an admin started it again.
const (
	ReasonRulesChanged = "rules_changed"
	ReasonStartedAgain = "started_again"
)

// The refusals' codes.
const (
	CodeNotRunning = "redaction_not_running"
	CodeNotPaused  = "redaction_not_paused"
	CodeStopped    = "redaction_stopped"
)

// Worker does retroactive redaction's runs: in the nightly run (Nightly),
// and in the background after a restart, a resume or Start again (Run).
type Worker struct {
	Store   *store.Store
	Network *profile.Network
	// Reparse re-parses each page whose text changes, in the batch's
	// transaction, and rebuilds the large events around them (R87f).
	Reparse *reparse.Worker
	Log     *slog.Logger
	Poll    time.Duration
	// Tracer traces each run that has work (R72); nil traces nothing.
	Tracer trace.Tracer
	// Progress is how far the run has got, and its state, for /metrics and
	// the admin's status.
	Progress longjob.Progress
	// ProgressEvery is how often a run logs its progress: 30 s if 0.
	ProgressEvery time.Duration

	mu sync.Mutex // one walk at a time
}

// RulesHash identifies the network's redaction rules.
func RulesHash(n *profile.Network) string {
	h := sha256.New()
	for _, r := range n.Redactions {
		fmt.Fprintf(h, "%s\x00%s\x00%d\n", r.Name, r.Re.String(), r.Group)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Status is retroactive redaction as an admin sees it: its state, how far
// the run (or, while idle, the last one) has got, its rate and time left
// while it runs, and whether the rules in use are still to be applied to
// every page. Counts and codes only (P1-P3).
type Status struct {
	State   longjob.State `json:"state"`
	Total   int64         `json:"total"`
	Done    int64         `json:"done"`
	Percent int64         `json:"percent"`
	// Redacted is the pages whose text the run changed so far.
	Redacted int64 `json:"redacted"`
	// PerMinute and ETASeconds are while it runs: since it started, or since
	// this process carried it on. ETASeconds is null until some are done.
	PerMinute  int64      `json:"per_minute"`
	ETASeconds *int64     `json:"eta_seconds"`
	StartedAt  *time.Time `json:"started_at"`
	ChangedAt  *time.Time `json:"changed_at"`
	FinishedAt *time.Time `json:"finished_at"`
	// Reason is why the run started: rules_changed or started_again.
	Reason string `json:"reason"`
	// Pending is true while the rules in use aren't yet applied to every
	// stored page: a run is under way, paused or stopped, or the next
	// nightly run starts one.
	Pending bool `json:"pending"`
}

// GetStatus is the status now: the run in the database, with this worker's
// own count of the batch in hand while it runs.
func (w *Worker) GetStatus(ctx context.Context) (Status, error) {
	out := Status{State: longjob.Idle}
	applied, err := w.Store.GetSetting(ctx, hashKey)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	out.Pending = applied != RulesHash(w.Network)
	r, err := w.Store.GetRedactionRun(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	} else if err != nil {
		return out, err
	}
	out.State, out.Total, out.Done, out.Redacted, out.Reason = longjob.StateOf(r.State), r.Total, r.Done, r.Redacted, r.Reason
	out.StartedAt, out.ChangedAt, out.FinishedAt = &r.StartedAt, &r.ChangedAt, r.FinishedAt
	if out.State != longjob.Idle {
		out.Pending = true
	}
	if s := w.Progress.Snapshot(); s.Run == r.ID && out.State == longjob.Running && s.Running {
		out.Done = max(out.Done, s.Done)
		perMin, eta := s.Rate(time.Now())
		out.PerMinute = int64(perMin)
		if eta >= 0 {
			out.ETASeconds = &eta
		}
	}
	out.Percent = longjob.Snapshot{Total: out.Total, Done: out.Done}.Percent()
	return out, nil
}

// Pause pauses the running run: it stops after the page in hand, and
// carries on from there when resumed, even after a restart. The nightly run
// leaves a paused run paused.
func (w *Worker) Pause(ctx context.Context) (Status, error) {
	return w.change(ctx, func(q *db.Queries, r *db.RedactionRun) (longjob.State, error) {
		if r == nil || longjob.StateOf(r.State) != longjob.Running {
			return "", &longjob.Refusal{Code: CodeNotRunning, Message: "No retroactive redaction is running, so there's nothing to pause."}
		}
		return longjob.Paused, q.SetRedactionRunState(ctx, db.SetRedactionRunStateParams{State: string(longjob.Paused), ID: r.ID})
	})
}

// Resume carries on a paused run from where it got to.
func (w *Worker) Resume(ctx context.Context) (Status, error) {
	return w.change(ctx, func(q *db.Queries, r *db.RedactionRun) (longjob.State, error) {
		if r != nil {
			switch longjob.StateOf(r.State) {
			case longjob.Paused:
				return longjob.Running, q.SetRedactionRunState(ctx, db.SetRedactionRunStateParams{State: string(longjob.Running), ID: r.ID})
			case longjob.Stopped:
				return "", &longjob.Refusal{Code: CodeStopped, Message: "Retroactive redaction was stopped, so it can't carry on: Start again runs it from the beginning, and so does the next nightly run."}
			}
		}
		return "", &longjob.Refusal{Code: CodeNotPaused, Message: "Retroactive redaction isn't paused."}
	})
}

// Stop cancels the running or paused run. The pages it has done stay done
// (each page's text and parsed fields together), the large events around
// the pages it re-parsed are rebuilt (R87f), and the rest wait: the next
// nightly run starts it again from the beginning, as the rules aren't yet
// applied to every page (P1), or an admin's Start again does.
func (w *Worker) Stop(ctx context.Context) (Status, error) {
	return w.change(ctx, func(q *db.Queries, r *db.RedactionRun) (longjob.State, error) {
		if r != nil {
			switch longjob.StateOf(r.State) {
			case longjob.Running, longjob.Paused:
				return longjob.Stopped, q.SetRedactionRunState(ctx, db.SetRedactionRunStateParams{State: string(longjob.Stopped), ID: r.ID})
			case longjob.Stopped:
				return "", &longjob.Refusal{Code: CodeNotRunning, Message: "Retroactive redaction is already stopped."}
			}
		}
		return "", &longjob.Refusal{Code: CodeNotRunning, Message: "No retroactive redaction is running or paused, so there's nothing to stop."}
	})
}

// StartAgain runs retroactive redaction from the beginning with the rules
// in use, whatever its state: every stored page is checked again. With no
// run yet, it starts the first.
func (w *Worker) StartAgain(ctx context.Context) (Status, error) {
	return w.change(ctx, func(q *db.Queries, r *db.RedactionRun) (longjob.State, error) {
		_, err := w.begin(ctx, q, r, longjob.Running, ReasonStartedAgain)
		return longjob.Running, err
	})
}

// change is an admin's change of state, in one transaction with the run
// locked, so it waits for a batch ending at that moment. The worker is told
// by a notification and by its progress. r is nil when there's no run yet.
func (w *Worker) change(ctx context.Context, fn func(*db.Queries, *db.RedactionRun) (longjob.State, error)) (Status, error) {
	var to longjob.State
	err := w.Store.InTx(ctx, func(q *db.Queries) error {
		var rp *db.RedactionRun
		r, err := q.LockRedactionRun(ctx)
		if err == nil {
			rp = &r
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if to, err = fn(q, rp); err != nil {
			return err
		}
		return q.NotifyRedaction(ctx)
	})
	if err != nil {
		return Status{}, err
	}
	w.Progress.Interrupt(to)
	return w.GetStatus(ctx)
}

// begin puts run r (nil: none yet, or a new one in place of a finished
// one) at its beginning in state, with the rules in use: every page from
// the first again, its total counted again, its done 0 and its days to
// rebuild gone. Its epoch goes up, so a batch finishing meanwhile doesn't
// write its place over the new start. q holds r locked.
func (w *Worker) begin(ctx context.Context, q *db.Queries, r *db.RedactionRun, state longjob.State, reason string) (db.RedactionRun, error) {
	total, err := q.CountMessagesToRedact(ctx)
	if err != nil {
		return db.RedactionRun{}, err
	}
	hash := RulesHash(w.Network)
	if r == nil {
		if err := q.DeleteRedactionRuns(ctx); err != nil {
			return db.RedactionRun{}, err
		}
		if _, err := q.CreateRedactionRun(ctx, db.CreateRedactionRunParams{Reason: reason, RulesHash: hash, Total: total}); err != nil {
			return db.RedactionRun{}, err
		}
		if state != longjob.Running {
			cur, err := q.GetRedactionRun(ctx)
			if err != nil {
				return cur, err
			}
			if err := q.SetRedactionRunState(ctx, db.SetRedactionRunStateParams{State: string(state), ID: cur.ID}); err != nil {
				return cur, err
			}
		}
		return q.GetRedactionRun(ctx)
	}
	if err := q.DeleteRedactionRunDays(ctx, r.ID); err != nil {
		return db.RedactionRun{}, err
	}
	if err := q.RestartRedactionRun(ctx, db.RestartRedactionRunParams{State: string(state), Reason: reason, RulesHash: hash, Total: total, ID: r.ID}); err != nil {
		return db.RedactionRun{}, err
	}
	return q.GetRedactionRun(ctx)
}

// CheckRules is the start-up check: a run that hadn't finished (running,
// paused or stopped) under rules that aren't the ones in use now starts
// again from the beginning with them, in the state it was in, so an
// admin's pause or stop holds. A new run for changed rules waits for the
// nightly run (P1).
func (w *Worker) CheckRules(ctx context.Context) error {
	var again *db.RedactionRun
	err := w.Store.InTx(ctx, func(q *db.Queries) error {
		r, err := q.LockRedactionRun(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if r.State == longjob.Finished || r.RulesHash == RulesHash(w.Network) {
			return nil
		}
		cur, err := w.begin(ctx, q, &r, longjob.StateOf(r.State), ReasonRulesChanged)
		again = &cur
		return err
	})
	if err == nil && again != nil {
		w.Log.InfoContext(ctx, "retroactive redaction starts again from the beginning: the rules changed", "state", again.State, "reasons", again.Reason, "total", again.Total)
	}
	return err
}

// Nightly is the nightly run's part (P1): when the rules in use haven't
// been applied to every stored page, it starts a run (a stopped one starts
// again from the beginning; one under other rules too, in its state), then
// does the running run until it finishes or an admin pauses or stops it. A
// paused run stays paused. It returns how many pages' text it changed.
func (w *Worker) Nightly(ctx context.Context) (int64, error) {
	var started *db.RedactionRun
	hash := RulesHash(w.Network)
	err := w.Store.InTx(ctx, func(q *db.Queries) error {
		applied, err := q.GetSetting(ctx, hashKey)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		r, err := q.LockRedactionRun(ctx)
		var rp *db.RedactionRun
		state, reason := longjob.Running, ReasonRulesChanged
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if applied == hash {
				return nil
			}
		case err != nil:
			return err
		case r.State == longjob.Finished:
			if applied == hash {
				return nil
			}
		case r.RulesHash != hash:
			rp, state = &r, longjob.StateOf(r.State)
			if state == longjob.Stopped {
				state = longjob.Running
			}
		case longjob.StateOf(r.State) == longjob.Stopped:
			rp, reason = &r, r.Reason
		default:
			return nil // running (carried on below) or paused (held)
		}
		cur, err := w.begin(ctx, q, rp, state, reason)
		started = &cur
		return err
	})
	if err != nil {
		return 0, err
	}
	if started != nil && started.Done == 0 && started.Epoch > 0 {
		w.Log.InfoContext(ctx, "retroactive redaction starts again from the beginning", "state", started.State, "reasons", started.Reason, "total", started.Total)
	}
	n, _, err := w.RunOnce(ctx)
	if err == nil {
		if s := w.Progress.Snapshot(); s.State == longjob.Paused {
			w.Log.InfoContext(ctx, "retroactive redaction is paused: the nightly run leaves it", "state", string(s.State), "done", s.Done, "total", s.Total)
		}
	}
	return n, err
}

// Run carries on a running run in the background until ctx ends: after a
// restart, a resume or Start again. It wakes on a notification and polls.
// A run it finishes is followed by a vacuum, as the nightly run's is (P1).
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := w.listen(ctx); err != nil && ctx.Err() == nil {
			w.Log.Error("retroactive redaction worker", "error", store.SafeError(err))
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// listen waits for notifications on a connection of its own, outside the
// pool: the pool is small (4 connections on a small host), and the re-parse's
// worker already keeps one of its connections listening.
func (w *Worker) listen(ctx context.Context) error {
	conn, err := pgx.ConnectConfig(ctx, w.Store.Pool.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return err
	}
	for {
		_, finished, err := w.RunOnce(ctx)
		if err != nil {
			return err
		}
		if finished {
			if err := w.vacuum(ctx); err != nil {
				return err
			}
		}
		wait, cancel := context.WithTimeout(ctx, w.Poll)
		_, err = conn.WaitForNotification(wait)
		cancel()
		if err != nil && ctx.Err() != nil {
			return nil
		}
		if err != nil && wait.Err() == nil {
			return err // the connection failed, not the poll timeout
		}
	}
}

// vacuum, so replaced text doesn't linger in table pages (P1).
func (w *Worker) vacuum(ctx context.Context) error {
	for _, t := range []string{"messages", "receptions"} {
		if _, err := w.Store.Pool.Exec(ctx, "VACUUM (ANALYZE) "+t); err != nil {
			return fmt.Errorf("vacuum %s: %s", t, store.SafeError(err))
		}
	}
	return nil
}

// RunOnce does the running run, from its last batch, until it finishes or
// an admin pauses or stops it. A paused or stopped run does nothing more (a
// stopped one first rebuilds the large events around the pages it did).
// It returns how many pages' text it changed, and whether this call
// finished the run.
func (w *Worker) RunOnce(ctx context.Context) (redacted int64, finished bool, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		r, err := w.Store.GetRedactionRun(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return redacted, false, nil
		} else if err != nil {
			return redacted, false, err
		}
		st := longjob.StateOf(r.State)
		if st != longjob.Running {
			if st == longjob.Stopped {
				if err := w.rebuildEvents(ctx, r.ID); err != nil {
					return redacted, false, err
				}
			}
			if st != longjob.Idle || w.Progress.Snapshot().Run == 0 {
				w.Progress.Hold(st, r.ID, r.Total, r.Done, r.StartedAt)
			}
			return redacted, false, nil
		}
		k, done, again, err := w.walk(ctx, r)
		redacted += k
		if err != nil || !again {
			return redacted, done, err
		}
	}
}

// walk does run r from where it got to, a batch at a time, and finishes it
// once every page and source ID is checked. again is true when it stopped
// because the run changed (paused, stopped or started again).
func (w *Worker) walk(ctx context.Context, r db.RedactionRun) (n int64, finished, again bool, err error) {
	ctx, span := telemetry.Or(w.Tracer).Start(ctx, "redaction")
	defer span.End()
	defer func() { span.SetAttributes(attribute.Int64("pagerarchive.redaction.pages", n)) }()
	cands, err := w.Reparse.Candidates(ctx)
	if err != nil {
		return 0, false, false, err
	}
	w.Progress.Begin(r.ID, r.Total, r.Done, r.StartedAt, time.Now())
	if r.Done == 0 && r.AfterID == 0 {
		w.Log.InfoContext(ctx, "retroactive redaction started", "reasons", r.Reason, "total", r.Total)
	} else {
		w.Log.InfoContext(ctx, "retroactive redaction carried on", "state", string(longjob.Running), "reasons", r.Reason, "done", r.Done, "total", r.Total)
	}
	stopLog := longjob.LogProgress(ctx, w.ProgressEvery, &w.Progress, func(ctx context.Context, args ...any) {
		w.Log.InfoContext(ctx, "retroactive redaction progress", args...)
	})
	defer stopLog()
	after := r.AfterID
	for !r.PagesDone {
		if w.Progress.Interrupted() {
			return n, false, true, nil
		}
		b, err := w.batch(ctx, r, cands, after)
		n += b.redacted
		if err != nil {
			return n, false, false, err
		}
		if b.changed || b.cut {
			return n, false, true, nil
		}
		after = b.last
		if b.end {
			break
		}
	}
	src := r.SourceAfter
	for !r.SourcesDone {
		if w.Progress.Interrupted() {
			return n, false, true, nil
		}
		b, err := w.sources(ctx, r, src)
		if err != nil {
			return n, false, false, err
		}
		if b.changed || b.cut {
			return n, false, true, nil
		}
		if b.end {
			break
		}
		src = b.lastSource
	}
	stopLog()
	if err := w.rebuildEvents(ctx, r.ID); err != nil {
		return n, false, false, err
	}
	// Every page is checked: the rules are applied, saved as such in the
	// same transaction as the run's finish.
	changed := false
	var cur db.RedactionRun
	err = w.Store.InTx(ctx, func(q *db.Queries) error {
		c, err := q.LockRedactionRun(ctx)
		if err != nil {
			return err
		}
		cur = c
		if c.ID != r.ID || c.Epoch != r.Epoch || longjob.StateOf(c.State) != longjob.Running {
			changed = true
			return nil
		}
		if err := q.SetSetting(ctx, db.SetSettingParams{Key: hashKey, Value: r.RulesHash}); err != nil {
			return err
		}
		return q.FinishRedactionRun(ctx, r.ID)
	})
	if err != nil || changed {
		return n, false, changed, err
	}
	w.Progress.Finish()
	perMin, _ := w.Progress.Snapshot().Rate(time.Now())
	w.Log.InfoContext(ctx, "retroactive redaction finished", "count", max(cur.Done, cur.Total), "redacted", cur.Redacted,
		"seconds", int64(time.Since(r.StartedAt).Seconds()), "per_minute", int64(perMin))
	return n, true, false, nil
}

// batchResult is how a batch went: checked pages and the last one's ID,
// redacted of them changed; changed when the run was started again
// meanwhile (the pages are done, its place isn't written), cut when an
// admin's change stopped it part way, or it was paused or stopped
// meanwhile (the pages done so far are kept, with their place), end when it
// was the last.
type batchResult struct {
	checked, redacted int64
	last              int64
	lastSource        string
	changed, cut, end bool
}

// batch checks a batch of pages after after, and for each page the rules
// change writes its new text and dedup hash and re-parses it, then writes
// the run's place, counts and the days to rebuild large events around, all
// in one transaction: each page is done or not, never its text without its
// parsed fields, and what the run says it has done, it has.
func (w *Worker) batch(ctx context.Context, r db.RedactionRun, cands []ingest.Candidate, after int64) (batchResult, error) {
	var b batchResult
	err := w.Store.InTx(ctx, func(q *db.Queries) error {
		b = batchResult{last: after}
		rows, err := q.MessagesForRedaction(ctx, db.MessagesForRedactionParams{AfterID: after, Batch: batchSize})
		if err != nil {
			return err
		}
		var days events.Affected
		for _, m := range rows {
			if w.Progress.Interrupted() {
				b.cut = true
				break
			}
			if pipeline.Redact(w.Network, m.Text) != m.Text {
				ok, err := w.redact(ctx, q, cands, m.ID, &days)
				if err != nil {
					return err
				}
				if ok {
					b.redacted++
				}
			}
			b.checked++
			b.last = m.ID
			w.Progress.Add()
		}
		b.end = len(rows) < batchSize && !b.cut
		cur, err := q.LockRedactionRun(ctx)
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
			if err := q.AddRedactionRunDay(ctx, db.AddRedactionRunDayParams{RunID: r.ID, Day: s.From.UTC().Unix() / 86400, FromAt: s.From, ToAt: s.To}); err != nil {
				return err
			}
		}
		if err := q.AdvanceRedactionRun(ctx, db.AdvanceRedactionRunParams{AfterID: b.last, Checked: b.checked, Changed: b.redacted, PagesDone: b.end, ID: r.ID}); err != nil {
			return err
		}
		if longjob.StateOf(cur.State) != longjob.Running {
			b.cut = true
		}
		return nil
	})
	if err != nil {
		w.Progress.Undo(int(b.checked))
		b.checked, b.redacted = 0, 0
	}
	return b, err
}

// redact writes page id's new text and re-parses it, in q's transaction,
// with the page locked until it commits. ok is false when the page has
// gone, or the rules no longer change it.
func (w *Worker) redact(ctx context.Context, q *db.Queries, cands []ingest.Candidate, id int64, days *events.Affected) (ok bool, err error) {
	rows, err := q.MessagesToRedactLocked(ctx, []int64{id})
	if err != nil || len(rows) == 0 {
		return false, err
	}
	m := rows[0]
	text := pipeline.Redact(w.Network, m.Text)
	if text == m.Text {
		return false, nil
	}
	key := pipeline.DedupKey(pipeline.Prepared{Capcode: m.Capcode, Text: text})
	if err := q.UpdateRedactedText(ctx, db.UpdateRedactedTextParams{ID: m.ID, Text: text, DedupKey: key}); err != nil {
		return false, err
	}
	m.Text = text
	return true, w.Reparse.ReparseInTx(ctx, q, cands, db.MessagesToReparseRow(m), days)
}

// sources checks a batch of the imported source IDs after after, and drops
// those the rules now match (ADR 0018: the copy stays, known by its
// message's dedup key and receive time), with the run's place, in one
// transaction.
func (w *Worker) sources(ctx context.Context, r db.RedactionRun, after string) (batchResult, error) {
	var b batchResult
	err := w.Store.InTx(ctx, func(q *db.Queries) error {
		b = batchResult{lastSource: after}
		ids, err := q.ImportSourceIDsAfter(ctx, db.ImportSourceIDsAfterParams{After: &after, Batch: batchSize})
		if err != nil {
			return err
		}
		for _, id := range ids {
			b.lastSource = id
			if pipeline.Redact(w.Network, id) == id {
				continue
			}
			if err := q.ForgetImportSourceID(ctx, id); err != nil {
				return err
			}
		}
		b.end = len(ids) < batchSize
		cur, err := q.LockRedactionRun(ctx)
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
		if err := q.AdvanceRedactionRunSources(ctx, db.AdvanceRedactionRunSourcesParams{SourceAfter: b.lastSource, SourcesDone: b.end, ID: r.ID}); err != nil {
			return err
		}
		if longjob.StateOf(cur.State) != longjob.Running {
			b.cut = true
		}
		return nil
	})
	return b, err
}

// rebuildEvents rebuilds the large events around the pages run id
// re-parsed (R87f), from the days its batches kept, then forgets the days:
// done again after a crash part way, it gives the same events.
func (w *Worker) rebuildEvents(ctx context.Context, id int64) error {
	days, err := w.Store.RedactionRunDays(ctx, id)
	if err != nil || len(days) == 0 {
		return err
	}
	var aff events.Affected
	for _, d := range days {
		aff.Add(d.FromAt)
		aff.Add(d.ToAt)
	}
	if err := w.Reparse.RebuildEvents(ctx, &aff); err != nil {
		return err
	}
	return w.Store.DeleteRedactionRunDays(ctx, id)
}
