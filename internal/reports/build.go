// Package reports keeps the job counts behind M24's reports (R38a-R38i,
// ADR 0031) and answers them. Reports never read the pages: they read
// report_jobs (one row per job), report_job_whos (each alias or capcode a
// job paged) and report_days (jobs per day, by what reports group and filter
// by), which migration 00023 made. Triggers on messages and aliases queue
// the jobs whose pages changed in report_dirty; CatchUp counts them again,
// in the background (Run) and before each report is answered.
package reports

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RIKIKU/PagerArchive/internal/store"
)

// lockKey is the transaction-level advisory lock held while the report
// tables are written, so one writer at a time does it.
const lockKey = 7_315_023

// builtKey is the settings row saying the tables were built, and for which
// time zone: days and hours are the deployment's (R35a), so a new time zone
// builds them again.
const builtKey = "reports_built"

// builtVersion changes when what the tables hold changes, so a new version
// of this package builds them again.
const builtVersion = "1"

// Batch is how many jobs CatchUp counts again in one transaction.
const Batch = 500

// ErrBusy: another writer holds the lock.
var ErrBusy = errors.New("the report tables are being written")

// Service keeps the report tables and answers reports.
type Service struct {
	Pool     *pgxpool.Pool
	Location *time.Location
	Log      *slog.Logger
	// Poll is how often Run looks for jobs to count again.
	Poll time.Duration

	mu           sync.Mutex
	eventTypes   []string // FilterOptions', kept for OptionsFresh
	eventTypesAt time.Time
}

func (s *Service) tz() string {
	if s.Location == nil {
		return "UTC"
	}
	return s.Location.String()
}

func (s *Service) builtValue() string { return builtVersion + " " + s.tz() }

// Built says whether the tables were built for this time zone.
func (s *Service) Built(ctx context.Context) (bool, error) {
	var v string
	err := s.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, builtKey).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return v == s.builtValue(), err
}

// Pending is how many jobs wait to be counted again (up to 100,000).
func (s *Service) Pending(ctx context.Context) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM report_dirty LIMIT 100000) d`).Scan(&n)
	return n, err
}

// basePages is every page a report counts, with its job's key: not a
// heartbeat (R35b), not to an ignored alias (R27). %s narrows it.
const basePages = `
SELECT COALESCE('j' || m.job_number, 'm' || m.id) AS job_key, m.id, m.job_number, m.capcode, m.sent_at, m.alias_id,
       COALESCE(a.template_id, 0) AS template_id, m.event_type, m.locality, m.units,
       CASE m.priority WHEN 'Emergency' THEN 3 WHEN 'Non-emergency' THEN 2 WHEN 'Admin' THEN 1 ELSE 0 END AS rank
FROM messages m LEFT JOIN aliases a ON a.id = m.alias_id
WHERE NOT m.heartbeat AND a.ignored IS NOT TRUE AND (%s)`

// countJobs writes report_jobs and report_job_whos for the jobs whose pages
// the condition picks (all of each job's pages), in one statement. $1 is the
// time zone.
const countJobs = `
WITH base AS MATERIALIZED (` + basePages + `
), jobs AS (
    SELECT job_key, min(job_number) AS job_number, min(sent_at) AS first_at, max(sent_at) AS last_at,
           COALESCE((array_agg(template_id ORDER BY sent_at, id) FILTER (WHERE alias_id IS NOT NULL))[1], 0) AS template_id,
           COALESCE((array_agg(event_type ORDER BY sent_at, id) FILTER (WHERE event_type IS NOT NULL))[1], '') AS event_type,
           COALESCE((array_agg(locality ORDER BY sent_at, id) FILTER (WHERE locality IS NOT NULL))[1], '') AS locality,
           max(rank) AS rank, count(*)::int AS pages, count(DISTINCT capcode)::int AS capcodes
    FROM base GROUP BY job_key
), units AS (
    SELECT b.job_key, array_agg(DISTINCT u ORDER BY u) AS units
    FROM base b, unnest(b.units) AS u WHERE u <> '' GROUP BY b.job_key
), ins AS (
    INSERT INTO report_jobs (job_key, job_number, first_at, last_at, day, hour, template_id, event_type, locality, priority, pages, capcodes, appliances)
    SELECT j.job_key, j.job_number, j.first_at, j.last_at, (j.first_at AT TIME ZONE $1)::date,
           extract(hour FROM j.first_at AT TIME ZONE $1)::smallint, j.template_id, j.event_type, j.locality,
           (ARRAY['', 'Admin', 'Non-emergency', 'Emergency'])[j.rank + 1], j.pages, j.capcodes, COALESCE(u.units, '{}')
    FROM jobs j LEFT JOIN units u USING (job_key)
    RETURNING job_key, day, hour, event_type, priority
)
INSERT INTO report_job_whos (job_key, who, template_id, day, hour, event_type, priority)
SELECT w.job_key, w.who, w.template_id, i.day, i.hour, i.event_type, i.priority
FROM (SELECT job_key, CASE WHEN alias_id IS NULL THEN 'c' || capcode ELSE 'a' || alias_id END AS who, max(template_id) AS template_id
      FROM base GROUP BY 1, 2) w
