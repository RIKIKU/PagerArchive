// Package store owns the PostgreSQL connection, migrations and client keys.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the database.
type Store struct {
	Pool *pgxpool.Pool
	*db.Queries
}

// Open connects and applies pending migrations.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("database: %s", SafeError(err))
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: %s", SafeError(err))
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool, Queries: db.New(pool)}, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub, goose.WithLogger(goose.NopLogger()))
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrations: %s", SafeError(err))
	}
	return nil
}

// Close closes the pool.
func (s *Store) Close() { s.Pool.Close() }

// InTx runs fn in a transaction.
func (s *Store) InTx(ctx context.Context, fn func(*db.Queries) error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return fn(s.Queries.WithTx(tx))
	})
}

// SafeError describes a database error without its message text, which can
// echo values (P1). PostgreSQL errors are reduced to their SQLSTATE.
func SafeError(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "SQLSTATE " + pgErr.Code
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return "can't connect to the database"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err.Error()
	}
	return fmt.Sprintf("%T", err)
}

// HashKey hashes a client API key. Keys are 256-bit random, so a plain
// SHA-256 is enough.
func HashKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// NewKey returns a new client API key.
func NewKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "pa_" + base64.RawURLEncoding.EncodeToString(b)
}

// AddClient creates a client and returns its API key, shown only once.
func (s *Store) AddClient(ctx context.Context, id, name string) (string, error) {
	key := NewKey()
	if name == "" {
		name = id
	}
	if err := s.CreateClient(ctx, db.CreateClientParams{ID: id, Name: name, KeyHash: HashKey(key)}); err != nil {
		return "", fmt.Errorf("add client %s: %s", id, SafeError(err))
	}
	return key, nil
}
