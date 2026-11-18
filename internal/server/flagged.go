package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// FlagCountsEvery is how often the flagged counts are counted again (R52):
// the most out of date /metrics' counts are while counting works.
const FlagCountsEvery = time.Minute

// FlagCounter counts the stored messages with each admin flag.
type FlagCounter interface {
	CountFlagged(ctx context.Context) (db.CountFlaggedRow, error)
}

// FlagCounts keeps the stored messages' admin flag counts for /metrics
// (R52). Counting them reads the whole messages table's flagged rows, whose
// cost grows with the archive (and far more after a mass rewrite, until the
// table is vacuumed), so a scrape never counts: Run counts at once and then
// every Every, and a scrape shows the last counts and when they were
// counted. A count that fails is logged as a WARN, with counts only, and
// the last counts are kept.
type FlagCounts struct {
	Store FlagCounter
	Log   *slog.Logger
	// Every is how often it counts: FlagCountsEvery if 0. A count that
	// takes longer than Every is given up and fails.
	Every time.Duration
	Now   func() time.Time

	mu     sync.Mutex
	counts db.CountFlaggedRow
	at     time.Time
}

// Run counts at once, then every Every, until ctx ends.
func (f *FlagCounts) Run(ctx context.Context) {
	every := f.Every
	if every <= 0 {
		every = FlagCountsEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		f.count(ctx, every)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (f *FlagCounts) count(ctx context.Context, timeout time.Duration) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := f.Store.CountFlagged(cctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		f.mu.Lock()
		at := f.at
		f.mu.Unlock()
		args := []any{"error", store.SafeError(err)}
		if !at.IsZero() {
			args = append(args, "counted_at", at.UTC().Format(time.RFC3339))
		}
		f.Log.Warn("metrics: the flagged messages couldn't be counted; the last counts are kept", args...)
		return
	}
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	f.mu.Lock()
	f.counts, f.at = c, now()
	f.mu.Unlock()
}

// Last is the last counts and when they were counted; ok is false until
// the first count has worked.
func (f *FlagCounts) Last() (c db.CountFlaggedRow, at time.Time, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts, f.at, !f.at.IsZero()
}
