package events

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

var t0 = time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func ptr[T any](v T) *T { return &v }

func jobPage(id int64, d time.Duration, units ...string) db.JobEventPagesRow {
	return db.JobEventPagesRow{ID: id, SentAt: at(d), Units: units}
}

func TestCrossing(t *testing.T) {
	job := []db.JobEventPagesRow{
		jobPage(1, 0, "T1", "T2"),
		jobPage(2, time.Minute, "T2", "T3"), // T2 again: still 3
		jobPage(3, 2*time.Minute),           // no units
		jobPage(4, 3*time.Minute, "T4"),
	}
	for _, c := range []struct {
		threshold int
		want      *time.Time
	}{
		{2, ptr(at(0))},
		{3, ptr(at(time.Minute))},
		{4, ptr(at(3 * time.Minute))},
		{5, nil},
	} {
		got := Crossing(job, c.threshold)
		if (got == nil) != (c.want == nil) || (got != nil && !got.Equal(*c.want)) {
			t.Errorf("threshold %d: %v, want %v", c.threshold, got, c.want)
		}
	}
	// Distinct by the literal text (P3): t1 isn't T1.
	if got := Crossing([]db.JobEventPagesRow{jobPage(1, 0, "T1", "t1")}, 2); got == nil {
		t.Error("T1 and t1 counted as one unit")
	}
	if Crossing(nil, DefaultUnits) != nil {
		t.Error("no pages crossed")
	}
}

// A85's numbers: 19 distinct units don't make a big job; the 20th does.
func TestCrossingAtTwenty(t *testing.T) {
	var job []db.JobEventPagesRow
	for i := range 19 {
		job = append(job, jobPage(int64(i+1), time.Duration(i)*time.Minute, fmt.Sprintf("U%02d", i+1), "U01"))
	}
	if Crossing(job, DefaultUnits) != nil {
		t.Fatal("19 units crossed 20")
	}
	job = append(job, jobPage(20, time.Hour, "U20"))
	if got := Crossing(job, DefaultUnits); got == nil || !got.Equal(at(time.Hour)) {
		t.Fatalf("the 20th unit: %v, want %v", got, at(time.Hour))
	}
	if Crossing(job, 25) != nil {
		t.Error("20 units crossed 25")
	}
}

func TestSeveralAt(t *testing.T) {
	if SeveralAt(nil) != nil || SeveralAt([]db.LargeEventCodeTimesRow{{Code: "DG0801", FirstAt: at(0)}}) != nil {
		t.Error("one code is several")
	}
	got := SeveralAt([]db.LargeEventCodeTimesRow{{Code: "DG0801", FirstAt: at(0)}, {Code: "DG0802", FirstAt: at(5 * time.Hour)}, {Code: "DG0803", FirstAt: at(6 * time.Hour)}})
	if got == nil || !got.Equal(at(5*time.Hour)) {
		t.Errorf("several at %v, want the second code's first page", got)
	}
}

func TestLevelsReachedOnce(t *testing.T) {
	none := levels{}
	dep := levels{Deployment: ptr(at(0))}
	both := levels{Deployment: ptr(at(0)), SeveralDeployments: ptr(at(5 * time.Hour))}
	if got := none.reached(dep); !slices.Equal(got, []Level{Deployment}) {
		t.Errorf("new event: %v", got)
	}
	if got := dep.reached(both); !slices.Equal(got, []Level{SeveralDeployments}) {
		t.Errorf("second code: %v", got)
	}
	// A level met earlier (a late page) isn't reached again.
	if got := both.reached(levels{Deployment: ptr(at(-time.Hour)), SeveralDeployments: ptr(at(time.Hour))}); len(got) != 0 {
		t.Errorf("earlier times fired %v", got)
	}
	// Merging: each level met when either event met it, and nothing that
	// either had fires again.
	a := levels{Deployment: ptr(at(time.Hour))}
	b := levels{Deployment: ptr(at(0)), BigJob: ptr(at(2 * time.Hour))}
	m := a.with(b)
	if !m[Deployment].Equal(at(0)) || !m[BigJob].Equal(at(2*time.Hour)) || m[SeveralDeployments] != nil {
		t.Errorf("merged %v", m)
	}
	if got := m.reached(levels{Deployment: ptr(at(0)), BigJob: ptr(at(2 * time.Hour)), SeveralDeployments: ptr(at(3 * time.Hour))}); !slices.Equal(got, []Level{SeveralDeployments}) {
		t.Errorf("after a merge: %v", got)
	}
}

