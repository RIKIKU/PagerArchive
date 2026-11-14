package events

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// Rebuilding events after a re-parse (owner, 2026-11-14; ADR 0027 §9):
// what a rebuild covers, how it groups, and which IDs it keeps.

func TestAffectedSpans(t *testing.T) {
	var a Affected
	if a.Len() != 0 || len(a.Spans()) != 0 {
		t.Fatal("an empty set has spans")
	}
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{20 * time.Hour, 3 * time.Hour, 9 * time.Hour, 50 * time.Hour, 26 * time.Hour} {
		a.Add(day.Add(d))
	}
	// The same instant in another zone is the same day.
	a.Add(day.Add(4 * time.Hour).In(time.FixedZone("+11", 11*3600)))
	want := []Span{
		{day.Add(3 * time.Hour), day.Add(20 * time.Hour)},
		{day.Add(26 * time.Hour), day.Add(26 * time.Hour)},
		{day.Add(50 * time.Hour), day.Add(50 * time.Hour)},
	}
	got := a.Spans()
	if len(got) != len(want) {
		t.Fatalf("spans %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].From.Equal(want[i].From) || !got[i].To.Equal(want[i].To) {
			t.Errorf("span %d: %v, want %v", i, got[i], want[i])
		}
	}
}

// fakeExtents is a database of event spans and job spans for Bounds.
type fakeExtents struct {
	events []Span
	jobs   []Span // each job's first and last counting pages
	calls  int
}

