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
// The alias: values' aliases are looked up first (the names are still
// matched). The bounded and second tries find their pages by alias_id, and have
// the memory to combine those with the text index's pages page by page and row
// by row (32 MB, for this query only). The first try isn't given several:
// knowing them, the planner gives up reading in order for a common term too.
// One alias, though, the first try is given (OnlyAliasID), so the planner can
// read that alias's newest pages in order. Through the join on its name it
// can't, and for an alias with a few per cent of the pages it read and sorted
// all of them.
//
// Several aliases that hold most of the pages are the other way round. They
// rule out few pages, so the bounded and second tries aren't given them: the
// planner may read most of the index on alias_id to combine with the text's
// pages. But the first try selects them by alias_id alone, looked up in its own
// snapshot (AliasNamesByID): through the join on their names the planner
// guesses they hold few pages, as each of 2,005 aliases would, and reads and
// sorts every one of them (12 s cold for alias:sta, five aliases holding 90%).
//
// A search with a term too short for a trigram takes the same tries, with the
// bigram index alone finding the pages, for all its terms (QShort).
func (s *Store) SearchMessages(ctx context.Context, p db.SearchMessagesParams) ([]db.SearchMessagesRow, error) {
	return s.searchMessages(ctx, p, newestFirst)
}

// SearchNewer is SearchMessages for the messages after a position (AfterSent,
// AfterID), the nearest first: oldest first (R81b, ADR 0025).
func (s *Store) SearchNewer(ctx context.Context, p db.SearchMessagesParams) ([]db.SearchMessagesRow, error) {
	return s.searchMessages(ctx, p, oldestFirst)
}

func (s *Store) searchMessages(ctx context.Context, p db.SearchMessagesParams, o order) ([]db.SearchMessagesRow, error) {
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
	p, most, err := s.withAliasIDs(ctx, p)
	if err != nil {
		return nil, err
	}
	first := p
	switch {
	case len(p.AliasIds) == 1:
		first.OnlyAliasID = &p.AliasIds[0]
	case p.AliasNamesResolved && len(p.AliasNameIds) == 1:
		first.OnlyAliasID = &p.AliasNameIds[0]
	}
	first.AliasNamesResolved, first.AliasNameIds = false, []int64{}
	first.AliasNamesByID = most && len(p.AliasNameIds) > 1
	if most {
		p.AliasNamesResolved, p.AliasNameIds = false, []int64{}
	}
	if p.QIndex1 == nil && !p.QAnyIndex && len(p.QShort) == 0 {
		return s.searchWith(ctx, first, o, "SET LOCAL jit = off")
	}
	if !s.SecondTryOnly && !s.BoundedTryOnly {
		rows, err := s.searchFirstTry(ctx, first, o, s.firstTry(p))
		if !searchTimedOut(ctx, err) {
			return rows, err
		}
	}
	if !s.SecondTryOnly {
		rows, complete, err := s.searchBoundedTry(ctx, p, o)
		if err != nil || complete {
			return rows, err
		}
	}
	if !s.SecondTryOnly && !s.BoundedTryOnly {
		rows, err := s.searchFirstTry(ctx, first, o, searchFirstTryAgain)
		if !searchTimedOut(ctx, err) {
			return rows, err
		}
	}
	return s.searchSecondTry(ctx, p, o)
}

// searchBigrams is how many bigrams a search gives the bigram index at most.
const searchBigrams = 3

// rarestBigrams picks a search's bigrams by the planner's statistics on the
// bigram index's expression: the bigrams it doesn't list are rarer than those
// it does. Without statistics (before the first ANALYZE), all of them.
const rarestBigrams = `
WITH s AS (SELECT most_common_elems::text::text[] AS e, most_common_elem_freqs AS f
           FROM pg_stats WHERE schemaname = current_schema() AND tablename = 'messages_search_bigrams_idx')
SELECT b FROM unnest(search_terms_bigrams($1::text[])) AS b
ORDER BY (SELECT s.f[array_position(s.e, b)] FROM s) NULLS FIRST, b
LIMIT CASE WHEN EXISTS (SELECT FROM s) THEN $2::int END`

// RarestBigrams are the searchBigrams rarest bigrams of these terms (ADR
// 0017). Each of a term's bigrams is in every page that holds it, so any of
// them is a necessary condition and the pages listed are the same. A common
// one rules out few pages but costs most of the index's reading: a bigram in
// every page has a list of every page. The bigrams go to the query as terms,
// as a bigram's only bigram is itself.
func (s *Store) RarestBigrams(ctx context.Context, terms []string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, rarestBigrams, terms, searchBigrams)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
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
func (s *Store) searchFirstTry(ctx context.Context, p db.SearchMessagesParams, o order, limit time.Duration) ([]db.SearchMessagesRow, error) {
	return s.searchWith(ctx, p, o, "SET LOCAL jit = off", "SET LOCAL statement_timeout = "+strconv.FormatInt(limit.Milliseconds(), 10))
}

