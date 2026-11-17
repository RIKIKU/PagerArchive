package reparse

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// Progress is how far the re-parse has got (R23b), for /metrics and the
// progress log lines. It holds counts only: never a capcode pattern, a page
// or its text. The zero value is idle.
type Progress struct {
	mu       sync.Mutex
	running  bool
	total    int64 // this run's messages, counted when it started (the last run's while idle)
	done     int64 // re-parsed so far in this run
	started  time.Time
	reparsed uint64 // every run's, since the API started
}

// ProgressSnapshot is Progress at one moment.
type ProgressSnapshot struct {
	Running bool
	// Total and Done are the run's, or the last run's while idle; both 0
	// before the first.
	Total, Done int64
	Started     time.Time // zero before the first run
	// Reparsed counts every run's messages since the API started.
	Reparsed uint64
}

// Snapshot is the progress now. A nil Progress is idle.
func (p *Progress) Snapshot() ProgressSnapshot {
	if p == nil {
		return ProgressSnapshot{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return ProgressSnapshot{Running: p.running, Total: p.total, Done: p.done, Started: p.started, Reparsed: p.reparsed}
}

func (p *Progress) start(total int64, at time.Time) {
	p.mu.Lock()
	p.running, p.total, p.done, p.started = true, total, 0, at
	p.mu.Unlock()
}

func (p *Progress) add() {
	p.mu.Lock()
	p.done++
	p.reparsed++
	p.mu.Unlock()
}

// finish ends the run. Pages stored while it ran are re-parsed too, so it
// can do a few more than it counted: the total is then what it did.
func (p *Progress) finish() {
	p.mu.Lock()
	p.running = false
	if p.done > p.total {
		p.total = p.done
	}
	p.mu.Unlock()
}

// Rate is messages re-parsed a minute since the run started, and the
// seconds left at that rate (-1 while nothing is done yet).
func (s ProgressSnapshot) Rate(now time.Time) (perMinute float64, etaSeconds int64) {
	elapsed := now.Sub(s.Started).Minutes()
	if s.Done == 0 || elapsed <= 0 {
		return 0, -1
	}
	perMinute = float64(s.Done) / elapsed
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