func (f *fakeExtents) EventsNear(_ context.Context, s Span) ([]Span, error) {
	f.calls++
	var out []Span
	for _, e := range f.events {
		if !e.To.Before(s.From.Add(-Window)) && !e.From.After(s.To.Add(Window)) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeExtents) Jobs(_ context.Context, s Span) (Span, bool, error) {
	var out Span
	ok := false
	for _, j := range f.jobs {
		if j.To.Before(s.From) || j.From.After(s.To) {
			continue // no page of this job is in s (a job's pages are its two ends here)
		}
		if !ok {
			out, ok = j, true
		}
		out = out.extend(j)
	}
	return out, ok, nil
}

func span(from, to time.Duration) Span { return Span{at(from), at(to)} }

func TestBounds(t *testing.T) {
	h := time.Hour
	for _, c := range []struct {
		name   string
		x      fakeExtents
		in     Span
		want   Span
		maxRun int
	}{
		{"nothing near: the pages and 12 hours either side", fakeExtents{}, span(0, 2*h), span(-12*h, 14*h), 2},
		{"an event the edge's pages could join", fakeExtents{events: []Span{span(-30*h, -20*h)}}, span(0, 0), span(-30*h, 12*h), 3},
		{"an event more than 12 hours past the edge stays out", fakeExtents{events: []Span{span(-40*h, -24*h-time.Minute)}}, span(0, 0), span(-12*h, 12*h), 2},
		{"events chain", fakeExtents{events: []Span{span(-30*h, -20*h), span(-50*h, -40*h), span(-80*h, -70*h)}}, span(0, 0), span(-50*h, 12*h), 4},
		{"a job's pages, whenever they were sent", fakeExtents{jobs: []Span{span(-3*24*h, 1*h)}}, span(0, 0), span(-72*h, 12*h), 3},
		{"a job, then an event near its first page, then that event's job",
			fakeExtents{jobs: []Span{span(-40*h, 0), span(-70*h, -55*h)}, events: []Span{span(-60*h, -50*h)}}, span(5*h, 5*h), span(-70*h, 17*h), 5},
	} {
		x := c.x
		got, err := Bounds(context.Background(), &x, c.in)
		if err != nil {
			t.Fatal(err)
		}
		if !got.From.Equal(c.want.From) || !got.To.Equal(c.want.To) {
			t.Errorf("%s: %v to %v, want %v to %v", c.name, got.From.Sub(t0), got.To.Sub(t0), c.want.From.Sub(t0), c.want.To.Sub(t0))
		}
		if x.calls > c.maxRun {
			t.Errorf("%s: %d rounds, want at most %d", c.name, x.calls, c.maxRun)
		}
	}
}

func coded(id int64, d time.Duration, job string) Candidate {
	return Candidate{ID: id, At: at(d), Job: job, Coded: true}
}

func plain(id int64, d time.Duration, job string) Candidate {
	return Candidate{ID: id, At: at(d), Job: job}
}

func show(gs []Grouped) string {
	s := ""
	for _, g := range gs {
		s += fmt.Sprintf("[%v %v-%v]", g.Pages, g.First.Sub(t0), g.Last.Sub(t0))
	}
	return s
}

func TestGroup(t *testing.T) {
	h, m := time.Hour, time.Minute
	var job19, job20 []Candidate
	for i := range 20 {
		p := plain(int64(100+i), time.Duration(i)*m, "EX0000201")
		if i < 19 {
			job19 = append(job19, p)
		}
		job20 = append(job20, p)
	}
	ids := func(from, n int) []int64 {
		var out []int64
		for i := range n {
			out = append(out, int64(from+i))
		}
		return out
	}
	for _, c := range []struct {
		name      string
		pages     []Candidate
		threshold int
		want      []Grouped
	}{
		{"A84: a code opens, a code within 12 hours joins, 13 quiet hours end it",
			[]Candidate{coded(1, 0, "EX0000101"), coded(2, 5*h, "EX0000102"), coded(3, 18*h+m, "EX0000103")}, 20,
			[]Grouped{{[]int64{1, 2}, at(0), at(5 * h)}, {[]int64{3}, at(18*h + m), at(18*h + m)}}},
		{"A85: 19 Emergency pages open nothing", job19, 20, nil},
		{"A85: the 20th opens an event of the job's 20", job20, 20, []Grouped{{ids(100, 20), at(0), at(19 * m)}}},
		{"A85: at 25, 20 pages open nothing", job20, 25, nil},
		{"a job's pages join its event, before and after; another job's don't",
			[]Candidate{plain(1, 0, "J1"), coded(2, 10*m, "J1"), plain(3, h, "J1"), plain(4, h, "J2"), plain(5, 30*h, "J1")}, 20,
			[]Grouped{{[]int64{1, 2, 3}, at(0), at(h)}}},
		{"a page with no job and no code is never grouped", []Candidate{plain(1, 0, ""), coded(2, m, "")}, 20,
			[]Grouped{{[]int64{2}, at(m), at(m)}}},
		{"in sent order a page between two codes joins the first, and the second joins it",
			[]Candidate{coded(1, 0, ""), coded(2, 20*h, ""), coded(3, 10*h, "")}, 20,
			[]Grouped{{[]int64{1, 3, 2}, at(0), at(20 * h)}}},
	} {
		if got := Group(c.pages, c.threshold); show(got) != show(c.want) {
			t.Errorf("%s:\n got %s\nwant %s", c.name, show(got), show(c.want))
		}
	}
}

// Group judges pages in sent order, whatever order it's given them in, as
// live ingest of pages in order does (TestLargeEventsOutOfOrder's pages).
func TestGroupOrder(t *testing.T) {
	h := time.Hour
	pages := []Candidate{coded(1, 0, "A"), coded(2, 5*h, "B"), coded(3, 12*h, "C"), coded(4, 20*h, "D"), coded(5, -3*h, "E"), coded(6, 40*h, "F")}
	want := "[[5 1 2 3 4] -3h0m0s-20h0m0s][[6] 40h0m0s-40h0m0s]"
	for _, order := range [][]int{{0, 1, 2, 3, 4, 5}, {5, 3, 2, 1, 0, 4}, {3, 0, 4, 5, 1, 2}} {
		var in []Candidate
		for _, i := range order {
			in = append(in, pages[i])
		}
		if got := show(Group(in, 20)); got != want {
			t.Errorf("order %v: %s, want %s", order, got, want)
		}
	}
}

func TestAssign(t *testing.T) {
	g := func(pages ...int64) Grouped { return Grouped{Pages: pages} }
	for _, c := range []struct {
		name     string
		groups   []Grouped
		old      []Old
		wantIDs  []int64
		wantGone []int64
	}{
		{"unchanged keeps its ID", []Grouped{g(1, 2, 3)}, []Old{{7, []int64{1, 2, 3}}}, []int64{7}, nil},
		{"pages added keep it too", []Grouped{g(1, 2, 3, 4)}, []Old{{7, []int64{2, 3}}}, []int64{7}, nil},
		{"a new event", []Grouped{g(1, 2)}, nil, []int64{0}, nil},
		{"an event left with no pages goes", nil, []Old{{7, []int64{1}}}, []int64{}, []int64{7}},
		{"two merged: the one holding more keeps its ID",
			[]Grouped{g(1, 2, 3, 4, 5)}, []Old{{7, []int64{1}}, {8, []int64{2, 3, 4}}}, []int64{8}, []int64{7}},
		{"a tie goes to the older", []Grouped{g(1, 2)}, []Old{{8, []int64{2}}, {7, []int64{1}}}, []int64{7}, []int64{8}},
		{"split: the larger part keeps it, the other is new",
			[]Grouped{g(1), g(2, 3)}, []Old{{7, []int64{1, 2, 3}}}, []int64{0, 7}, nil},
	} {
		ids, gone := Assign(c.groups, c.old)
		if !slices.Equal(ids, c.wantIDs) || !slices.Equal(gone, c.wantGone) {
			t.Errorf("%s: ids %v gone %v, want %v and %v", c.name, ids, gone, c.wantIDs, c.wantGone)
		}
	}
}
