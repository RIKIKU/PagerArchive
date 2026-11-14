package reports

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"time"

	"github.com/jackc/pgx/v5"
)

// Filter is what a report counts (R38a): jobs that started on From to To
// (whole days in the deployment's time zone, inclusive), of the agencies'
// templates (nil: every agency), with the event type and priority given
// ("": any).
type Filter struct {
	From, To  time.Time // days, as dates at midnight UTC
	Templates []int64
	EventType string
	Priority  string
}

// Days is how many days the filter's range holds.
func (f Filter) Days() int { return int(f.To.Sub(f.From).Hours()/24) + 1 }

// Previous is the filter over the same number of days just before (R38b).
func (f Filter) Previous() Filter {
	p := f
	p.To = f.From.AddDate(0, 0, -1)
	p.From = f.From.AddDate(0, 0, -f.Days())
	return p
}

func date(t time.Time) string { return t.Format(time.DateOnly) }

// where is the condition on report_days (or a table with the same columns,
// as d) for the filter, with its arguments appended to args. skipEvent
// leaves the event type out (R38e counts every event type).
func (f Filter) where(args *[]any, skipEvent bool) string {
	add := func(v any) string {
		*args = append(*args, v)
		return "$" + strconv.Itoa(len(*args))
	}
	w := []string{"d.day >= " + add(date(f.From)) + "::date", "d.day <= " + add(date(f.To)) + "::date"}
	if f.Templates != nil {
		w = append(w, "d.template_id = ANY("+add(f.Templates)+"::bigint[])")
	}
	if f.EventType != "" && !skipEvent {
		w = append(w, "d.event_type = "+add(f.EventType))
	}
	if f.Priority != "" {
		w = append(w, "d.priority = "+add(f.Priority))
	}
	return strings.Join(w, " AND ")
}

// Count is a key and its jobs.
type Count struct {
	Key  string
	Jobs int64
}

// sum is the jobs per key of one kind of report_days row.
func (s *Service) sum(ctx context.Context, kind string, f Filter, skipEvent bool, keys []string) ([]Count, error) {
	args := []any{kind}
	sql := `SELECT d.key, sum(d.jobs)::bigint FROM report_days d WHERE d.kind = $1 AND ` + f.where(&args, skipEvent)
	if keys != nil {
		args = append(args, keys)
		sql += " AND d.key = ANY($" + strconv.Itoa(len(args)) + "::text[])"
	}
	return s.counts(ctx, sql+" GROUP BY d.key", args...)
}

func (s *Service) counts(ctx context.Context, sql string, args ...any) ([]Count, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Count])
}

