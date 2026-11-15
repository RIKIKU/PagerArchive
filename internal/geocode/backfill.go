package geocode

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// backfillKey is the settings row holding how far BackfillLocalities has
// got: the last message id it looked at, "recount" once every page is done
// and the report counts are to be built again, then "done".
const backfillKey = "message_localities_backfill"

// BackfillBatch is how many pages BackfillLocalities reads in one
// transaction.
const BackfillBatch = 2000

// BackfillLocalities works out the derived locality (R24, ADR 0034) of
// every page stored before migration 00025 kept them, a batch at a time, in
// id order, then calls recount (the reports' Rebuild) once. Each batch's
// rows and how far it got are written in one transaction, so a restart
// carries on where it stopped, and a batch run again changes nothing. Only
// pages with no locality field are read, and the locality is worked out from
// the street index alone (Geocoder.Locality), so it costs no lookups. A page
// re-parsed meanwhile isn't overwritten unless it still has no locality
// field (its re-parse wrote its own row). Its rows queue no jobs for the
// reports (pagerarchive.backfill): recount counts them all at once instead.
// Between batches it waits as long as the batch took, so it takes at most
// half of one connection from live pages. It logs its progress (counts only,
// P1) every interval, and returns when it's done.
func (g *Geocoder) BackfillLocalities(ctx context.Context, st *store.Store, log *slog.Logger, interval time.Duration, recount func(context.Context) error) error {
	state, err := st.GetSetting(ctx, backfillKey)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		state = "0"
	case err != nil:
		return err
	}
	if state == "done" {
		return nil
	}
	if state != "recount" {
		if err := g.backfill(ctx, st, log, interval, state); err != nil {
			return err
		}
	}
	if recount != nil {
		start := time.Now()
		if err := recount(ctx); err != nil {
			return err
		}
		if log != nil {
			log.Info("report counts built again with stored pages' localities", "seconds", int(time.Since(start).Seconds()))
		}
	}
	return st.SetSetting(ctx, db.SetSettingParams{Key: backfillKey, Value: "done"})
}

// backfill does the pages after the id in state.
func (g *Geocoder) backfill(ctx context.Context, st *store.Store, log *slog.Logger, interval time.Duration, state string) error {
	after, err := strconv.ParseInt(state, 10, 64)
	if err != nil {
		after = 0
	}
	var last int64
	if err := st.Pool.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM messages`).Scan(&last); err != nil {
		return err
	}
	if log != nil {
		log.Info("working out stored pages' localities, in the background", "from_id", after, "to_id", last)
	}
	start, logged := time.Now(), time.Now()
	var read, found int64
	for {
		t0 := time.Now()
		n, k, next, err := g.backfillBatch(ctx, st, after)
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
			log.Info("working out stored pages' localities", "pages", read, "localities", found, "percent", pct, "seconds", int(time.Since(start).Seconds()))
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
		log.Info("stored pages' localities worked out", "pages", read, "localities", found, "seconds", int(time.Since(start).Seconds()))
	}
	return nil
}

// backfillBatch does the pages after id after: how many it read, how many
// localities it found, and the last id it read.
func (g *Geocoder) backfillBatch(ctx context.Context, st *store.Store, after int64) (n, found int, last int64, err error) {
	last = after
	err = pgx.BeginFunc(ctx, st.Pool, func(tx pgx.Tx) error {
		// The report counts are built again afterwards, so its rows queue no
		// jobs (migration 00025's triggers).
		if _, err := tx.Exec(ctx, `SET LOCAL pagerarchive.backfill = 'on'`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, COALESCE(address, ''), COALESCE(description, '') FROM messages
			WHERE id > $1 AND locality IS NULL ORDER BY id LIMIT $2`, after, BackfillBatch)
		if err != nil {
			return err
		}
		ids, localities := []int64{}, []string{}
		for rows.Next() {
			var id int64
			var in Input
			if err := rows.Scan(&id, &in.Address, &in.Description); err != nil {
				rows.Close()
				return err
			}
			n++
			last = id
			if l := g.Locality(in); l != "" {
				ids, localities = append(ids, id), append(localities, l)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		found = len(ids)
		if len(ids) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO message_localities (message_id, locality)
				SELECT u.id, u.locality FROM unnest($1::bigint[], $2::text[]) AS u (id, locality)
				JOIN messages m ON m.id = u.id AND m.locality IS NULL
				ON CONFLICT (message_id) DO NOTHING`, ids, localities); err != nil {
				return err
			}
		}
		state := strconv.FormatInt(last, 10)
		if n < BackfillBatch {
			state = "recount"
		}
		_, err = tx.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, backfillKey, state)
		return err
	})
	return n, found, last, err
}
