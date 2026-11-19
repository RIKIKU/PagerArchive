package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/store"
)

// ExtensionChecksEvery is how often the database's extensions are checked
// again for an update the image provides (R84c): a db container can be
// recreated with a newer image while the API keeps running.
const ExtensionChecksEvery = time.Hour

// ExtensionChecker says, for each watched extension, whether the image
// provides a newer version than the database has registered.
type ExtensionChecker interface {
	ExtensionUpdatesPending(ctx context.Context) (map[string]bool, error)
}

// ExtensionChecks keeps the extensions' pending updates for /metrics
// (pagerarchive_db_extension_update_pending, R84c): Run checks at once, as
// the API starts, then every Every, and a scrape shows the last check. A
// check that fails is logged as a WARN, and the last result is kept.
type ExtensionChecks struct {
	Store ExtensionChecker
	Log   *slog.Logger
	// Every is how often it checks: ExtensionChecksEvery if 0.
	Every time.Duration

	mu      sync.Mutex
	pending map[string]bool
}

// Run checks at once, then every Every, until ctx ends.
func (c *ExtensionChecks) Run(ctx context.Context) {
	every := c.Every
	if every <= 0 {
		every = ExtensionChecksEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		c.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *ExtensionChecks) check(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p, err := c.Store.ExtensionUpdatesPending(cctx)
	if err != nil {
		if ctx.Err() == nil {
			c.Log.Warn("metrics: the database's extension versions couldn't be checked; the last check is kept", "error", store.SafeError(err))
		}
		return
	}
	c.mu.Lock()
	c.pending = p
	c.mu.Unlock()
}

// Last is the last check: each watched extension that's installed, and
// whether an update is pending. ok is false until the first check has worked.
func (c *ExtensionChecks) Last() (pending map[string]bool, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending, c.pending != nil
}
