package events

// Rebuilding large events after a re-parse (R23a, R87; owner, 2026-11-14;
// ADR 0027 §9). A re-parse can change which pages count (an alias added,
// changed, deleted or marked ignored), their jobs (a template's profile) or
// their codes (the network profile's pattern). The events around the pages
// it touched are then rebuilt, so they're what live ingest of the same pages,
// in the order they were sent, would have made. A rebuild never fires an
// integration (R83b's rule for pages that aren't live): Fire is never called.

import (
	"context"
	"slices"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Span is a stretch of sent time, both ends included.
type Span struct {
	From, To time.Time
}

func (s Span) extend(o Span) Span {
	return Span{From: earliest(s.From, o.From), To: latest(s.To, o.To)}
}

// Affected collects the sent times of re-parsed pages that events must be
// rebuilt around: one span per day, from that day's earliest such page to
// its latest, so it holds a few dozen bytes a day however many pages a
// re-parse touches.
type Affected struct {
	days map[int64]Span
}

// Add records a re-parsed page's sent time.
func (a *Affected) Add(t time.Time) {
	if a.days == nil {
		a.days = map[int64]Span{}
	}
	t = t.UTC()
	day := t.Unix() / 86400
	if s, ok := a.days[day]; ok {
		a.days[day] = s.extend(Span{t, t})
	} else {
		a.days[day] = Span{t, t}
	}
}

// Len is the number of days recorded.
func (a *Affected) Len() int { return len(a.days) }

// Spans are the days' spans, oldest first. Each is rebuilt in a batch of its
// own (Regrouper.Rebuild).
func (a *Affected) Spans() []Span {
	out := make([]Span, 0, len(a.days))
	for _, s := range a.days {
		out = append(out, s)
	}
	slices.SortFunc(out, func(x, y Span) int { return x.From.Compare(y.From) })
	return out
}

// Extents is what Bounds asks of the database.
type Extents interface {
	// EventsNear are the spans of the events pages sent in s could join
	// (R87a: within 12 hours of the event's first or last page).
	EventsNear(ctx context.Context, s Span) ([]Span, error)
	// Jobs is the span of every counting page of the jobs with a counting
	// page in s; ok is false when there's none.
	Jobs(ctx context.Context, s Span) (Span, bool, error)
}

// Bounds is the time a rebuild covers for pages re-parsed in s: s and 12
// hours either side, extended over every event that pages in it could join
// and over every job that has pages in it, again and again until it grows
// no more. Nothing outside it can join, or be joined by, an event of the
// pages inside, so rebuilding the pages inside alone gives what rebuilding
// everything would.
func Bounds(ctx context.Context, x Extents, s Span) (Span, error) {
	b := Span{From: s.From.Add(-Window), To: s.To.Add(Window)}
	for {
		next := b
		evs, err := x.EventsNear(ctx, next)
		if err != nil {
			return b, err
		}
		for _, e := range evs {
			next = next.extend(e)
		}
		j, ok, err := x.Jobs(ctx, next)
		if err != nil {
			return b, err
		}
		if ok {
			next = next.extend(j)
		}
		if next == b {
			return b, nil
		}
		b = next
	}
}

// Candidate is a page that counts towards events (Page.Counts), as a
// rebuild reads it.
type Candidate struct {
	ID    int64
	At    time.Time
	Job   string // "" for none
	Coded bool   // carries a code matching the network profile's pattern (R5)
}

// Grouped is an event a rebuild makes: its pages, oldest first, and the
// sent times of its first and last.
type Grouped struct {
	Pages       []int64
	First, Last time.Time
}

// Group replays pages through Detector.Stored's rules in the order they were
// sent, each judged by the pages sent before it (R87, R87a): a page with a
// code, or the page that takes its job to the threshold, opens an event or
// joins every event it's within 12 hours of, merging them; a page of a job
// in a near event joins it; and so do its job's pages in no event yet. It
// reads and writes nothing, so it's what live ingest of these pages would
// have made, whatever else is in the database.
func Group(pages []Candidate, threshold int) []Grouped {
	sorted := slices.Clone(pages)
	slices.SortFunc(sorted, func(a, b Candidate) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return cmpID(a.ID, b.ID)
	})
	type event struct {
		pages       []int64
		first, last time.Time
		gone        bool
	}
	var all, open []*event // open: those a page now could still join, oldest first
	in := map[int64]*event{}
	at := map[int64]time.Time{}
	jobs := map[string][]int64{}
	for _, p := range sorted {
		at[p.ID] = p.At
		if !p.Coded && p.Job == "" {
			continue
		}
		var job []int64
		if p.Job != "" {
			jobs[p.Job] = append(jobs[p.Job], p.ID)
			job = jobs[p.Job]
		}
		opens := p.Coded || (p.Job != "" && threshold >= 1 && len(job) >= threshold)

		// Pages come oldest first, so an event whose last page is more than
		// 12 hours before this one takes no more.
		open = slices.DeleteFunc(open, func(e *event) bool { return e.gone || e.last.Before(p.At.Add(-Window)) })
		near := slices.Clone(open)
		if !opens {
			near = slices.DeleteFunc(near, func(e *event) bool {
				return !slices.ContainsFunc(job, func(id int64) bool { return in[id] == e })
			})
			if len(near) == 0 {
				continue
			}
		}
		adds := []int64{p.ID}
		if p.Job != "" {
			adds = slices.DeleteFunc(slices.Clone(job), func(id int64) bool { return in[id] != nil })
		}
		var ev *event
		if len(near) == 0 {
			ev = &event{first: p.At, last: p.At}
			all = append(all, ev)
			open = append(open, ev)
		} else {
			ev = near[0]
			for _, o := range near[1:] {
				for _, id := range o.pages {
					in[id] = ev
				}
				ev.pages = append(ev.pages, o.pages...)
				ev.first, ev.last = earliest(ev.first, o.first), latest(ev.last, o.last)
				o.gone = true
			}
		}
		for _, id := range adds {
			in[id] = ev
			ev.pages = append(ev.pages, id)
			ev.first, ev.last = earliest(ev.first, at[id]), latest(ev.last, at[id])
		}
	}
	var out []Grouped
	for _, e := range all {
		if e.gone {
			continue
		}
		slices.SortFunc(e.pages, func(a, b int64) int {
			if c := at[a].Compare(at[b]); c != 0 {
				return c
			}
			return cmpID(a, b)
		})
		out = append(out, Grouped{Pages: e.pages, First: e.first, Last: e.last})
	}
	slices.SortStableFunc(out, func(a, b Grouped) int { return a.First.Compare(b.First) })
	return out
}

