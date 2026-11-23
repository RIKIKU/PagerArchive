package acceptance

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// areaNames is "Zone A, Region North" for a list of areas, by layer label.
func areaNames(refs []server.AreaRef) string {
	label := map[string]string{"zones": "Zone", "regions": "Region"}
	var out []string
	for _, r := range refs {
		out = append(out, label[r.Layer]+" "+r.Name)
	}
	return strings.Join(out, ", ")
}

// A134 at the API level (R87c, R88b): a large event's areas, every area of
// every layer its pages are in, on the Large events list and the event's
// view; none for an event in none. The list asks for them in one query for
// the whole page of events. (The browser half is in web/tests/e2e.spec.ts.)
func TestA134LargeEventAreas(t *testing.T) {
	e := setup(t)
	base := e.now.Add(-48 * time.Hour)
	e.mustPost("a", "0100001", "@@EX0013401 GRS3 DG0361 GRASS FIRE @ 3 FICTION ST, MADEUPTON #STN1", base)
	e.mustPost("a", "0100002", "@@EX0013401 HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON #STN2", base.Add(time.Minute))
	// 13 hours later, its own event, in no area.
	e.mustPost("a", "0100003", "@@EX0013402 GRS3 DG0362 GRASS FIRE @ 4 FICTION ST, TESTVILLE #STN3", base.Add(13*time.Hour))
	evs := e.largeEvents(e.user)
	if len(evs) != 2 {
		t.Fatalf("events: %+v, want 2", evs)
	}
	one, two := evs[1], evs[0]
	if got := areaNames(one.Areas); got != "Zone A, Region North" {
		t.Errorf("the first event's areas on the list: %q, want Zone A, Region North", got)
	}
	if two.Areas == nil || len(two.Areas) != 0 {
		t.Errorf("the second event's areas on the list: %#v, want none (an empty list)", two.Areas)
	}
	if got := areaNames(e.largeEvent(e.user, one.ID).Areas); got != "Zone A, Region North" {
		t.Errorf("the first event's view: %q", got)
	}
	if got := e.largeEvent(e.user, two.ID).Areas; got == nil || len(got) != 0 {
		t.Errorf("the second event's view: %#v, want none", got)
	}

	// A page at 5 EXAMPLE ST (Zone B, Region North) joins the first event.
	e.mustPost("a", "0100004", "@@EX0013403 GRS3 DG0363 GRASS FIRE @ 5 EXAMPLE ST, MADEUPTON #STN4", base.Add(2*time.Minute))
	var a server.JobAreas
	if code, p := e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/large-events/%d/areas", one.ID), nil, &a); code != http.StatusOK {
		t.Fatalf("event areas: %d %s", code, p.Error)
	}
	if got := areaNames(a.Areas); got != "Zone A, Zone B, Region North" {
		t.Errorf("after the page joined, its areas: %q, want Zone A, Zone B, Region North (Region North once)", got)
	}
	if got := areaNames(e.largeEvents(e.user)[1].Areas); got != "Zone A, Zone B, Region North" {
		t.Errorf("after the page joined, the list: %q", got)
	}
	if code, _ := e.user.call(http.MethodGet, "/api/v1/large-events/999999/areas", nil, nil); code != http.StatusNotFound {
		t.Errorf("no such event's areas: %d, want 404", code)
	}
	if code, _ := (&member{e: e}).call(http.MethodGet, fmt.Sprintf("/api/v1/large-events/%d/areas", one.ID), nil, nil); code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", code)
	}

	// The list's areas are one query for its page of events, not one each.
	for i := range 6 {
		e.mustPost("a", "0100001", fmt.Sprintf("@@EX00134%02d GRS3 DG0%03d GRASS FIRE @ 3 FICTION ST, MADEUPTON #STN1", 10+i, 370+i), base.Add(-time.Duration(i+1)*26*time.Hour))
	}
	e.queries.start()
	listed := len(e.largeEvents(e.user))
	sqls := e.queries.stop()
	n := 0
	for _, q := range sqls {
		if strings.Contains(q, "-- name: LargeEventsAreas ") {
			n++
		}
	}
	if got := listed; got != 8 || n != 1 {
		t.Errorf("the list of %d events made %d areas queries, want 8 events and 1 query", got, n)
	}

	// A profile with no layers shows none, anywhere.
	layers := e.srv.Areas
	e.srv.Areas = nil
	defer func() { e.srv.Areas = layers }()
	for _, ev := range e.largeEvents(e.user) {
		if len(ev.Areas) != 0 {
			t.Errorf("with no layers, event %d has areas %v", ev.ID, ev.Areas)
		}
	}
	if got := e.largeEvent(e.user, one.ID).Areas; len(got) != 0 || !slices.Equal(got, []server.AreaRef{}) {
		t.Errorf("with no layers, the view: %v", got)
	}
}