// Total is how many jobs the filter holds, from the 't' rows.
func (s *Service) Total(ctx context.Context, f Filter) (int64, error) {
	args := []any{}
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE(sum(d.jobs), 0)::bigint FROM report_days d WHERE d.kind = 't' AND `+f.where(&args, false), args...).Scan(&n)
	return n, err
}

// byJobs orders counts by jobs, most first, then by key.
func byJobs(c []Count) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].Jobs != c[j].Jobs {
			return c[i].Jobs > c[j].Jobs
		}
		return c[i].Key < c[j].Key
	})
}

// Whos is the jobs per alias ('a' || id) or capcode ('c' || capcode) the
// filter's jobs paged (R38b); the agency filter is the alias's own. keys, if
// given, narrows it to those.
func (s *Service) Whos(ctx context.Context, f Filter, keys []string) ([]Count, error) {
	return s.sum(ctx, "w", f, false, keys)
}

// Localities is the jobs per locality (empty: none), most first (R38c).
func (s *Service) Localities(ctx context.Context, f Filter) ([]Count, error) {
	c, err := s.sum(ctx, "l", f, false, nil)
	byJobs(c)
	return c, err
}

// Appliances is the jobs naming each appliance, most first (R38h).
func (s *Service) Appliances(ctx context.Context, f Filter) ([]Count, error) {
	c, err := s.sum(ctx, "a", f, false, nil)
	byJobs(c)
	return c, err
}

// Point is where a locality is drawn: the middle of its extent in the
// address dataset (R24).
type Point struct {
	Name     string
	Lon, Lat float64
}

// LocalityPoints are the points of the localities named, matched ignoring
// case.
func (s *Service) LocalityPoints(ctx context.Context, names []string) (map[string]Point, error) {
	upper := make([]string, len(names))
	for i, n := range names {
		upper[i] = strings.ToUpper(n)
	}
	rows, err := s.Pool.Query(ctx, `SELECT upper(name), (min_lon + max_lon) / 2, (min_lat + max_lat) / 2 FROM localities WHERE upper(name) = ANY($1::text[])`, upper)
	if err != nil {
		return nil, err
	}
	ps, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Point])
	if err != nil {
		return nil, err
	}
	out := map[string]Point{}
	for _, p := range ps {
		out[p.Name] = p
	}
	return out, nil
}

// Cell is the jobs that started in one hour (0-23) of one day of the week
// (1 Monday to 7 Sunday).
type Cell struct {
	Weekday, Hour int
	Jobs          int64
}

// Hours is the jobs by weekday and hour they started (R38d), of the whos
// given (aliases or capcodes: a job that paged several counts once), or of
// the filter's jobs when none are given.
func (s *Service) Hours(ctx context.Context, f Filter, whos []string) ([]Cell, error) {
	args := []any{}
	var sql string
	if len(whos) == 0 {
		sql = `SELECT extract(isodow FROM d.day)::int, d.key::int, sum(d.jobs)::bigint FROM report_days d WHERE d.kind = 'h' AND ` + f.where(&args, false) + ` GROUP BY 1, 2`
	} else {
		g := f
		g.Templates = nil // the alias's agency is its own
		args = append(args, whos)
		sql = `SELECT extract(isodow FROM d.day)::int, d.hour::int, count(DISTINCT d.job_key)::bigint FROM report_job_whos d WHERE d.who = ANY($1::text[]) AND ` + g.where(&args, false) + ` GROUP BY 1, 2`
	}
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Cell])
}

// MonthCount is one event type's jobs in one month.
type MonthCount struct {
	Month     time.Time
	EventType string
	Jobs      int64
}

// EventTypeMonths is the jobs per event type per calendar month (R38e),
// whatever the filter's event type.
func (s *Service) EventTypeMonths(ctx context.Context, f Filter) ([]MonthCount, error) {
	args := []any{}
	rows, err := s.Pool.Query(ctx, `SELECT date_trunc('month', d.day)::date, d.event_type, sum(d.jobs)::bigint FROM report_days d
		WHERE d.kind = 'h' AND `+f.where(&args, true)+` GROUP BY 1, 2 ORDER BY 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[MonthCount])
}

// SizeCount is how many jobs of an event type have a size: Kind 'p' pages,
// 'c' capcodes, 'n' appliances.
type SizeCount struct {
	Kind      string
	EventType string
	Size      int64
	Jobs      int64
}

// Sizes is the filter's jobs by their numbers of pages, capcodes and
// appliances, per event type (R38f).
func (s *Service) Sizes(ctx context.Context, f Filter) ([]SizeCount, error) {
	args := []any{}
	rows, err := s.Pool.Query(ctx, `SELECT d.kind, d.event_type, d.key::bigint, sum(d.jobs)::bigint FROM report_days d
		WHERE d.kind IN ('p', 'c', 'n') AND `+f.where(&args, false)+` GROUP BY 1, 2, 3`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SizeCount])
}

// Job is one job, for the long-running jobs (R38g).
type Job struct {
	JobNumber   string
	First, Last time.Time
	Pages       int64
	EventType   string
	Locality    string
}

// longJobsByDay is how long a range, in days, R38g reads by day: up to two
// years, the range's jobs are read from the day index (an index-only scan)
// and sorted; beyond that, the longest jobs overall are read in order until
// enough are in the range, as most of them are.
const longJobsByDay = 731

