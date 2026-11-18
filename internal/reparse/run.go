package reparse

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// A run (R23c, ADR 0043) is what one re-parse has to do: its capcode
// patterns, each walked by message ID, and how far each has got, kept in
// the database (reparse_runs and the tables beside it) in the same
// transaction as each batch of pages. So a crash, restart or upgrade carries
// on from the last batch done; a batch cut short by one is rolled back, and
// done again, whole. There's at most one run: the latest, kept once it's
// finished so its numbers can still be shown.

// The refusals' codes.
const (
	CodeNotRunning = "reparse_not_running"
	CodeNotPaused  = "reparse_not_paused"
	CodeStopped    = "reparse_stopped"
	CodeNone       = "reparse_none"
)

// Status is the re-parse as an admin sees it (R23c): its state, how far the
// run (or, while idle, the last one) has got, its rate and time left while
// it runs, its reasons as codes, and the requests waiting for the next run.
// Counts and codes only (P1-P3).
type Status struct {
	State   State `json:"state"`
	Total   int64 `json:"total"`
	Done    int64 `json:"done"`
	Percent int64 `json:"percent"`
	// PerMinute and ETASeconds are while it runs: since it started, or since
	// this process carried it on. ETASeconds is null until some are done.
	PerMinute  int64      `json:"per_minute"`
	ETASeconds *int64     `json:"eta_seconds"`
	StartedAt  *time.Time `json:"started_at"`
	ChangedAt  *time.Time `json:"changed_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Reasons    []string   `json:"reasons"`
	Requests   int        `json:"requests"`
	Patterns   int        `json:"patterns"`
	// Waiting is the requests queued for the next run: they wait while a run
	// is running, paused or stopped, and Start again takes them in.
	Waiting int64 `json:"waiting"`
}

// GetStatus is the re-parse's status now: the run in the database, with the
// worker's own count of the batch in hand (p) while it runs.
func GetStatus(ctx context.Context, st *store.Store, p *Progress) (Status, error) {
	out := Status{State: Idle, Reasons: []string{}}
	waiting, err := st.CountReparseRequests(ctx)
	if err != nil {
		return out, err
	}
	out.Waiting = waiting
	r, err := st.GetReparseRun(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	} else if err != nil {
		return out, err
	}
	pats, err := st.ReparseRunPatterns(ctx, r.ID)
	if err != nil {
		return out, err
	}
	out.State, out.Total, out.Done, out.Requests, out.Patterns = stateOf(r.State), r.Total, r.Done, int(r.Requests), len(pats)
	out.StartedAt, out.ChangedAt, out.FinishedAt = &r.StartedAt, &r.ChangedAt, r.FinishedAt
	if r.Reasons != "" {
		out.Reasons = strings.Split(r.Reasons, ",")
	}
	if s := p.Snapshot(); s.Run == r.ID && out.State == Running && s.Running {
		out.Done = max(out.Done, s.Done)
		perMin, eta := s.Rate(time.Now())
		out.PerMinute = int64(perMin)
		if eta >= 0 {
			out.ETASeconds = &eta
		}
	}
	out.Percent = ProgressSnapshot{Total: out.Total, Done: out.Done}.Percent()
	return out, nil
}

// Pause pauses the running run: it stops after the page in hand, and
// carries on from there when resumed, even after a restart.
func Pause(ctx context.Context, st *store.Store, p *Progress) (Status, error) {
	return change(ctx, st, p, func(q *db.Queries, r db.ReparseRun) (State, error) {
		if stateOf(r.State) != Running {
			return "", &Refusal{Code: CodeNotRunning, Message: "No re-parse is running, so there's nothing to pause."}
		}
		return Paused, q.SetReparseRunState(ctx, db.SetReparseRunStateParams{State: string(Paused), ID: r.ID})
	})
}

// Resume carries on a paused run from where it got to.
func Resume(ctx context.Context, st *store.Store, p *Progress) (Status, error) {
	return change(ctx, st, p, func(q *db.Queries, r db.ReparseRun) (State, error) {
		switch stateOf(r.State) {
		case Paused:
			return Running, q.SetReparseRunState(ctx, db.SetReparseRunStateParams{State: string(Running), ID: r.ID})
		case Stopped:
			return "", &Refusal{Code: CodeStopped, Message: "The re-parse was stopped, so it can't carry on: Start again runs it from the beginning."}
		}
		return "", &Refusal{Code: CodeNotPaused, Message: "The re-parse isn't paused."}
	})
}

// Stop cancels the running or paused run. The pages it has done stay done,
// the rest stay as they were parsed before, and the large events around the
// pages done are rebuilt (R87f). It shows as stopped until started again.
func Stop(ctx context.Context, st *store.Store, p *Progress) (Status, error) {
	return change(ctx, st, p, func(q *db.Queries, r db.ReparseRun) (State, error) {
		switch stateOf(r.State) {
		case Running, Paused:
			return Stopped, q.SetReparseRunState(ctx, db.SetReparseRunStateParams{State: string(Stopped), ID: r.ID})
		case Stopped:
			return "", &Refusal{Code: CodeNotRunning, Message: "The re-parse is already stopped."}
		}
		return "", &Refusal{Code: CodeNotRunning, Message: "No re-parse is running or paused, so there's nothing to stop."}
	})
}

// StartAgain runs the run (the last one, while idle) again from the
// beginning, whatever its state, with any requests waiting for the next run.
func StartAgain(ctx context.Context, st *store.Store, p *Progress) (Status, error) {
	return change(ctx, st, p, func(q *db.Queries, r db.ReparseRun) (State, error) {
		_, err := restart(ctx, q, r, Running)
		return Running, err
	})
}

// change is an admin's change of state, in one transaction with the run
// locked, so it waits for a batch ending at that moment. The worker is told
// by a notification and, in this process, by p.
func change(ctx context.Context, st *store.Store, p *Progress, fn func(*db.Queries, db.ReparseRun) (State, error)) (Status, error) {
	var to State
	err := st.InTx(ctx, func(q *db.Queries) error {
		r, err := q.LockReparseRun(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return &Refusal{Code: CodeNone, Message: "There's no re-parse yet: one starts when an alias, template or profile changes."}
		} else if err != nil {
			return err
		}
		if to, err = fn(q, r); err != nil {
			return err
		}
		return q.NotifyReparse(ctx)
	})
	if err != nil {
		return Status{}, err
	}
	p.Interrupt(to)
	return GetStatus(ctx, st, p)
}

// claim starts a run from the queued requests, in one transaction with
// taking them, so a crash can't lose them. ok is false when none wait.
func claim(ctx context.Context, st *store.Store) (r db.ReparseRun, ok bool, err error) {
	err = st.InTx(ctx, func(q *db.Queries) error {
		reqs, err := q.ClaimReparseRequests(ctx)
		if err != nil || len(reqs) == 0 {
			return err
		}
		pats, reasons := map[string]bool{}, make([]string, len(reqs))
		for i, rq := range reqs {
			pats[rq.CapcodePattern] = true
			reasons[i] = rq.Reason
		}
		total, err := countPatterns(ctx, q, onlyEverything(pats))
		if err != nil {
			return err
		}
		if err := q.DeleteReparseRuns(ctx); err != nil {
			return err
		}
		id, err := q.CreateReparseRun(ctx, db.CreateReparseRunParams{Reasons: reasonCodes(reasons), Requests: int32(len(reqs)), Total: total})
		if err != nil {
			return err
		}
		for _, pat := range sortedPatterns(pats) {
			if err := q.AddReparseRunPattern(ctx, db.AddReparseRunPatternParams{RunID: id, CapcodePattern: pat}); err != nil {
				return err
			}
		}
		if r, err = q.GetReparseRun(ctx); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return r, ok, err
}

// restart puts run r back to its beginning in state, taking in the requests
// waiting for the next run: every pattern from the first message again, its
// total counted again, its done 0, and its days to rebuild gone (the pages
// are done again). Its epoch goes up, so a batch finishing meanwhile doesn't
// write its place over the new start. q holds r locked.
func restart(ctx context.Context, q *db.Queries, r db.ReparseRun, state State) (db.ReparseRun, error) {
	reqs, err := q.ClaimReparseRequests(ctx)
	if err != nil {
		return r, err
	}
	old, err := q.ReparseRunPatterns(ctx, r.ID)
	if err != nil {
		return r, err
	}
	pats := map[string]bool{}
	for _, p := range old {
		pats[p.CapcodePattern] = true
	}
	reasons := []string{}
	if r.Reasons != "" {
		reasons = strings.Split(r.Reasons, ",")
	}
	raw := make([]string, len(reqs))
	for i, rq := range reqs {
		pats[rq.CapcodePattern] = true
		raw[i] = rq.Reason
	}
	if len(raw) > 0 {
		reasons = append(reasons, strings.Split(reasonCodes(raw), ",")...)
	}
	slices.Sort(reasons)
	pats = onlyEverything(pats)
	total, err := countPatterns(ctx, q, pats)
	if err != nil {
		return r, err
	}
	if err := q.DeleteReparseRunPatterns(ctx, r.ID); err != nil {
		return r, err
	}
	for _, pat := range sortedPatterns(pats) {
		if err := q.AddReparseRunPattern(ctx, db.AddReparseRunPatternParams{RunID: r.ID, CapcodePattern: pat}); err != nil {
			return r, err
		}
	}
	if err := q.DeleteReparseRunDays(ctx, r.ID); err != nil {
		return r, err
	}
	if err := q.RestartReparseRun(ctx, db.RestartReparseRunParams{State: string(state), Reasons: strings.Join(slices.Compact(reasons), ","),
		Requests: r.Requests + int32(len(reqs)), Total: total, ID: r.ID}); err != nil {
		return r, err
	}
	return q.GetReparseRun(ctx)
}

// onlyEverything is pats, or just "%" when that's one of them: every
// message once, not again for each narrower pattern.
func onlyEverything(pats map[string]bool) map[string]bool {
	if pats[everything] {
		return map[string]bool{everything: true}
	}
	return pats
}

func sortedPatterns(pats map[string]bool) []string {
	out := make([]string, 0, len(pats))
	for p := range pats {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// countPatterns is R23b's total: each pattern's messages, counted with the
// condition the walk uses (overlapping patterns are walked, and counted,
// once each).
func countPatterns(ctx context.Context, q *db.Queries, pats map[string]bool) (int64, error) {
	var total int64
	for pat := range pats {
		c, err := q.CountMessagesToReparse(ctx, pat)
		if err != nil {
			return 0, err
		}
		total += c
	}
	return total, nil
}
