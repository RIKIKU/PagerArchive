// Package longjob is what the app's long jobs (SPEC.md R91, ADR 0042) share:
// a state an admin can change (idle, running, paused, stopped), how far a
// run has got for /metrics, the admin's status and the progress log lines,
// and the refusal an admin's change gets when the state doesn't allow it.
// Each job keeps its own run in the database, written in the same
// transaction as each unit of work (the re-parse, ADR 0043; retroactive
// redaction, ADR 0044). Counts and codes only (P1-P3): never a page, its
// text, a capcode, an address or a locality.
package longjob

import (
	"context"
	"sync"
	"time"
)

// State is where a long job is: idle (no run, or the last one finished),
// running, paused or stopped. It's kept in the database with the run, so it
// holds across a restart.
type State string

const (
	Idle    State = "idle"
	Running State = "running"
	Paused  State = "paused"
	Stopped State = "stopped"
)

// Finished is a run's state in the database once it's done: idle.
const Finished = "finished"

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

// StateOf is a run's state in the database as a State.
func StateOf(dbState string) State {
	if dbState == Finished || dbState == "" {
		return Idle
	}
	return State(dbState)
}

// Refusal is an admin's change the job's state doesn't allow, with a code
// and a message for the API.
type Refusal struct {
	Code, Message string
}

func (r *Refusal) Error() string { return r.Message }

// Progress is how far a long job's run has got and its state, for /metrics,
// the admin's status and the progress log lines. It holds counts only. The
// zero value is idle. The job's worker keeps it; an admin's change of state
// sets it too, and asks the worker to stop after the unit in hand
// (Interrupt).
type Progress struct {
	mu      sync.Mutex
	state   State
	run     int64 // the run's ID in the database; 0 before any
	total   int64 // the run's units, counted when it started (the last run's while idle)
	done    int64 // done so far in the run
	started time.Time
	// The rate is from since, when this process began or carried on the
	// run, and sinceDone, what it had done then: a run carried on after a
	// restart or a pause has its rate from then, not from its start.
	since     time.Time
	sinceDone int64
	count     uint64 // every run's units, since the API started
	interrupt bool
}

// Snapshot is Progress at one moment.
type Snapshot struct {
	State State
	// Running is State == Running.
	Running bool
	Run     int64
	// Total and Done are the run's, or the last run's while idle; both 0
	// before the first.
	Total, Done int64
	Started     time.Time // zero before the first run
	// Since and SinceDone are where the rate is measured from.
	Since     time.Time
	SinceDone int64
	// Count is every run's units since the API started.
	Count uint64
}

// Snapshot is the progress now. A nil Progress is idle.
func (p *Progress) Snapshot() Snapshot {
	if p == nil {
		return Snapshot{State: Idle}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state
	if st == "" {
		st = Idle
	}
	return Snapshot{State: st, Running: st == Running, Run: p.run, Total: p.total, Done: p.done, Started: p.started,
		Since: p.since, SinceDone: p.sinceDone, Count: p.count}
}

// Begin is a run starting, or carrying on from done, now.
func (p *Progress) Begin(run, total, done int64, started, now time.Time) {
	p.mu.Lock()
	p.state, p.run, p.total, p.done, p.started = Running, run, total, done, started
	p.since, p.sinceDone, p.interrupt = now, done, false
	p.mu.Unlock()
}

// Hold is a run that's paused or stopped, or (idle) the last one, finished.
func (p *Progress) Hold(state State, run, total, done int64, started time.Time) {
	p.mu.Lock()
	p.state, p.run, p.total, p.done, p.started = state, run, total, done, started
	p.since, p.sinceDone = time.Time{}, done
	p.mu.Unlock()
}

// Add is one unit done.
func (p *Progress) Add() {
	p.mu.Lock()
	p.done++
	p.count++
	p.mu.Unlock()
}

// Undo takes back n units of a batch that was rolled back: they're done
// again. The counter of work done keeps them.
func (p *Progress) Undo(n int) {
	p.mu.Lock()
	p.done -= int64(n)
	p.mu.Unlock()
}

// Finish ends the run. Units added while it ran (pages stored meanwhile)
// can make it do a few more than it counted: the total is then what it did.
func (p *Progress) Finish() {
	p.mu.Lock()
	p.state = Idle
	if p.done > p.total {
		p.total = p.done
	}
	p.mu.Unlock()
}

// Interrupt is an admin's change of state: the progress shows it at once,
// and the worker stops after the unit in hand, finishes that batch and
// reads the run again. A nil Progress does nothing.
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

// Interrupted is whether an admin's change asked the worker to stop.
func (p *Progress) Interrupted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interrupt
}

// Rate is units done a minute since the run started, or since this process
// carried it on, and the seconds left at that rate (-1 while nothing is done
// yet).
func (s Snapshot) Rate(now time.Time) (perMinute float64, etaSeconds int64) {
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
func (s Snapshot) Percent() int64 {
	if s.Total <= 0 {
		return 100
	}
	return min(100, s.Done*100/s.Total)
}

// LogProgress logs p's done, total, percent, rate and time left, with logf
// (whose message is the job's own constant, R73), every every (30 s if 0)
// until the returned function is called. It runs beside the job, so a job
// that's stuck still says so: done stops moving.
func LogProgress(ctx context.Context, every time.Duration, p *Progress, logf func(context.Context, ...any)) func() {
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
				s := p.Snapshot()
				perMin, eta := s.Rate(now)
				args := []any{"done", s.Done, "total", s.Total, "percent", s.Percent(), "per_minute", int64(perMin)}
				if eta >= 0 {
					args = append(args, "eta_seconds", eta)
				}
				logf(ctx, args...)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop); wg.Wait() })
	}
}
