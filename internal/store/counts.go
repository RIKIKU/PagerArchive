package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// CountsTimeout is how long counting a scrubber's marks may take before it
// gives up (R81c, ADR 0026): the counts are an extra, and never hold up the
// list or take more of the database than a search does.
const CountsTimeout = 2 * time.Second

// ErrCountsTimedOut: the counts took longer than their limit.
var ErrCountsTimedOut = errors.New("counting took too long")

// CountMessages counts the messages a search finds in each span of a range,
// for the scrubber's marks (R81b, R81c; ADR 0026). bounds are the spans'
// edges, oldest first: counts[i] is how many were sent in [bounds[i],
// bounds[i+1]). It runs the search's own query (SearchMessages), with its
// columns, order and limit replaced by a count per span, so the counts
// follow every filter the list does, from one statement that reads only the
// range's messages. A range's pages are found by their sent time
// (messages_sent_idx), or by alias and sent time for an alias's
// (messages_alias_sent_idx), and a text search's by the search indexes.
func (s *Store) CountMessages(ctx context.Context, p db.SearchMessagesParams, bounds []time.Time, timeout time.Duration) ([]int64, error) {
	if len(bounds) < 2 {
		return []int64{}, nil
	}
	if p.AliasNameIds == nil {
		p.AliasNameIds = []int64{}
	}
	if p.QShort == nil {
		p.QShort = []string{}
	}
	if len(p.QShort) > 0 {
		short, err := s.RarestBigrams(ctx, p.QShort)
		if err != nil {
			return nil, err
		}
		p.QShort = short
	}
	// The alias: values' aliases, looked up first, as the search's bounded
	// and second tries have them: their pages are found by alias_id.
	p, most, err := s.withAliasIDs(ctx, p)
	if err != nil {
		return nil, err
	}
	if most {
		p.AliasNamesResolved, p.AliasNameIds = false, []int64{}
	}
	from, to := bounds[0], bounds[len(bounds)-1]
	p.SentFrom, p.SentTo = &from, &to
	p.BeforeSent, p.BeforeID, p.AfterSent, p.AfterID = nil, nil, nil, nil
	p.MaxRows = int32(len(bounds) + 1)

	sql, args, err := searchSQL(ctx, p)
	if err != nil {
		return nil, err
	}
	sql, err = countsSQL(sql, len(args)+1)
	if err != nil {
		return nil, err
	}
	args = append(args, bounds)
	counts := make([]int64, len(bounds)-1)
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, stmt := range []string{"SET LOCAL jit = off", "SET LOCAL statement_timeout = " + strconv.FormatInt(timeout.Milliseconds(), 10)} {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var span int32
			var n int64
			if err := rows.Scan(&span, &n); err != nil {
				return err
			}
			if span >= 1 && int(span) <= len(counts) {
				counts[span-1] = n
			}
		}
		return rows.Err()
	})
	var pgErr *pgconn.PgError
	if err != nil && ctx.Err() == nil && errors.As(err, &pgErr) && pgErr.Code == "57014" {
		return nil, ErrCountsTimedOut
	}
	return counts, err
}

// errCaptured stops the search once its SQL and arguments are captured.
var errCaptured = errors.New("captured")

// capture is a database that keeps the query it's given and runs nothing.
type capture struct {
	sql  *string
	args *[]any
}

func (c capture) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errCaptured
}

func (c capture) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	*c.sql, *c.args = sql, args
	return nil, errCaptured
}

func (c capture) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

// searchSQL is the search query and its arguments for p, as SearchMessages
// would send them.
func searchSQL(ctx context.Context, p db.SearchMessagesParams) (string, []any, error) {
	var sql string
	var args []any
	_, err := db.New(capture{&sql, &args}).SearchMessages(ctx, p)
	if !errors.Is(err, errCaptured) || sql == "" {
		return "", nil, fmt.Errorf("search query not captured: %v", err)
	}
	return sql, args, nil
}

// countsSQL turns the search query into a count per span: the spans' edges
// are parameter n, a timestamptz array.
func countsSQL(sql string, n int) (string, error) {
	start := strings.Index(sql, "SELECT m.id,")
	from := strings.Index(sql, "\nFROM messages m\n")
	if start < 0 || from < start || !strings.Contains(sql, searchOrderBy) {
		return "", errors.New("search query has an unexpected shape")
	}
	sql = sql[:start] + "SELECT width_bucket(m.sent_at, $" + strconv.Itoa(n) + "::timestamptz[])::int AS span, count(*)::bigint" + sql[from:]
	return strings.Replace(sql, searchOrderBy, "GROUP BY 1\n", 1), nil
}
