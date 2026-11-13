// Package events notices large events (R87-R87e, ADR 0027): an Emergency
// page to an aliased capcode that carries a code matching the network
// profile's pattern (R5), or a job that has enough of them, opens an event;
// pages within 12 hours of one join it, with the other pages of their jobs.
// An event is simply a large event: it has no levels (owner, 2026-11-13).
//
// Detection runs in the ingest transaction of each new page (not a duplicate
// copy), so an event and the deliveries it fires commit with the page that
// made them. Page text is read to find the codes and never logged.
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

// Window joins a page to an event, and ends an event with no page (R87a).
const Window = 12 * time.Hour

// The job threshold (R87): Emergency pages on one job that count
// (Page.Counts), each stored message once. Admins set it. The setting's key
// is still large_event_units, from when it counted distinct units (owner,
// 2026-11-13): a value stored then is in the same range, so it stays valid.
const (
	DefaultPages = 20
	MinPages     = 2
	MaxPages     = 1000
	thresholdKey = "large_event_units"
)

// Threshold is the job threshold now.
func Threshold(ctx context.Context, q *db.Queries) (int, error) {
	v, err := q.GetSetting(ctx, thresholdKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultPages, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < MinPages || n > MaxPages {
		return DefaultPages, nil
	}
	return n, nil
}

// ErrThreshold is a threshold out of range.
var ErrThreshold = fmt.Errorf("the number of Emergency pages must be a whole number from %d to %d", MinPages, MaxPages)

// SetThreshold changes the job threshold. Events already recorded stay as
// they are; new pages are judged by the new number (A85).
func SetThreshold(ctx context.Context, q *db.Queries, n int) error {
	if n < MinPages || n > MaxPages {
		return ErrThreshold
	}
	return q.SetSetting(ctx, db.SetSettingParams{Key: thresholdKey, Value: strconv.Itoa(n)})
}

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

// Fire queues the deliveries of an event that has just opened, carried by
// the page that opened it, in the ingest transaction q (R87d).
type Fire func(ctx context.Context, q *db.Queries, eventID, messageID int64, sentAt time.Time) error

// Detector finds large events as pages are stored.
type Detector struct {
	Network *profile.Network
	Fire    Fire // nil fires nothing
}

// Stored groups a newly stored page into a large event, if it opens or joins
// one (R87, R87a), and fires the event's integrations when the page opens it
// (R87d): only then, so each integration is told of an event once. A page
// joining an event, or merging two, fires nothing. It runs in the ingest
// transaction q. A page that doesn't count, with no code and no job, costs
// nothing: no query is made for it.
func (d *Detector) Stored(ctx context.Context, q *db.Queries, p Page) error {
	if d == nil || !p.Counts() {
		return nil
	}
	coded := HasCode(d.Network, p.Text)
	if !coded && p.Job == nil {
		return nil
	}
	if err := q.LockLargeEvents(ctx); err != nil {
		return err
	}
	var job []db.JobEventPagesRow
	large := false
	if p.Job != nil {
		var err error
		if job, err = q.JobEventPages(ctx, *p.Job); err != nil {
			return err
		}
		threshold, err := Threshold(ctx, q)
		if err != nil {
			return err
		}
		large = Reached(job, threshold)
	}
	opens := coded || large
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
	adds := joining(p, job)
	if len(adds) == 0 {
		return nil
	}

	// The event: the oldest it's near, with any others merged into it, or a
	// new one.
	var ev db.LargeEvent
	opened := len(near) == 0
	if opened {
		if ev, err = q.CreateLargeEvent(ctx, p.SentAt); err != nil {
			return err
		}
	} else {
		ev = near[0]
		var others []int64
		for _, e := range near[1:] {
			others = append(others, e.ID)
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

	span := db.SetLargeEventSpanParams{ID: ev.ID, FirstAt: adds[0].at, LastAt: adds[0].at}
	for _, a := range adds {
		if err := q.AddLargeEventPage(ctx, db.AddLargeEventPageParams{MessageID: a.id, EventID: ev.ID}); err != nil {
			return err
		}
		span.FirstAt, span.LastAt = earliest(span.FirstAt, a.at), latest(span.LastAt, a.at)
	}
	if err := q.SetLargeEventSpan(ctx, span); err != nil {
		return err
	}
	if opened && d.Fire != nil {
		return d.Fire(ctx, q, ev.ID, p.ID, p.SentAt)
	}
	return nil
}

type add struct {
	id int64
	at time.Time
}

// joining is the pages that join an event with p: p itself and, when it has
// a job, the job's counting pages in no event yet (oldest first). Pages
// already in an event stay where they are, so a page is in one event at most.
func joining(p Page, job []db.JobEventPagesRow) []add {
	own := add{id: p.ID, at: p.SentAt}
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
		}
		out = append(out, add{id: j.ID, at: j.SentAt})
	}
	if !seen {
		out = append(out, own)
		slices.SortStableFunc(out, func(a, b add) int { return a.at.Compare(b.at) })
	}
	return out
}

// HasCode reports whether an Emergency page's stored text carries a code
// matching the network profile's deployment_code_pattern (R5), which opens a
// large event.
func HasCode(n *profile.Network, text string) bool {
	return len(pipeline.DeploymentCodes(n, pipeline.Prepared{Text: text, Priority: profile.Emergency})) > 0
}

// Reached reports whether a job has the threshold's number of Emergency
// pages, which opens a large event (R87). job is the job's stored messages
// that count (Emergency, to an aliased capcode, not a heartbeat), one row per
// message: a copy from another receiver is the same message (R19), so it has
// no row of its own and counts once.
func Reached(job []db.JobEventPagesRow, threshold int) bool {
	return threshold >= 1 && len(job) >= threshold
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
