package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// SearchFirstTry is how long a search the index can serve runs as planned
// before it runs again with the index (R33b, ADR 0017).
const SearchFirstTry = 250 * time.Millisecond

// SearchMessages runs the search (R33, R33b; ADR 0017). For text terms in
// many pages the planner reads the newest pages in order until it has a
// page of results, which takes milliseconds. It judges "many" by assuming
// the terms are independent, so for terms that are each common but rarely
// or never together it reads every page. Such a search is still running
// after SearchFirstTry; it then runs again with that ordered reading off, so
// the index finds the pages. Without JIT compilation, which costs a search
// far more than it saves (up to half a second).
//
// The second try also finds the alias: values' pages by alias_id, looked up
// first (the names are still matched), and has the memory to combine those
// with the text index's pages page by page and row by row (32 MB, for this
// query only). The first try doesn't: knowing the alias, the planner gives
// up reading in order for a common term too.
func (s *Store) SearchMessages(ctx context.Context, p db.SearchMessagesParams) ([]db.SearchMessagesRow, error) {
	if p.AliasNameIds == nil {
		p.AliasNameIds = []int64{}
	}
	if p.QIndex1 == nil && !p.QAnyIndex {
		return s.searchWith(ctx, p, "SET LOCAL jit = off")
	}
	if !s.SecondTryOnly {
		rows, err := s.searchWith(ctx, p, "SET LOCAL jit = off", "SET LOCAL statement_timeout = "+strconv.FormatInt(SearchFirstTry.Milliseconds(), 10))
		var pgErr *pgconn.PgError
		if err == nil || ctx.Err() != nil || !errors.As(err, &pgErr) || pgErr.Code != "57014" {
			return rows, err
		}
	}
	if len(p.Aliases) > 0 || len(p.AliasLikes) > 0 {
		ids, err := s.Queries.AliasIDsNamed(ctx, db.AliasIDsNamedParams{Aliases: p.Aliases, AliasLikes: p.AliasLikes})
		if err != nil {
			return nil, err
		}
		p.AliasNamesResolved, p.AliasNameIds = true, append([]int64{}, ids...)
	}
	return s.searchWith(ctx, p, "SET LOCAL jit = off", "SET LOCAL enable_indexscan = off", "SET LOCAL work_mem = '32MB'")
}

// searchWith runs the search in a transaction with these settings.
func (s *Store) searchWith(ctx context.Context, p db.SearchMessagesParams, set ...string) (rows []db.SearchMessagesRow, err error) {
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, stmt := range set {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		rows, err = s.Queries.WithTx(tx).SearchMessages(ctx, p)
		return err
	})
	return rows, err
}