// Old is an event a rebuild replaces, with the pages it holds now.
type Old struct {
	ID    int64
	Pages []int64
}

// Assign gives each rebuilt group the ID of the old event that holds the
// most of its pages, each old ID kept by one group at most (ties go to the
// older event, then the earlier group); 0 means a new event. gone are the old
// IDs no group keeps: their events are deleted, and links to them find
// nothing (404), as an event merged away by live ingest does.
func Assign(groups []Grouped, old []Old) (ids []int64, gone []int64) {
	holder := map[int64]int64{}
	for _, o := range old {
		for _, p := range o.Pages {
			holder[p] = o.ID
		}
	}
	type pair struct {
		group int
		old   int64
		n     int
	}
	var pairs []pair
	for g, gr := range groups {
		n := map[int64]int{}
		for _, p := range gr.Pages {
			if id, ok := holder[p]; ok {
				n[id]++
			}
		}
		for id, c := range n {
			pairs = append(pairs, pair{g, id, c})
		}
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		if a.n != b.n {
			return b.n - a.n
		}
		if a.old != b.old {
			return cmpID(a.old, b.old)
		}
		return a.group - b.group
	})
	ids = make([]int64, len(groups))
	used := map[int64]bool{}
	for _, p := range pairs {
		if ids[p.group] != 0 || used[p.old] {
			continue
		}
		ids[p.group], used[p.old] = p.old, true
	}
	for _, o := range old {
		if !used[o.ID] {
			gone = append(gone, o.ID)
		}
	}
	return ids, gone
}

