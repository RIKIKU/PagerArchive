package reparse

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// State is where the re-parse is (R23c): idle (no run, or the last one
// finished), running, paused or stopped. It's kept in the database with the
// run, so it holds across a restart.
type State string

const (
	Idle    State = "idle"
	Running State = "running"
	Paused  State = "paused"
	Stopped State = "stopped"
)

// finished is a run's state in the database once it's done: idle.
const finished = "finished"

// Code is the state as /metrics has it: 0 idle, 1 running, 2 paused, 3
// stopped.
func (s State) Code() int {
	switch s {
	case Running:
		return 1
	case Paused:
		return 2
	case Stopped:
		return 3
	}
	return 0
}

// stateOf is a run's state in the database as a State.
func stateOf(dbState string) State {
	if dbState == finished || dbState == "" {
		return Idle
	}
	return State(dbState)
}

// Progress is how far the re-parse has got (R23b) and its state (R23c), for
// /metrics, the admin's status and the progress log lines. It holds counts
// only: never a capcode pattern, a page or its text. The zero value is idle.
// The worker keeps it; an admin's change of state sets it too, and asks the
// worker to stop after the page in hand (Interrupt).
type Progress struct {
	mu      sync.Mutex
	state   State
	run     int64 // the run's ID in the database; 0 before any
	total   int64 // the run's messages, counted when it started (the last run's while idle)
	done    int64 // re-parsed so far in the run
	started time.Time
	// The rate is from since, when this process began or carried on the
	// run, and sinceDone, what it had done then: a run carried on after a
	// restart or a pause has its rate from then, not from its start.
	since     time.Time
	sinceDone int64
	reparsed  uint64 // every run's, since the API started
	interrupt bool
}

// ProgressSnapshot is Progress at one moment.
type ProgressSnapshot struct {
	State State
	// Running is State == Running: until the run has rebuilt the large
	// events after it (R87f).
	Running bool
	Run     int64
	// Total and Done are the run's, or the last run's while idle; both 0
	// before the first.
	Total, Done int64
	Started     time.Time // zero before the first run
	// Since and SinceDone are where the rate is measured from.
	Since     time.Time
	SinceDone int64
	// Reparsed counts every run's messages since the API started.
	Reparsed uint64
}

// Snapshot is the progress now. A nil Progress is idle.
func (p *Progress) Snapshot() ProgressSnapshot {
	if p == nil {
		return ProgressSnapshot{State: Idle}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state
	if st == "" {
		st = Idle
	}
	return ProgressSnapshot{State: st, Running: st == Running, Run: p.run, Total: p.total, Done: p.done, Started: p.started,
		Since: p.since, SinceDone: p.sinceDone, Reparsed: p.reparsed}
}

// begin is a run starting, or carrying on from done, now.
func (p *Progress) begin(run, total, done int64, started, now time.Time) {
	p.mu.Lock()
	p.state, p.run, p.total, p.done, p.started = Running, run, total, done, started
	p.since, p.sinceDone, p.interrupt = now, done, false
	p.mu.Unlock()
}

// hold is a run that's paused or stopped, or (idle) the last one, finished.
func (p *Progress) hold(state State, run, total, done int64, started time.Time) {
	p.mu.Lock()
	p.state, p.run, p.total, p.done, p.started = state, run, total, done, started
	p.since, p.sinceDone = time.Time{}, done
	p.mu.Unlock()
}

func (p *Progress) add() {
	p.mu.Lock()
	p.done++
	p.reparsed++
	p.mu.Unlock()
}

// undo takes back n pages of a batch that was rolled back: they're done
// again. The counter of work done keeps them.
func (p *Progress) undo(n int) {
	p.mu.Lock()
	p.done -= int64(n)
	p.mu.Unlock()
}

// finish ends the run. Pages stored while it ran are re-parsed too, so it
// can do a few more than it counted: the total is then what it did.
func (p *Progress) finish() {
	p.mu.Lock()
	p.state = Idle
	if p.done > p.total {
		p.total = p.done
	}
	p.mu.Unlock()
}

// Interrupt is an admin's change of state (R23c): the progress shows it at
// once, and the worker stops after the page in hand, finishes that batch
// and reads the run again. A nil Progress does nothing.
func (p *Progress) Interrupt(state State) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.state, p.interrupt = state, true
	if state != Running {
		p.since = time.Time{}
	}
	p.mu.Unlock()
}

func (p *Progress) interrupted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interrupt
}

// Rate is messages re-parsed a minute since the run started, or since this
// process carried it on, and the seconds left at that rate (-1 while
// nothing is done yet).
func (s ProgressSnapshot) Rate(now time.Time) (perMinute float64, etaSeconds int64) {
	from, fromDone := s.Since, s.SinceDone
	if from.IsZero() {
		from, fromDone = s.Started, 0
	}
	elapsed := now.Sub(from).Minutes()
	if s.Done-fromDone <= 0 || elapsed <= 0 {
		return 0, -1
	}
	perMinute = float64(s.Done-fromDone) / elapsed
	left := max(s.Total-s.Done, 0)
	return perMinute, int64(float64(left) / perMinute * 60)
}

// Percent is how much of the run is done, 0 to 100.
func (s ProgressSnapshot) Percent() int64 {
	if s.Total <= 0 {
		return 100
	}
	return min(100, s.Done*100/s.Total)
}

// logProgress logs a progress line every w.ProgressEvery (30 s by default)
// until the returned function is called. It runs beside the re-parse, so a
// re-parse that's stuck still says so: done stops moving.
func (w *Worker) logProgress(ctx context.Context) func() {
	every := w.ProgressEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case now := <-t.C:
				s := w.Progress.Snapshot()
				perMin, eta := s.Rate(now)
				args := []any{"done", s.Done, "total", s.Total, "percent", s.Percent(), "per_minute", int64(perMin)}
				if eta >= 0 {
					args = append(args, "eta_seconds", eta)
				}
				w.Log.InfoContext(ctx, "re-parse progress", args...)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop); wg.Wait() })
	}
}

// knownReasons are the reasons the code queues a re-parse with (R23a). Only these
// reach a log line, as codes; any tool can queue a request, with any text.
var knownReasons = []string{
	"profiles changed", "retry", "alias added", "alias capcode changed", "alias template changed",
	"alias ignored changed", "alias deleted", "alias import", "template profile changed",
}

// reasonCodes is the requests' reasons as codes, each once and sorted:
// "profiles changed" is profiles_changed, and a reason that isn't one of
// knownReasons is "other".
func reasonCodes(reasons []string) string {
	var out []string
	for _, r := range reasons {
		c := "other"
		if slices.Contains(knownReasons, r) {
			c = strings.ReplaceAll(r, " ", "_")
		}
		out = append(out, c)
	}
	slices.Sort(out)
	return strings.Join(slices.Compact(out), ",")
}
