// Package events notices large events (R87-R87e, ADR 0027): Emergency pages
// to aliased capcodes that carry a deployment code, or belong to a job that
// has named enough distinct units, open an event; pages within 12 hours of
// one join it, with the other pages of their jobs.
//
// Detection runs in the ingest transaction of each new page (not a duplicate
// copy), so an event and the deliveries it fires commit with the page that
// made them. Page text is read to find deployment codes and never logged.
package events

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Level is a condition an event has met (R87b).
type Level string

const (
	Deployment         Level = "deployment"          // one deployment code
	SeveralDeployments Level = "several_deployments" // 2 or more distinct codes
	BigJob             Level = "big_job"             // a job with enough distinct units
)

// Levels, in the order the app shows them.
var Levels = []Level{Deployment, SeveralDeployments, BigJob}

// Label is a level's name in the app and in deliveries.
func (l Level) Label() string {
	switch l {
	case Deployment:
		return "Deployment"
	case SeveralDeployments:
		return "Several deployments"
	case BigJob:
		return "Big job"
	}
	return string(l)
}

// Valid reports whether l is one of Levels.
func (l Level) Valid() bool { return slices.Contains(Levels, l) }

// Window joins a page to an event, and ends an event with no page (R87a).
const Window = 12 * time.Hour

// The big-job threshold (R87): distinct units on one job. Admins set it.
const (
	DefaultUnits = 20
	MinUnits     = 2
	MaxUnits     = 1000
	unitsKey     = "large_event_units"
)