func cmpID(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Regrouper rebuilds large events in the database.
type Regrouper struct {
	Network *profile.Network
}

// Rebuilt says what a rebuild did. It never holds page text.
type Rebuilt struct {
	Batches int // spans rebuilt, each in its own transaction
	Pages   int // pages that count, read and grouped
	Events  int // events the rebuilt spans hold afterwards
	Created int // new events
	Deleted int // events that went
	Moved   int // pages put into, moved between or taken out of events
	// Longest is the longest batch's transaction, which holds the large
	// events lock (and so delays a live page that might open an event).
	Longest time.Duration
}

// Rebuild rebuilds the events around the affected pages, one batch per
// day, each in its own short transaction under the lock live detection
// takes (LockLargeEvents), so live pages wait for one batch at most and see
// either the old events or the new. Days near each other share part of
// their bounds; a later batch finds the earlier one's events and gives them
// again unchanged.
func (r *Regrouper) Rebuild(ctx context.Context, st *store.Store, a *Affected) (Rebuilt, error) {
	var out Rebuilt
	for _, s := range a.Spans() {
		start := time.Now()
		if err := st.InTx(ctx, func(q *db.Queries) error { return r.rebuild(ctx, q, s, &out) }); err != nil {
			return out, err
		}
		out.Batches++
		out.Longest = max(out.Longest, time.Since(start))
	}
	return out, nil
}

type dbExtents struct{ q *db.Queries }

func (x dbExtents) EventsNear(ctx context.Context, s Span) ([]Span, error) {
	rows, err := x.q.RegroupEventsNear(ctx, db.RegroupEventsNearParams{Lo: s.From, Hi: s.To})
	out := make([]Span, len(rows))
	for i, e := range rows {
		out[i] = Span{e.FirstAt, e.LastAt}
	}
	return out, err
}

func (x dbExtents) Jobs(ctx context.Context, s Span) (Span, bool, error) {
	j, err := x.q.RegroupJobsExtent(ctx, db.RegroupJobsExtentParams{Lo: s.From, Hi: s.To})
	if err != nil {
		return Span{}, false, err
	}
	return Span{j.FirstAt, j.LastAt}, true, nil
}

func (r *Regrouper) rebuild(ctx context.Context, q *db.Queries, s Span, out *Rebuilt) error {
	if err := q.LockLargeEvents(ctx); err != nil {
		return err
	}
	b, err := Bounds(ctx, dbExtents{q}, s)
	if err != nil {
		return err
	}
	threshold, err := Threshold(ctx, q)
	if err != nil {
		return err
	}
	rows, err := q.RegroupPages(ctx, db.RegroupPagesParams{Lo: b.From, Hi: b.To})
	if err != nil {
		return err
	}
	cands := make([]Candidate, 0, len(rows))
	for _, m := range rows {
		c := Candidate{ID: m.ID, At: m.SentAt, Coded: HasCode(r.Network, m.Text)}
		if m.JobNumber != nil {
			c.Job = *m.JobNumber
		}
		cands = append(cands, c)
	}
	out.Pages += len(cands)
	groups := Group(cands, threshold)

	// The events now within the bounds, all of them inside it (Bounds took
	// in every event its pages could join), and the pages they hold.
	near, err := q.RegroupEventsNear(ctx, db.RegroupEventsNearParams{Lo: b.From, Hi: b.To})
	if err != nil {
		return err
	}
	oldIDs := make([]int64, len(near))
	for i, e := range near {
		oldIDs[i] = e.ID
	}
	members, err := q.RegroupMembers(ctx, oldIDs)
	if err != nil {
		return err
	}
	was := map[int64]int64{} // page -> event now
	byEvent := map[int64][]int64{}
	for _, m := range members {
		was[m.MessageID] = m.EventID
		byEvent[m.EventID] = append(byEvent[m.EventID], m.MessageID)
	}
	old := make([]Old, len(oldIDs))
	for i, id := range oldIDs {
		old[i] = Old{ID: id, Pages: byEvent[id]}
	}
	ids, gone := Assign(groups, old)

	// New events never fire (R87d's integrations hear only of events live
	// ingest opens): CreateLargeEvent is called here without Fire.
	spans := map[int64]Span{}
	for _, e := range near {
		spans[e.ID] = Span{e.FirstAt, e.LastAt}
	}
	will := map[int64]int64{}
	for i, g := range groups {
		if ids[i] == 0 {
			ev, err := q.CreateLargeEvent(ctx, g.First)
			if err != nil {
				return err
			}
			ids[i] = ev.ID
			out.Created++
		}
		if was, ok := spans[ids[i]]; !ok || !was.From.Equal(g.First) || !was.To.Equal(g.Last) {
			if err := q.PutLargeEventSpan(ctx, db.PutLargeEventSpanParams{ID: ids[i], FirstAt: g.First, LastAt: g.Last}); err != nil {
				return err
			}
		}
		for _, p := range g.Pages {
			will[p] = ids[i]
		}
	}
	var putPages, putEvents, removed []int64
	for p, ev := range will {
		if was[p] != ev {
			putPages, putEvents = append(putPages, p), append(putEvents, ev)
		}
	}
	for p := range was {
		if _, ok := will[p]; !ok {
			removed = append(removed, p)
		}
	}
	if len(putPages) > 0 {
		if err := q.PutLargeEventPages(ctx, db.PutLargeEventPagesParams{MessageIds: putPages, EventIds: putEvents}); err != nil {
			return err
		}
	}
	if len(removed) > 0 {
		if err := q.RemoveLargeEventPages(ctx, removed); err != nil {
			return err
		}
	}
	out.Moved += len(putPages) + len(removed)
	if len(putPages)+len(removed)+len(gone) > 0 {
		if _, err := q.RehomeEventDeliveries(ctx, db.RehomeEventDeliveriesParams{EventIds: oldIDs, Pages: putPages}); err != nil {
			return err
		}
	}
	if len(gone) > 0 {
		if err := q.DeleteLargeEvents(ctx, gone); err != nil {
			return err
		}
	}
	out.Deleted += len(gone)
	out.Events += len(groups)
	return nil
}