// withAliasIDs adds the alias: values' aliases, looked up first, and says
// whether several hold most of the pages: over half, by the planner's own
// estimate. For one alias that isn't asked: the first try has it by alias_id
// anyway.
func (s *Store) withAliasIDs(ctx context.Context, p db.SearchMessagesParams) (db.SearchMessagesParams, bool, error) {
	if len(p.Aliases) == 0 && len(p.AliasLikes) == 0 {
		return p, false, nil
	}
	ids, err := s.Queries.AliasIDsNamed(ctx, db.AliasIDsNamedParams{Aliases: p.Aliases, AliasLikes: p.AliasLikes})
	if err != nil {
		return p, false, err
	}
	p.AliasNamesResolved, p.AliasNameIds = true, append([]int64{}, ids...)
	if len(ids) < 2 {
		return p, false, nil
	}
	share, err := s.aliasShare(ctx, ids)
	return p, share > 0.5, err
}

// aliasShare is the planner's estimate of the share of pages these aliases
// hold, from its statistics: no page is read.
func (s *Store) aliasShare(ctx context.Context, ids []int64) (float64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var plan []struct {
		Plan struct {
			Rows float64 `json:"Plan Rows"`
		}
	}
	var all float64
	err := s.Pool.QueryRow(ctx, `EXPLAIN (FORMAT JSON) SELECT FROM messages WHERE alias_id = ANY($1::bigint[])`, ids).Scan(&plan)
	if err == nil {
		err = s.Pool.QueryRow(ctx, `SELECT reltuples FROM pg_class WHERE oid = 'messages'::regclass`).Scan(&all)
	}
	if err != nil || len(plan) == 0 || all <= 0 {
		return 0, err
	}
	return plan[0].Plan.Rows / all, nil
}

// searchBoundedTry lets the index find up to searchBoundedRows pages, unordered.
// If it finds fewer, those are all the pages that match: complete is true and
// rows is the answer, in the search's order.
func (s *Store) searchBoundedTry(ctx context.Context, p db.SearchMessagesParams, o order) (rows []db.SearchMessagesRow, complete bool, err error) {
	limit := p.MaxRows
	p.MaxRows = searchBoundedRows
	// With no order to keep, the planner would read the table from its start,
	// expecting to find that many soon: for terms never together, all of it.
	rows, err = s.searchWith(ctx, p, unorderedRows, "SET LOCAL jit = off", "SET LOCAL enable_indexscan = off", "SET LOCAL enable_seqscan = off", "SET LOCAL work_mem = '32MB'")
	if err != nil || len(rows) >= searchBoundedRows {
		return nil, false, err
	}
	slices.SortFunc(rows, func(a, b db.SearchMessagesRow) int {
		c := b.SentAt.Compare(a.SentAt)
		if c == 0 {
			c = cmp.Compare(b.ID, a.ID)
		}
		if o == oldestFirst {
			return -c
		}
		return c
	})
	return rows[:min(len(rows), int(limit))], true, nil
}

// searchSecondTry runs the search with the index, not in order.
func (s *Store) searchSecondTry(ctx context.Context, p db.SearchMessagesParams, o order) ([]db.SearchMessagesRow, error) {
	return s.searchWith(ctx, p, o, "SET LOCAL jit = off", "SET LOCAL enable_indexscan = off", "SET LOCAL work_mem = '32MB'")
}

// order is the order a search's rows come in: the list's, newest first; the
// nearest first after a position (SearchNewer); or none, for the bounded try.
type order int

const (
	newestFirst order = iota
	oldestFirst
	unorderedRows
)

// searchOldestFirst is how the search query ends for SearchNewer.
const searchOldestFirst = "ORDER BY m.sent_at, m.id\n"

// reordered runs the search query in another order, or none, in a transaction.
type reordered struct {
	pgx.Tx
	orderBy string
}

func (u reordered) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return u.Tx.Query(ctx, strings.Replace(sql, searchOrderBy, u.orderBy, 1), args...)
}

// searchWith runs the search in a transaction with these settings. With
// AliasNamesByID, the alias: values' aliases are looked up in the search's own
// snapshot, so they are exactly the aliases the names match there.
func (s *Store) searchWith(ctx context.Context, p db.SearchMessagesParams, o order, set ...string) (rows []db.SearchMessagesRow, err error) {
	var opts pgx.TxOptions
	if p.AliasNamesByID {
		opts.IsoLevel = pgx.RepeatableRead
	}
	err = pgx.BeginTxFunc(ctx, s.Pool, opts, func(tx pgx.Tx) error {
		for _, stmt := range set {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		q := s.Queries.WithTx(tx)
		if p.AliasNamesByID {
			ids, err := q.AliasIDsNamed(ctx, db.AliasIDsNamedParams{Aliases: p.Aliases, AliasLikes: p.AliasLikes})
			if err != nil {
				return err
			}
			p.AliasNamesResolved, p.AliasNameIds = true, append([]int64{}, ids...)
		}
		switch o {
		case unorderedRows:
			q = db.New(reordered{tx, ""})
		case oldestFirst:
			q = db.New(reordered{tx, searchOldestFirst})
		}
		rows, err = q.SearchMessages(ctx, p)
		return err
	})
	return rows, err
}