// Units is the big-job threshold now.
func Units(ctx context.Context, q *db.Queries) (int, error) {
	v, err := q.GetSetting(ctx, unitsKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultUnits, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < MinUnits || n > MaxUnits {
		return DefaultUnits, nil
	}
	return n, nil
}

// ErrUnits is a threshold out of range.
var ErrUnits = fmt.Errorf("the number of units must be a whole number from %d to %d", MinUnits, MaxUnits)

// SetUnits changes the big-job threshold. Events already open keep their
// levels; new pages are judged by the new number (A85).
func SetUnits(ctx context.Context, q *db.Queries, n int) error {
	if n < MinUnits || n > MaxUnits {
		return ErrUnits
	}
	return q.SetSetting(ctx, db.SetSettingParams{Key: unitsKey, Value: strconv.Itoa(n)})
}

// Ongoing reports whether an event whose last page was sent at last is still
// open at now: no 12 hours have passed without a page joining it (R87a).
func Ongoing(last, now time.Time) bool { return !now.After(last.Add(Window)) }

// Page is a newly stored page, as ingest stored it.
type Page struct {
	ID        int64
	SentAt    time.Time
	Text      string // stored text, marker included; never logged
	Priority  *string
	AliasID   *int64
	Heartbeat bool
	Job       *string
}

// Counts reports whether a page counts towards events (R87): Emergency, to a
// capcode with an alias, not a heartbeat.
func (p Page) Counts() bool {
	return p.Priority != nil && *p.Priority == string(profile.Emergency) && p.AliasID != nil && !p.Heartbeat
}

// Fire queues the deliveries of an event that has just reached a level, for
// the page that took it there, in the ingest transaction q (R87d).
type Fire func(ctx context.Context, q *db.Queries, eventID int64, level Level, messageID int64, sentAt time.Time) error

// Detector finds large events as pages are stored.
type Detector struct {
	Network *profile.Network
	Fire    Fire // nil fires nothing
}

// Stored groups a newly stored page into a large event, if it opens or joins
// one (R87, R87a), records the levels it meets (R87b), and fires each level
// the event reaches for the first time (R87d). It runs in the ingest
// transaction q. A page that doesn't count, with no deployment code and no
// job, costs nothing: no query is made for it.
func (d *Detector) Stored(ctx context.Context, q *db.Queries, p Page) error {
	if d == nil || !p.Counts() {
		return nil
	}
	codes := codesIn(d.Network, p.Text)
	if len(codes) == 0 && p.Job == nil {
		return nil
	}
	if err := q.LockLargeEvents(ctx); err != nil {
		return err
	}
	var job []db.JobEventPagesRow
	var crossed *time.Time
	if p.Job != nil {
		var err error
		if job, err = q.JobEventPages(ctx, *p.Job); err != nil {
			return err
		}
		threshold, err := Units(ctx, q)
		if err != nil {
			return err
		}
		crossed = Crossing(job, threshold)
	}
	opens := len(codes) > 0 || crossed != nil
	near, err := q.LargeEventsNear(ctx, p.SentAt)
	if err != nil {
		return err
	}
	if !opens {
		// Not a page that opens an event: it joins only an event within 12
		// hours that holds its job (R87a: "and so do the other pages of its
		// job").
		held := map[int64]bool{}
		for _, j := range job {
			if j.EventID != nil {
				held[*j.EventID] = true
			}
		}
		near = slices.DeleteFunc(near, func(e db.LargeEvent) bool { return !held[e.ID] })
		if len(near) == 0 {
			return nil
		}
	}

	// The pages that join: this one, and its job's pages in no event yet.
	adds := joining(d.Network, p, codes, job)
	if len(adds) == 0 {
		return nil
	}

	// The event: the oldest it's near, with any others merged into it, or a
	// new one.
	var ev db.LargeEvent
	before := levels{}
	ids := map[int64]bool{}
	if len(near) == 0 {
		if ev, err = q.CreateLargeEvent(ctx, p.SentAt); err != nil {
			return err
		}
	} else {
		ev = near[0]
		var others []int64
		for _, e := range near {
			ids[e.ID] = true
			before = before.with(levelsOf(e))
			if e.ID != ev.ID {
				others = append(others, e.ID)
			}
		}
		if len(others) > 0 {
			if err := q.MergeLargeEvents(ctx, db.MergeLargeEventsParams{ID: ev.ID, Others: others}); err != nil {
				return err
			}
			if err := q.DeleteLargeEvents(ctx, others); err != nil {
				return err
			}
		}
	}
	ids[ev.ID] = true

	set := db.SetLargeEventParams{ID: ev.ID, FirstAt: adds[0].at, LastAt: adds[0].at}
	coded := len(near) > 1
	for _, a := range adds {
		if err := q.AddLargeEventPage(ctx, db.AddLargeEventPageParams{MessageID: a.id, EventID: ev.ID, DeploymentCodes: a.codes}); err != nil {
			return err
		}
		set.FirstAt, set.LastAt = earliest(set.FirstAt, a.at), latest(set.LastAt, a.at)
		if len(a.codes) > 0 {
			coded = true
			t := a.at
			if set.DeploymentAt == nil || t.Before(*set.DeploymentAt) {
				set.DeploymentAt = &t
			}
		}
	}
	if coded {
		times, err := q.LargeEventCodeTimes(ctx, ev.ID)
		if err != nil {
			return err
		}
		set.SeveralDeploymentsAt = SeveralAt(times)
	}
	if crossed != nil {
		// Big job: when the job reached the threshold, or when its first page
		// in this event was sent, if that's later (a job that grew big in an
		// earlier event).
		first := time.Time{}
		added := map[int64]bool{}
		for _, a := range adds {
			added[a.id] = true
		}
		for _, j := range job {
			if added[j.ID] || (j.EventID != nil && ids[*j.EventID]) {
				first = j.SentAt
				break
			}
		}
		t := latest(*crossed, first)
		set.BigJobAt = &t
	}
	if err := q.SetLargeEvent(ctx, set); err != nil {
		return err
	}
	after, err := q.LargeEventByID(ctx, ev.ID)
	if err != nil {
		return err
	}
	if d.Fire == nil {
		return nil
	}
	for _, l := range before.reached(levelsOf(after)) {
		if err := d.Fire(ctx, q, ev.ID, l, p.ID, p.SentAt); err != nil {
			return err
		}
	}
	return nil
}

type add struct {
	id    int64
	at    time.Time
	codes []string
}

// joining is the pages that join an event with p: p itself and, when it has
// a job, the job's counting pages in no event yet (oldest first). Pages
// already in an event stay where they are, so a page is in one event at most.
func joining(n *profile.Network, p Page, codes []string, job []db.JobEventPagesRow) []add {
	own := add{id: p.ID, at: p.SentAt, codes: orEmpty(codes)}
	if p.Job == nil {
		return []add{own}
	}
	var out []add
	seen := false
	for _, j := range job {
		if j.EventID != nil {
			continue
		}
		if j.ID == p.ID {
			seen = true
			out = append(out, own)
			continue
		}
		out = append(out, add{id: j.ID, at: j.SentAt, codes: orEmpty(codesIn(n, j.Text))})
	}
	if !seen {
		out = append(out, own)
		slices.SortStableFunc(out, func(a, b add) int { return a.at.Compare(b.at) })
	}
	return out
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// codesIn finds an Emergency page's deployment codes in its stored text.
func codesIn(n *profile.Network, text string) []string {
	return pipeline.DeploymentCodes(n, pipeline.Prepared{Text: text, Priority: profile.Emergency})
}

// Crossing is when a job's counting pages, oldest first, first named
// threshold distinct units: the sent time of the page that named the last
// of them. nil if they haven't (R87's big job).
func Crossing(job []db.JobEventPagesRow, threshold int) *time.Time {
	seen := map[string]bool{}
	for _, j := range job {
		for _, u := range j.Units {
			seen[u] = true
		}
		if len(seen) >= threshold {
			t := j.SentAt
			return &t
		}
	}
	return nil
}

// SeveralAt is when an event's pages first carried 2 distinct deployment
// codes: the first page of its second code. times is each code with its first
// page's time, earliest first.
func SeveralAt(times []db.LargeEventCodeTimesRow) *time.Time {
	if len(times) < 2 {
		return nil
	}
	t := times[1].FirstAt
	return &t
}

// levels are when an event met each level; nil for not yet.
type levels map[Level]*time.Time

func levelsOf(e db.LargeEvent) levels {
	return levels{Deployment: e.DeploymentAt, SeveralDeployments: e.SeveralDeploymentsAt, BigJob: e.BigJobAt}
}

// with is the levels of two events merged: each met when either met it.
func (l levels) with(o levels) levels {
	out := levels{}
	for _, k := range Levels {
		a, b := l[k], o[k]
		switch {
		case a == nil:
			out[k] = b
		case b == nil:
			out[k] = a
		default:
			t := earliest(*a, *b)
			out[k] = &t
		}
	}
	return out
}

// reached is the levels after has met that l hadn't: the ones to fire.
func (l levels) reached(after levels) []Level {
	var out []Level
	for _, k := range Levels {
		if l[k] == nil && after[k] != nil {
			out = append(out, k)
		}
	}
	return out
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
