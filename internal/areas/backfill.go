package areas

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/RIKIKU/PagerArchive/internal/store"
)

// BackfillBatch is how many pages Backfill reads in one transaction.
const BackfillBatch = 2000

// Backfill works out the area of every page stored before the areas were
// loaded, or before they changed (R88a, R88d; ADR 0035), a batch at a time in
// id order, as ADR 0034's localities are. Each batch's rows and how far it
// got are written in one transaction, so a restart carries on where it
// stopped, and a batch run again changes nothing. A page's area is worked
// out from where it's placed (area_at), and an unchanged row isn't written;
// a page with no location, or in no area, loses its row. Each batch reads
// one snapshot (repeatable read), so a page placed again meanwhile, whose
// row the re-parse wrote, makes the batch fail and run again rather than be
// given the area it had. Between batches it waits as long as the batch took,
// so it takes at most half of one connection from live pages. It logs its
// progress every interval (counts only, P1), and returns when it's done, or
// at once when there's nothing to do.
func Backfill(ctx context.Context, st *store.Store, log *slog.Logger, interval time.Duration) error {
	state, err := st.GetSetting(ctx, backfillKey)
	if errors.Is(err, pgx.ErrNoRows) || state == "done" {
		return nil
	}
	if err != nil {
		return err
	}
	after, err := strconv.ParseInt(state, 10, 64)
	if err != nil {
		after = 0
	}
	var last int64
	if err := st.Pool.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM messages`).Scan(&last); err != nil {
		return err
	}
	if log != nil {
		log.Info("working out stored pages' areas, in the background", "from_id", after, "to_id", last)
	}
	start, logged := time.Now(), time.Now()
	var read, found int64
	for {
		t0 := time.Now()
		n, k, next, err := batch(ctx, st, after)
		if isSerialization(err) {
			continue // a page in the batch was placed again meanwhile: read it again
		}
		if err != nil {
			return err
		}
		read, found, after = read+int64(n), found+int64(k), next
		if n < BackfillBatch {
			break
		}
		if log != nil && time.Since(logged) >= interval {
			logged = time.Now()
			pct := 100
			if last > 0 {
				pct = int(min(100*after/last, 100))
			}
			log.Info("working out stored pages' areas", "pages", read, "in_areas", found, "percent", pct, "seconds", int(time.Since(start).Seconds()))
		}
		t := time.NewTimer(time.Since(t0))
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	if log != nil {
		log.Info("stored pages' areas worked out", "pages", read, "in_areas", found, "seconds", int(time.Since(start).Seconds()))
	}
	return nil
}

// batch works out the areas of the pages after id after: how many pages it
// read, how many are in an area, and the last id it read.
func batch(ctx context.Context, st *store.Store, after int64) (n, found int, last int64, err error) {
	last = after
	err = pgx.BeginTxFunc(ctx, st.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `WITH batch AS (
				SELECT id, location FROM messages WHERE id > $1 ORDER BY id LIMIT $2
			), found AS (
				SELECT id, area_at(location) AS area FROM batch WHERE location IS NOT NULL
			), gone AS (
				DELETE FROM message_areas ma USING batch b
				WHERE ma.message_id = b.id AND NOT EXISTS (SELECT 1 FROM found f WHERE f.id = b.id AND f.area IS NOT NULL)
			), put AS (
				INSERT INTO message_areas (message_id, area) SELECT id, area FROM found WHERE area IS NOT NULL
				ON CONFLICT (message_id) DO UPDATE SET area = EXCLUDED.area
				WHERE message_areas.area IS DISTINCT FROM EXCLUDED.area
			)
			SELECT count(*), COALESCE(max(id), $1), (SELECT count(*) FROM found WHERE area IS NOT NULL) FROM batch`,
			after, BackfillBatch).Scan(&n, &last, &found); err != nil {
			return err
		}
		state := strconv.FormatInt(last, 10)
		if n < BackfillBatch {
			state = "done"
		}
		_, err := tx.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, backfillKey, state)
		return err
	})
	return n, found, last, err
}

// isSerialization reports whether err is a serialization failure, which a
// repeatable-read transaction is retried after.
func isSerialization(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "40001"
}

// Pending reports whether stored pages' areas are still being worked out
// (R88e).
func Pending(ctx context.Context, st *store.Store) (bool, error) {
	state, err := st.GetSetting(ctx, backfillKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && state != "done", err
}
