package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

const (
	// SearchFirstTry is how long a search the index can serve runs as planned
	// before a bounded try with the index (R33b, ADR 0017).
	SearchFirstTry = 100 * time.Millisecond
	// searchFirstTryOr is the same for a search with or: terms. The planner's
	// estimate holds for those, as they aren't multiplied together, so its
	// first try is rarely wrong, and reading the index for them takes longest.
	searchFirstTryOr = 500 * time.Millisecond
	// searchBoundedRows is how many pages the bounded try stops at. Fewer are
	// all the pages that match.
	searchBoundedRows = 1000
	// searchFirstTryAgain is how long the first try has when it runs again.
	searchFirstTryAgain = time.Second
)

// searchOrderBy is how the search query ends, before its LIMIT.
const searchOrderBy = "ORDER BY m.sent_at DESC, m.id DESC\n"

// SearchMessages runs the search (R33, R33b; ADR 0017). For text terms in many
// pages the planner reads the newest pages in order until it has a page of
// results, which takes milliseconds. It judges "many" by assuming the terms are
// independent, so for terms that are each common but rarely or never together
// it reads every page. Such a search is still running after SearchFirstTry. A
// cold disk also keeps a first try running that long, when it's nearly done.
// Without JIT compilation, which costs a search far more than it saves (up to
// half a second).
//
// A bounded try tells those two apart. It lets the index find the pages,
// unordered, and stops at searchBoundedRows. Terms rarely together are found in
// full, from little reading, and that's the answer. Common terms stop it soon
// after it has read the index, and the first try runs again, with what it read
// still in the caches. Only if that doesn't answer in searchFirstTryAgain (the
// pages may all be old) does a second try read and sort them all. They run in
// turn, as on a cold disk a try reading the table leaves no room for another.
//
// The bounded and second tries find the alias: values' pages by alias_id, looked
// up first (the names are still matched), and have the memory to combine those
// with the text index's pages page by page and row by row (32 MB, for this
// query only). The first try doesn't: knowing the alias, the planner gives up
// reading in order for a common term too.
func (s *Store) SearchMessages(ctx context.Context, p db.SearchMessagesParams) ([]db.SearchMessagesRow, error) {
	if p.AliasNameIds == nil {
		p.AliasNameIds = []int64{}
	}
	if p.QIndex1 == nil && !p.QAnyIndex {
		return s.searchWith(ctx, p, false, "SET LOCAL jit = off")
	}
	if !s.SecondTryOnly && !s.BoundedTryOnly {
		rows, err := s.searchFirstTry(ctx, p, s.firstTry(p))
		if !searchTimedOut(ctx, err) {
			return rows, err
		}
	}
	if !s.SecondTryOnly {
		rows, complete, err := s.searchBoundedTry(ctx, p)
		if err != nil || complete {
			return rows, err
		}
	}
	if !s.SecondTryOnly && !s.BoundedTryOnly {
		rows, err := s.searchFirstTry(ctx, p, searchFirstTryAgain)
		if !searchTimedOut(ctx, err) {
			return rows, err
		}
	}
	return s.searchSecondTry(ctx, p)
}

// firstTry is how long this search's first try runs.
func (s *Store) firstTry(p db.SearchMessagesParams) time.Duration {
	switch {
	case s.FirstTry > 0:
		return s.FirstTry
	case len(p.QAny) > 0:
		return searchFirstTryOr
	}
	return SearchFirstTry
}

// searchTimedOut says whether a try reached its limit, with the search itself
// still wanted.
func searchTimedOut(ctx context.Context, err error) bool {
	var pgErr *pgconn.PgError
	return err != nil && ctx.Err() == nil && errors.As(err, &pgErr) && pgErr.Code == "57014"
}

// searchFirstTry runs the search as planned, for this long.
func (s *Store) searchFirstTry(ctx context.Context, p db.SearchMessagesParams, limit time.Duration) ([]db.SearchMessagesRow, error) {
	return s.searchWith(ctx, p, false, "SET LOCAL jit = off", "SET LOCAL statement_timeout = "+strconv.FormatInt(limit.Milliseconds(), 10))
}

// withAliasIDs adds the alias: values' aliases, looked up first. Values that
// name every alias rule out no alias's pages, so the planner isn't given them:
// it may read the whole index on alias_id to combine with the text's pages.
func (s *Store) withAliasIDs(ctx context.Context, p db.SearchMessagesParams) (db.SearchMessagesParams, error) {
	if len(p.Aliases) > 0 || len(p.AliasLikes) > 0 {
		ids, err := s.Queries.AliasIDsNamed(ctx, db.AliasIDsNamedParams{Aliases: p.Aliases, AliasLikes: p.AliasLikes})
		if err != nil {
			return p, err
		}
		var aliases int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM aliases`).Scan(&aliases); err != nil {
			return p, err
		}
		if len(ids) < aliases {
			p.AliasNamesResolved, p.AliasNameIds = true, append([]int64{}, ids...)
		}
	}
	return p, nil
}

// searchBoundedTry lets the index find up to searchBoundedRows pages, unordered.
// If it finds fewer, those are all the pages that match: complete is true and
// rows is the answer, in the search's order.
func (s *Store) searchBoundedTry(ctx context.Context, p db.SearchMessagesParams) (rows []db.SearchMessagesRow, complete bool, err error) {
	limit := p.MaxRows
	if p, err = s.withAliasIDs(ctx, p); err != nil {
		return nil, false, err
	}
	p.MaxRows = searchBoundedRows
	// With no order to keep, the planner would read the table from its start,
	// expecting to find that many soon: for terms never together, all of it.
	rows, err = s.searchWith(ctx, p, true, "SET LOCAL jit = off", "SET LOCAL enable_indexscan = off", "SET LOCAL enable_seqscan = off", "SET LOCAL work_mem = '32MB'")
	if err != nil || len(rows) >= searchBoundedRows {
		return nil, false, err
	}
	slices.SortFunc(rows, func(a, b db.SearchMessagesRow) int {
		if c := b.SentAt.Compare(a.SentAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return rows[:min(len(rows), int(limit))], true, nil
}

// searchSecondTry runs the search with the index, not in order.
func (s *Store) searchSecondTry(ctx context.Context, p db.SearchMessagesParams) ([]db.SearchMessagesRow, error) {
	p, err := s.withAliasIDs(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.searchWith(ctx, p, false, "SET LOCAL jit = off", "SET LOCAL enable_indexscan = off", "SET LOCAL work_mem = '32MB'")
}

// unordered runs the search query without its order, in a transaction.
type unordered struct{ pgx.Tx }

func (u unordered) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return u.Tx.Query(ctx, strings.Replace(sql, searchOrderBy, "", 1), args...)
}

// searchWith runs the search in a transaction with these settings.
func (s *Store) searchWith(ctx context.Context, p db.SearchMessagesParams, noOrder bool, set ...string) (rows []db.SearchMessagesRow, err error) {
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, stmt := range set {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		q := s.Queries.WithTx(tx)
		if noOrder {
			q = db.New(unordered{tx})
		}
		rows, err = q.SearchMessages(ctx, p)
		return err
	})
	return rows, err
}
