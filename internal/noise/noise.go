// Package noise works out "probably noise" (SPEC.md R35h) for the pages
// stored before it was kept. New pages get theirs as they're stored
// (ingest.Save); this is the one pass over the ones before, in the
// background after the start (R91a).
package noise

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// backfillKey keeps how far the pass has got: the start of the next span
// to do (RFC 3339), or "done".
const backfillKey = "probably_noise_backfill"

// Span is how much of the archive one transaction works out: a day's pages,
// of which only those with no format are read.
const Span = 24 * time.Hour

// Window is leg (b)'s: another client's copy within this of a page.
const Window = 10 * time.Minute

// Backfill marks the stored pages that are probably noise (R35h), a day at
// a time from Window before the second client's first copy (no page before
// can qualify) up to now, each day in one transaction with how far it got,
// so a restart carries on where it stopped and a day done again changes
// nothing. Each day takes the live copies' lock (ingest's LockNoise), so a
// copy arriving meanwhile is worked out after it, seeing what it wrote.
// Between days it waits as long as the day took, so it takes at most half
// of one connection. It returns when it's done, or at once once it has
// been; with fewer than two clients' copies there's nothing to mark, and
// later pages are worked out as they're stored.
func Backfill(ctx context.Context, st *store.Store, log *slog.Logger, interval time.Duration) error {
	state, err := st.GetSetting(ctx, backfillKey)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		state = ""
	case err != nil:
		return err
	}
	if state == "done" {
		return nil
	}
	from, err := time.Parse(time.RFC3339Nano, state)
	if err != nil {
		start, ok, err := Start(ctx, st)
		if err != nil {
			return err
		}
		if !ok {
			return st.SetSetting(ctx, db.SetSettingParams{Key: backfillKey, Value: "done"})
		}
		from = start
	}
	end := time.Now()
	if log != nil {
		log.Info("working out stored pages' probably noise, in the background", "from", from.UTC().Format(time.RFC3339), "to", end.UTC().Format(time.RFC3339))
	}
	began, logged := time.Now(), time.Now()
	var days, marked int64
	for from.Before(end) {
		t0 := time.Now()
		to := from.Add(Span)
		next := to.UTC().Format(time.RFC3339Nano)
		if !to.Before(end) {
			next = "done"
		}
		var n int64
		err := pgx.BeginFunc(ctx, st.Pool, func(tx pgx.Tx) error {
			q := st.Queries.WithTx(tx)
			if err := q.LockNoise(ctx); err != nil {
				return err
			}
			// A day's pages after the start are worked out as they come,
			// so the last day's end runs on to now and beyond.
			upTo := to
			if next == "done" {
				upTo = time.Now().Add(Span)
			}
			var err error
			if n, err = q.BackfillNoise(ctx, db.BackfillNoiseParams{SentFrom: from, SentTo: upTo}); err != nil {
				return err
			}
			return q.SetSetting(ctx, db.SetSettingParams{Key: backfillKey, Value: next})
		})
		if err != nil {
			return err
		}
		days, marked, from = days+1, marked+n, to
		if log != nil && time.Since(logged) >= interval {
			logged = time.Now()
			log.Info("working out stored pages' probably noise", "days", days, "marked", marked, "seconds", int(time.Since(began).Seconds()))
		}
		t := time.NewTimer(time.Since(t0))
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	if err := st.SetSetting(ctx, db.SetSettingParams{Key: backfillKey, Value: "done"}); err != nil {
		return err
	}
	if log != nil {
		log.Info("stored pages' probably noise worked out", "days", days, "marked", marked, "seconds", int(time.Since(began).Seconds()))
	}
	return nil
}

// Start is where the pass starts: Window before the second client's first
// copy, the earliest any page can qualify (R35h b). ok is false when fewer
// than two clients have copies.
func Start(ctx context.Context, st *store.Store) (time.Time, bool, error) {
	firsts, err := st.FirstReceptions(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(firsts) < 2 {
		return time.Time{}, false, nil
	}
	return firsts[1].Add(-Window), true, nil
}