JOIN ins i USING (job_key)`

// countDays writes report_days for the days the condition picks (on a
// table's day column, d.day).
const countDays = `
INSERT INTO report_days (kind, day, template_id, event_type, priority, key, jobs)
SELECT 't', d.day, d.template_id, d.event_type, d.priority, '', count(*) FROM report_jobs d WHERE %[1]s GROUP BY 2, 3, 4, 5, 6
UNION ALL
SELECT 'h', d.day, d.template_id, d.event_type, d.priority, d.hour::text, count(*) FROM report_jobs d WHERE %[1]s GROUP BY 2, 3, 4, 5, 6
UNION ALL
SELECT 'l', d.day, d.template_id, d.event_type, d.priority, d.locality, count(*) FROM report_jobs d WHERE %[1]s GROUP BY 2, 3, 4, 5, 6
UNION ALL
SELECT 'p', d.day, d.template_id, d.event_type, d.priority, d.pages::text, count(*) FROM report_jobs d WHERE %[1]s GROUP BY 2, 3, 4, 5, 6
UNION ALL
SELECT 'c', d.day, d.template_id, d.event_type, d.priority, d.capcodes::text, count(*) FROM report_jobs d WHERE %[1]s GROUP BY 2, 3, 4, 5, 6
UNION ALL
SELECT 'n', d.day, d.template_id, d.event_type, d.priority, cardinality(d.appliances)::text, count(*) FROM report_jobs d WHERE %[1]s GROUP BY 2, 3, 4, 5, 6
UNION ALL
SELECT 'a', d.day, d.template_id, d.event_type, d.priority, u, count(*) FROM report_jobs d, unnest(d.appliances) AS u WHERE %[1]s GROUP BY 2, 3, 4, 5, 6
UNION ALL
SELECT 'w', d.day, d.template_id, d.event_type, d.priority, d.who, count(*) FROM report_job_whos d WHERE %[1]s GROUP BY 2, 3, 4, 5, 6`

// Rebuild counts every job and day again from the pages, in one
// transaction: readers see the old counts until it commits. It leaves the
// queue alone: what was queued before it is counted again afterwards, which
// changes nothing, and a page stored meanwhile never waits for it.
func (s *Service) Rebuild(ctx context.Context) error {
	start := time.Now()
	err := pgx.BeginTxFunc(ctx, s.Pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		for _, stmt := range []string{"SET LOCAL jit = off", "SET LOCAL work_mem = '32MB'"} {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
			return err
		}
		for _, stmt := range []string{`DELETE FROM report_days`, `DELETE FROM report_job_whos`, `DELETE FROM report_jobs`} {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(countJobs, "true"), s.tz()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(countDays, "true")); err != nil {
			return err
		}
		// And every large event's jobs (R38i), which the triggers keep after.
		if _, err := tx.Exec(ctx, `INSERT INTO report_event_jobs (event_id, jobs)
			SELECT lem.event_id, count(DISTINCT m.job_number) FROM large_event_messages lem JOIN messages m ON m.id = lem.message_id GROUP BY lem.event_id
			ON CONFLICT (event_id) DO UPDATE SET jobs = EXCLUDED.jobs`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, builtKey, s.builtValue())
		return err
	})
	if err == nil && s.Log != nil {
		s.Log.Info("report counts built", "seconds", int(time.Since(start).Seconds()))
	}
	return err
}

// catchUpBatch counts up to n waiting jobs again, and the days they were
// and are on. It returns how many it took, or ErrBusy.
func (s *Service) catchUpBatch(ctx context.Context, n int) (int, error) {
	done := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL jit = off"); err != nil {
			return err
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, lockKey).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrBusy
		}
		rows, err := tx.Query(ctx, `DELETE FROM report_dirty WHERE id IN
			(SELECT id FROM report_dirty ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING job_key`, n)
		if err != nil {
			return err
		}
		queued, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(queued) == 0 {
			return err
		}
		done = len(queued)
		slices.Sort(queued)
		keys := slices.Compact(queued)
		numbers, ids := []string{}, []int64{}
		for _, k := range keys {
			if strings.HasPrefix(k, "j") {
				numbers = append(numbers, k[1:])
			} else {
				var id int64
				if _, err := fmt.Sscan(k[1:], &id); err == nil {
					ids = append(ids, id)
				}
			}
		}
		rows, err = tx.Query(ctx, `DELETE FROM report_jobs WHERE job_key = ANY($1) RETURNING day`, keys)
		if err != nil {
			return err
		}
		days, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(countJobs, "m.job_number = ANY($2::text[]) OR (m.job_number IS NULL AND m.id = ANY($3::bigint[]))"), s.tz(), numbers, ids); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT DISTINCT day FROM report_jobs WHERE job_key = ANY($1)`, keys)
		if err != nil {
			return err
		}
		now, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
		if err != nil {
			return err
		}
		days = append(days, now...)
		if len(days) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM report_days WHERE day = ANY($1)`, days); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(countDays, "d.day = ANY($1)"), days)
		return err
	})
	return done, err
}

// CatchUp counts waiting jobs again, a batch at a time, until none wait or
// it has spent about limit. It returns ErrBusy if another writer has the
// tables.
func (s *Service) CatchUp(ctx context.Context, limit time.Duration) error {
	end := time.Now().Add(limit)
	for {
		n, err := s.catchUpBatch(ctx, Batch)
		if err != nil || n < Batch || time.Now().After(end) {
			return err
		}
	}
}

// Run builds the tables if they aren't built for this time zone, then
// counts changed jobs again as they're queued. Between batches it waits as
// long as the batch took, so a large backlog (a re-parse of many pages, an
// import) takes at most half of one connection's time from live pages.
func (s *Service) Run(ctx context.Context) {
	poll := s.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for ctx.Err() == nil {
		built, err := s.Built(ctx)
		if err == nil && !built {
			if s.Log != nil {
				s.Log.Info("counting every job for the reports, in the background")
			}
			err = s.Rebuild(ctx)
		}
		if err == nil {
			break
		}
		if ctx.Err() == nil && s.Log != nil {
			s.Log.Error("report counts", "error", store.SafeError(err))
		}
		sleep(ctx, time.Minute)
	}
	for ctx.Err() == nil {
		t0 := time.Now()
		n, err := s.catchUpBatch(ctx, Batch)
		if err != nil && !errors.Is(err, ErrBusy) && ctx.Err() == nil && s.Log != nil {
			s.Log.Error("report counts", "error", store.SafeError(err))
		}
		if n == Batch {
			sleep(ctx, max(time.Since(t0), 50*time.Millisecond))
			continue
		}
		sleep(ctx, poll)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Drain counts every waiting job again, waiting for another writer (a
// rebuild) to finish first: the nightly run's, so the counts follow its
// retention and redaction in the same run (P1).
func (s *Service) Drain(ctx context.Context) error {
	for {
		n, err := s.catchUpBatch(ctx, Batch)
		if errors.Is(err, ErrBusy) {
			sleep(ctx, time.Second)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if err != nil || n == 0 {
			return err
		}
	}
}
