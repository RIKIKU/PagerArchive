package authentik

import (
	"context"
	_ "embed"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Locks counts accounts locked by the guessing protection (R44a) for the
// metrics (R52). It connects to Authentik's database as a role that can
// only call pagerarchive_locked_accounts(), a function made by
// EnsureLocksRole that applies the same rule as
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

//go:embed locked_accounts.sql
var lockedAccountsSQL string

// EnsureLocksRole creates or updates what Locks needs in Authentik's database:
// the role pagerarchive_metrics with this password, and
// pagerarchive_locked_accounts(). The API does this on every start, so no
// release leaves it for a hand step (R84). dbURL is a login with rights over
// the server; the database "authentik" comes from Authentik's first-start
// script (deploy/postgres/10-authentik.sh).
func EnsureLocksRole(ctx context.Context, dbURL, password string) error {
	cfg, err := pgx.ParseConfig(dbURL)
	if err != nil {
		return errors.New("the database URL isn't a valid connection string")
	}
	cfg.Database = "authentik"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var alter string
	if err := conn.QueryRow(ctx, `SELECT format('ALTER ROLE pagerarchive_metrics LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %L', $1::text)`, password).Scan(&alter); err != nil {
		return err
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pagerarchive_metrics')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := conn.Exec(ctx, `CREATE ROLE pagerarchive_metrics`); err != nil {
			return err
		}
	}
	for _, sql := range []string{alter, `GRANT CONNECT ON DATABASE authentik TO pagerarchive_metrics`, lockedAccountsSQL} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			return err
		}
	}
	return nil
}