func TestOngoing(t *testing.T) {
	last := at(5 * time.Hour) // 15:00
	if !Ongoing(last, at(17*time.Hour)) {
		t.Error("ended at exactly 12 hours")
	}
	if Ongoing(last, at(17*time.Hour+time.Minute)) {
		t.Error("ongoing at 03:01 the next day (A84)")
	}
}

func network(t *testing.T) *profile.Network {
	t.Helper()
	set, err := profile.LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	return set.Networks["example-network"]
}

func TestCounts(t *testing.T) {
	em, ne := "Emergency", "Non-emergency"
	alias := int64(1)
	for _, c := range []struct {
		name string
		p    Page
		want bool
	}{
		{"Emergency, aliased", Page{Priority: &em, AliasID: &alias}, true},
		{"no alias", Page{Priority: &em}, false},
		{"Non-emergency", Page{Priority: &ne, AliasID: &alias}, false},
		{"no priority", Page{AliasID: &alias}, false},
		{"heartbeat", Page{Priority: &em, AliasID: &alias, Heartbeat: true}, false},
	} {
		if c.p.Counts() != c.want {
			t.Errorf("%s: counts %v", c.name, !c.want)
		}
	}
}

// A page that can't open or join an event makes no query: the detector is
// given no database at all here, and would panic if it made one.
func TestStoredSkipsWithoutQueries(t *testing.T) {
	d := &Detector{Network: network(t)}
	em, ne := "Emergency", "Non-emergency"
	alias := int64(1)
	for _, p := range []Page{
		{ID: 1, Text: "@@EX0000101 GRS3 DG0801 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1", Priority: &ne, AliasID: &alias, Job: ptr("EX0000101")},
		{ID: 2, Text: "@@EX0000101 GRS3 DG0801 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1", Priority: &em, Job: ptr("EX0000101")},
		{ID: 3, Text: "@@GRASS FIRE NEAR THE HALL", Priority: &em, AliasID: &alias}, // no code, no job
	} {
		if err := d.Stored(context.Background(), nil, p); err != nil {
			t.Errorf("page %d: %v", p.ID, err)
		}
	}
	var none *Detector
	if err := none.Stored(context.Background(), nil, Page{}); err != nil {
		t.Error(err)
	}
}

func TestJoining(t *testing.T) {
	n := network(t)
	ev := int64(9)
	job := []db.JobEventPagesRow{
		{ID: 1, SentAt: at(0), Text: "@@EX0000101 GRS3 DG0801 FIRE @ SAMPLE RD, MADEUPTON #STN1"},
		{ID: 2, SentAt: at(time.Minute), Text: "@@EX0000101 GRS3 FIRE @ SAMPLE RD, MADEUPTON #STN2", EventID: &ev}, // already in an event
		{ID: 3, SentAt: at(2 * time.Minute), Text: "@@EX0000101 GRS3 FIRE @ SAMPLE RD, MADEUPTON #STN3"},
	}
	p := Page{ID: 3, SentAt: at(2 * time.Minute), Job: ptr("EX0000101")}
	got := joining(n, p, nil, job)
	if len(got) != 2 || got[0].id != 1 || !slices.Equal(got[0].codes, []string{"DG0801"}) || got[1].id != 3 || len(got[1].codes) != 0 {
		t.Errorf("joining: %+v, want pages 1 (with its code) and 3, not 2", got)
	}
	// A page beyond the job query's limit still joins, in time order.
	got = joining(n, Page{ID: 4, SentAt: at(-time.Minute), Job: ptr("EX0000101")}, []string{"DG0802"}, job)
	if len(got) != 3 || got[0].id != 4 || !slices.Equal(got[0].codes, []string{"DG0802"}) {
		t.Errorf("a page not in the job's rows: %+v", got)
	}
	// No job: only the page.
	if got = joining(n, Page{ID: 5, SentAt: at(0)}, []string{"DG0803"}, nil); len(got) != 1 || got[0].id != 5 {
		t.Errorf("no job: %+v", got)
	}
}

func TestLevels(t *testing.T) {
	for _, l := range Levels {
		if !l.Valid() || l.Label() == string(l) {
			t.Errorf("level %q: valid %v, label %q", l, l.Valid(), l.Label())
		}
	}
	if Level("huge").Valid() {
		t.Error("an unknown level is valid")
	}
}
