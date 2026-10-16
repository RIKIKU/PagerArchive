package authentik

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Locks counts accounts locked by the guessing protection (R44a) for the
// metrics (R52). It connects to Authentik's database as a role that can
// only call pagerarchive_locked_accounts(), a function made by
// deploy/postgres/20-pagerarchive-metrics.sh that applies the same rule as
// the pagerarchive-not-locked policy and returns just the count (owner
// decision Q1, ADR 0006).
type Locks struct {
	Pool *pgxpool.Pool
	TTL  time.Duration // cache, default 60 s

	mu sync.Mutex
	n  int
	at time.Time
}

// Locked is the number of accounts locked now.
func (l *Locks) Locked(ctx context.Context) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ttl := l.TTL
	if ttl == 0 {
		ttl = time.Minute
	}
	if !l.at.IsZero() && time.Since(l.at) < ttl {
		return l.n, nil
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var n int
	if err := l.Pool.QueryRow(qctx, "SELECT public.pagerarchive_locked_accounts()").Scan(&n); err != nil {
		return 0, err
	}
	l.n, l.at = n, time.Now()
	return n, nil
}