// LongJobs is the filter's n longest jobs, from first to last page: longest
// first, then the earlier start, then by job number. Only jobs with pages
// at two times or more.
func (s *Service) LongJobs(ctx context.Context, f Filter, n int) ([]Job, error) {
	args := []any{}
	w := f.where(&args, false)
	args = append(args, n)
	limit := "$" + strconv.Itoa(len(args))
	sql := `SELECT COALESCE(d.job_number, ''), d.first_at, d.last_at, d.pages::bigint, d.event_type, d.locality
		FROM report_jobs d WHERE d.last_at > d.first_at AND ` + w + `
		ORDER BY d.last_at - d.first_at DESC, d.first_at, d.job_key LIMIT ` + limit
	if f.Days() <= longJobsByDay {
		// The range's jobs, fenced off so the planner can't read the span index
		// instead, then the longest.
		sql = `WITH r AS MATERIALIZED (
			SELECT d.job_key, d.last_at - d.first_at AS span, d.first_at FROM report_jobs d
			WHERE d.last_at > d.first_at AND ` + w + `
		), top AS (SELECT * FROM r ORDER BY span DESC, first_at, job_key LIMIT ` + limit + `)
		SELECT COALESCE(j.job_number, ''), j.first_at, j.last_at, j.pages::bigint, j.event_type, j.locality
		FROM top JOIN report_jobs j USING (job_key) ORDER BY top.span DESC, top.first_at, top.job_key`
	}
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Job])
}

// Event is one large event (R87c), for R38i.
type Event struct {
	ID          int64
	First, Last time.Time
	Jobs        int64
}

// LargeEvents is the large events that started from one time to another,
// with their numbers of jobs (report_event_jobs, kept as the Large events
// page counts them).
func (s *Service) LargeEvents(ctx context.Context, from, to time.Time) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `SELECT e.id, e.first_at, e.last_at,
		COALESCE(j.jobs, 0)::bigint
		FROM large_events e LEFT JOIN report_event_jobs j ON j.event_id = e.id WHERE e.first_at >= $1 AND e.first_at < $2 ORDER BY e.first_at, e.id`, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Event])
}

// Alias is what a report shows of an alias.
type Alias struct {
	ID      int64
	Name    string
	Pattern string
	Agency  string
}

// Aliases are the aliases with these IDs.
func (s *Service) Aliases(ctx context.Context, ids []int64) (map[int64]Alias, error) {
	rows, err := s.Pool.Query(ctx, `SELECT a.id, a.name, a.pattern, COALESCE(t.agency, '') FROM aliases a LEFT JOIN templates t ON t.id = a.template_id WHERE a.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	as, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Alias])
	if err != nil {
		return nil, err
	}
	out := map[int64]Alias{}
	for _, a := range as {
		out[a.ID] = a
	}
	return out, nil
}

// Templates are the templates of an agency, by its name as written.
func (s *Service) Templates(ctx context.Context, agency string) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM templates WHERE agency = $1 ORDER BY id`, agency)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// Options are the values the filters offer: every agency, and every event
// type a counted job has.
type Options struct {
	Agencies   []string
	EventTypes []string
}

// OptionsFresh is how long the event types are kept before they're counted
// again: counting them reads every day's rows.
const OptionsFresh = 5 * time.Minute

// FilterOptions are the filters' values.
func (s *Service) FilterOptions(ctx context.Context) (Options, error) {
	var o Options
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT agency FROM templates ORDER BY 1`)
	if err != nil {
		return o, err
	}
	if o.Agencies, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return o, err
	}
	s.mu.Lock()
	kept, at := s.eventTypes, s.eventTypesAt
	s.mu.Unlock()
	if time.Since(at) < OptionsFresh && kept != nil {
		o.EventTypes = kept
		return o, nil
	}
	rows, err = s.Pool.Query(ctx, `SELECT DISTINCT event_type FROM report_days WHERE kind = 'p' AND event_type <> '' ORDER BY 1`)
	if err != nil {
		return o, err
	}
	if o.EventTypes, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return o, err
	}
	s.mu.Lock()
	s.eventTypes, s.eventTypesAt = o.EventTypes, time.Now()
	s.mu.Unlock()
	return o, nil
}

// ParseDay reads a YYYY-MM-DD day.
func ParseDay(s string) (time.Time, error) {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q isn't a date (YYYY-MM-DD)", s)
	}
	return d, nil
}
