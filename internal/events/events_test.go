package events

import (
	"context"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

var t0 = time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func ptr[T any](v T) *T { return &v }

func jobPage(id int64, d time.Duration) db.JobEventPagesRow {
	return db.JobEventPagesRow{ID: id, SentAt: at(d)}
}

// A job opens an event at the threshold's number of Emergency pages that
// count, one per stored message (owner, 2026-11-13). What the pages say
// (appliances or none) doesn't matter.
func TestReached(t *testing.T) {
	job := []db.JobEventPagesRow{jobPage(1, 0), jobPage(2, time.Minute), jobPage(3, 2*time.Minute), jobPage(4, 3*time.Minute)}
	for _, c := range []struct {
		threshold int
		want      bool
	}{{2, true}, {4, true}, {5, false}, {0, false}} {
		if got := Reached(job, c.threshold); got != c.want {
			t.Errorf("threshold %d: %v, want %v", c.threshold, got, c.want)
		}
	}
	if Reached(nil, DefaultPages) {
		t.Error("no pages reached the threshold")
	}
}

// A85's numbers: 19 Emergency pages don't open an event; the 20th does.
func TestReachedAtTwenty(t *testing.T) {
	var job []db.JobEventPagesRow
	for i := range 19 {
		job = append(job, jobPage(int64(i+1), time.Duration(i)*time.Minute))
	}
	if Reached(job, DefaultPages) {
		t.Fatal("19 pages reached 20")
	}
	job = append(job, jobPage(20, time.Hour))
	if !Reached(job, DefaultPages) {
		t.Fatal("the 20th page didn't reach 20")
	}
	if Reached(job, 25) {
		t.Error("20 pages reached 25")
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

// R5: a code matching the example network's pattern (DG and 4 digits) opens
// an event; others don't.
func TestHasCode(t *testing.T) {
	n := network(t)
	for text, want := range map[string]bool{
		"@@EX0000101 GRS3 DG0801 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1": true,
		"@@EX0000101 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1":        false,
		"@@EX0000101 GRS3 DG08011 GRASS FIRE #STN1":                       false,
	} {
		if got := HasCode(n, text); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
	if HasCode(&profile.Network{}, "@@DG0801") {
		t.Error("a network with no pattern found a code")
	}
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
		{"an ignored alias (owner, 2026-11-14)", Page{Priority: &em, AliasID: &alias, Ignored: true}, false},
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
		{ID: 4, Text: "@@EX0000101 GRS3 DG0801 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1", Priority: &em, AliasID: &alias, Ignored: true, Job: ptr("EX0000101")},
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
	ev := int64(9)
	job := []db.JobEventPagesRow{
		{ID: 1, SentAt: at(0)},
		{ID: 2, SentAt: at(time.Minute), EventID: &ev}, // already in an event
		{ID: 3, SentAt: at(2 * time.Minute)},
	}
	p := Page{ID: 3, SentAt: at(2 * time.Minute), Job: ptr("EX0000101")}
	got := joining(p, job)
	if len(got) != 2 || got[0].id != 1 || got[1].id != 3 {
		t.Errorf("joining: %+v, want pages 1 and 3, not 2", got)
	}
	// A page beyond the job query's limit still joins, in time order.
	got = joining(Page{ID: 4, SentAt: at(-time.Minute), Job: ptr("EX0000101")}, job)
	if len(got) != 3 || got[0].id != 4 {
		t.Errorf("a page not in the job's rows: %+v", got)
	}
	// No job: only the page.
	if got = joining(Page{ID: 5, SentAt: at(0)}, nil); len(got) != 1 || got[0].id != 5 {
		t.Errorf("no job: %+v", got)
	}
}
