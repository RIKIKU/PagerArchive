package acceptance

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// standClock is a stood-in clock for A133: the API's, and the one events
// open by, live and in a rebuild.
type standClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *standClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *standClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// standIn puts every clock the dot depends on onto c (R77c).
func (e *env) standIn(c *standClock) {
	e.srv.Now = c.now
	e.srv.Ingest.Events.Now = c.now
	e.srv.Ingest.Regroup.Now = c.now
	e.worker.Regroup.Now = c.now
}

// newEvent is m's answer: whether the side menu shows the dot.
func (m *member) newEvent() server.NewLargeEvent {
	m.e.t.Helper()
	var out server.NewLargeEvent
	if code, p := m.call(http.MethodGet, "/api/v1/me/new-large-event", nil, &out); code != http.StatusOK {
		m.e.t.Fatalf("%s: new large event: %d %s", m.email, code, p.Error)
	}
	return out
}

// seeEvents is m opening the Large events page.
func (m *member) seeEvents() server.NewLargeEvent {
	m.e.t.Helper()
	var out server.NewLargeEvent
	if code, p := m.call(http.MethodPost, "/api/v1/me/large-events-seen", nil, &out); code != http.StatusOK {
		m.e.t.Fatalf("%s: large events seen: %d %s", m.email, code, p.Error)
	}
	return out
}

// A133 at the API level (R77c): a new large event's dot, for users A (the
// user) and B (the admin), on a stood-in clock. (The browser half is in
// web/tests/e2e.spec.ts.)
func TestA133NewLargeEventDot(t *testing.T) {
	e := setup(t)
	day := e.now.Truncate(24*time.Hour).AddDate(0, 0, -2)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	clock := &standClock{t: at(9, 0)}
	e.standIn(clock)
	a, b := e.user, e.admin
	aSecond, err := e.signIn(a.claims)
	if err != nil {
		t.Fatal(err)
	}
	want := func(step string, m *member, on bool) server.NewLargeEvent {
		t.Helper()
		got := m.newEvent()
		if got.New != on {
			t.Errorf("%s: %s has a new event: %v, want %v (%+v)", step, m.email, got.New, on, got)
		}
		if !got.Now.Equal(clock.now()) {
			t.Errorf("%s: now is %v, want the stood-in %v", step, got.Now, clock.now())
		}
		return got
	}

	want("no events", a, false)
	want("no events", b, false)

	// An event opens from Emergency pages sent at 10:00.
	clock.set(at(10, 0).Add(5 * time.Second))
	e.mustPost("a", "0100001", dgPage("EX0013301", "DG0331", "STN1"), at(10, 0))
	e.mustPost("a", "0100002", jobPage("EX0013301", 2, "STN2"), at(10, 0).Add(2*time.Second))
	if evs := e.largeEvents(a); len(evs) != 1 {
		t.Fatalf("events after 10:00: %+v, want 1", evs)
	}
	got := want("an event at 10:00", a, true)
	want("an event at 10:00", b, true)
	if got.Until == nil || !got.Until.Equal(at(14, 0)) {
		t.Errorf("the dot goes at %v, want 14:00, 4 hours after the start", got.Until)
	}

	// A opens the Large events page.
	clock.set(at(10, 5))
	if got := a.seeEvents(); got.New {
		t.Errorf("A's seen call answers %+v, want no new event", got)
	}
	want("A looked", a, false)
	want("A looked", b, true)
	want("A looked, in A's second browser", aSecond, false)

	// A page joins the event at 10:30.
	clock.set(at(10, 30).Add(time.Second))
	e.mustPost("a", "0100003", dgPage("EX0013302", "DG0332", "STN3"), at(10, 30))
	if evs := e.largeEvents(a); len(evs) != 1 || evs[0].EmergencyPages != 3 {
		t.Fatalf("after the 10:30 page: %+v, want 1 event of 3 pages", evs)
	}
	want("a page joined", a, false)

	// The event's pages are re-parsed (as after an alias import, R23a) and
	// the event rebuilt (R87f).
	clock.set(at(10, 40))
	before := e.largeEvents(a)[0]
	var openedBefore time.Time
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT opened_at FROM large_events WHERE id = $1", before.ID).Scan(&openedBefore); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RequestReparse(e.ctx, db.RequestReparseParams{CapcodePattern: "%", Reason: "A133"}); err != nil {
		t.Fatal(err)
	}
	e.reparseNow()
	after := e.largeEvents(a)
	if len(after) != 1 || after[0].EmergencyPages != 3 {
		t.Fatalf("after the rebuild: %+v, want the one event of 3 pages", after)
	}
	var openedAfter time.Time
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT opened_at FROM large_events WHERE id = $1", after[0].ID).Scan(&openedAfter); err != nil {
		t.Fatal(err)
	}
	if !openedAfter.Equal(openedBefore) {
		t.Errorf("the rebuild moved the event's opening from %v to %v", openedBefore, openedAfter)
	}
	want("the event rebuilt", a, false)
	want("the event rebuilt", b, true)

	// A second event opens at 11:00. R87a joins a page sent within 12 hours
	// of an event's last page (10:30) to it, so a second event can't open at
	// 11:00 the same day: it opens at 11:00 the next day, after 12 quiet hours.
	next := func(h, m int) time.Time { return at(24+h, m) }
	clock.set(next(11, 0).Add(5 * time.Second))
	want("the next day, before the second event", a, false)
	want("the next day, before the second event", b, false) // B's 4 hours went by
	e.mustPost("a", "0100004", dgPage("EX0013303", "DG0333", "STN4"), next(11, 0))
	if evs := e.largeEvents(a); len(evs) != 2 {
		t.Fatalf("events after the second: %+v, want 2", evs)
	}
	got = want("a second event", a, true)
	want("a second event", b, true)
	if got.Until == nil || !got.Until.Equal(next(15, 0)) {
		t.Fatalf("the dot goes at %v, want 15:00", got.Until)
	}

	// At 15:00, 4 hours after the newest start, neither has, without opening
	// the page.
	clock.set(next(15, 0))
	want("4 hours after the newest start", a, false)
	want("4 hours after the newest start", b, false)

	// An import of Emergency pages opens no event.
	clock.set(next(15, 5))
	r := e.mustImport(
		importLine(next(14, 30), "0100001", dgPage("EX0013304", "DG0334", "STN1"), ""),
		importLine(next(14, 30).Add(time.Second), "0100002", dgPage("EX0013304", "DG0335", "STN2"), ""),
	)
	if r.Stored != 2 {
		t.Fatalf("import stored %d, want 2", r.Stored)
	}
	if evs := e.largeEvents(a); len(evs) != 2 {
		t.Errorf("events after the import: %d, want still 2", len(evs))
	}
	want("an import", a, false)
	want("an import", b, false)

	// Only ever about the user asking; signed out, nothing.
	if code, _ := a.call(http.MethodPost, "/api/v1/me/large-events-seen", map[string]any{"user_id": b.id}, nil); code != http.StatusBadRequest {
		t.Errorf("a body naming another user: %d, want 400", code)
	}
	var seenB *time.Time
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT seen_large_events FROM users WHERE id = $1", b.id).Scan(&seenB); err != nil || seenB != nil {
		t.Errorf("after the refused call, B's seen time is %v (%v), want none", seenB, err)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/me/large-events-seen"},
		{http.MethodGet, "/api/v1/me/new-large-event"},
		{http.MethodGet, "/api/v1/me/new-large-event/stream"},
	} {
		if code, _ := (&member{e: e}).call(c.method, c.path, nil, nil); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s: %d, want 401", c.method, c.path, code)
		}
	}
}

// openedAt is when an event opened (R77c), as stored.
func (e *env) openedAt(id int64) time.Time {
	e.t.Helper()
	var t time.Time
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT opened_at FROM large_events WHERE id = $1", id).Scan(&t); err != nil {
		e.t.Fatal(err)
	}
	return t
}

// R77c, R87f: a rebuild that gives an event a new ID keeps its opening, so a
// user who saw it isn't shown it again; only an event a rebuild makes from
// pages in no event opens then. And a merge keeps the newer opening.
func TestR77cRebuildKeepsOpening(t *testing.T) {
	e := setup(t)
	opened := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) // made up, long before
	base := e.now.Add(-72 * time.Hour)
	// E1: two pages with codes; E2, 13 hours after E1's last: one.
	e.mustPost("a", "0100001", dgPage("EX0013311", "DG0341", "STN1"), base)
	e.mustPost("a", "0100002", dgPage("EX0013312", "DG0342", "STN2"), base.Add(time.Minute))
	p3 := e.mustPost("a", "0100003", dgPage("EX0013313", "DG0343", "STN3"), base.Add(13*time.Hour+time.Minute)).MessageID
	evs := e.largeEvents(e.user)
	if len(evs) != 2 {
		t.Fatalf("events: %+v, want 2", evs)
	}
	e1 := evs[1].ID
	// As a rebuild might find them: all three pages in E1, opened then.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE large_event_messages SET event_id = $1 WHERE message_id = $2", []any{e1, p3}},
		{"DELETE FROM large_events WHERE id <> $1", []any{e1}},
		{"UPDATE large_events SET opened_at = $2, last_at = $3 WHERE id = $1", []any{e1, opened, base.Add(13*time.Hour + time.Minute)}},
	} {
		if _, err := e.st.Pool.Exec(e.ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	var aff events.Affected
	aff.Add(base)
	if err := e.worker.RebuildEvents(e.ctx, &aff); err != nil {
		t.Fatal(err)
	}
	evs = e.largeEvents(e.user)
	if len(evs) != 2 || evs[1].ID != e1 || evs[0].ID == e1 {
		t.Fatalf("after the rebuild: %+v, want E1 (%d) and a new event", evs, e1)
	}
	for _, ev := range evs {
		if got := e.openedAt(ev.ID); !got.Equal(opened) {
			t.Errorf("event %d opened at %v, want E1's %v: the rebuild split it, giving one part a new ID", ev.ID, got, opened)
		}
	}

	// A merge: a page with a code between them joins both, into the older,
	// which keeps the newer opening.
	newer := opened.Add(time.Hour)
	if _, err := e.st.Pool.Exec(e.ctx, "UPDATE large_events SET opened_at = $2 WHERE id = $1", evs[0].ID, newer); err != nil {
		t.Fatal(err)
	}
	e.mustPost("a", "0100004", dgPage("EX0013314", "DG0344", "STN4"), base.Add(7*time.Hour))
	if evs = e.largeEvents(e.user); len(evs) != 1 || evs[0].ID != e1 {
		t.Fatalf("after the merge: %+v, want E1 alone", evs)
	}
	if got := e.openedAt(e1); !got.Equal(newer) {
		t.Errorf("merged, E1 opened at %v, want the newer %v", got, newer)
	}

	// Pages in no event, which a rebuild groups: that event opens now.
	if _, err := e.st.Pool.Exec(e.ctx, "DELETE FROM large_events"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := e.worker.RebuildEvents(e.ctx, &aff); err != nil {
		t.Fatal(err)
	}
	if evs = e.largeEvents(e.user); len(evs) != 1 {
		t.Fatalf("rebuilt from pages in no event: %+v, want 1", evs)
	}
	if got := e.openedAt(evs[0].ID); got.Before(start.Add(-time.Second)) {
		t.Errorf("an event a rebuild made from pages in no event opened at %v, want now (%v)", got, start)
	}
}

// R77c: the dot is live: a state event on the stream when an event opens,
// for every user, and when the user opens the Large events page in another
// session, only for them.
func TestR77cDotLive(t *testing.T) {
	e := setup(t)
	aSecond, err := e.signIn(e.user.claims)
	if err != nil {
		t.Fatal(err)
	}
	state := func(ch <-chan event, who string) server.NewLargeEvent {
		t.Helper()
		ev, _ := until(t, ch, "state", 3*time.Second)
		var s server.NewLargeEvent
		if err := json.Unmarshal([]byte(ev.Data), &s); err != nil {
			t.Fatalf("%s: %v in %q", who, err, ev.Data)
		}
		return s
	}
	a := e.sseAt("/api/v1/me/new-large-event/stream", e.user.cookie, "")
	b := e.sseAt("/api/v1/me/new-large-event/stream", e.admin.cookie, "")
	if s := state(a, "A"); s.New {
		t.Errorf("A at first: %+v", s)
	}
	if s := state(b, "B"); s.New {
		t.Errorf("B at first: %+v", s)
	}
	start := time.Now()
	e.mustPost("a", "0100001", dgPage("EX0013321", "DG0351", "STN1"), e.now)
	if s := state(a, "A"); !s.New {
		t.Errorf("A after an event opened: %+v", s)
	}
	if s := state(b, "B"); !s.New {
		t.Errorf("B after an event opened: %+v", s)
	}
	t.Logf("R77c: both streams told %v after the post", time.Since(start).Round(time.Millisecond))
	// A page joining it tells nobody.
	e.mustPost("a", "0100002", dgPage("EX0013322", "DG0352", "STN2"), e.now.Add(time.Second))
	aSecond.seeEvents()
	if s := state(a, "A"); s.New {
		t.Errorf("A after looking in another session: %+v", s)
	}
	select {
	case ev := <-b:
		t.Errorf("B was told %+v when A looked, or when a page joined", ev)
	case <-time.After(500 * time.Millisecond):
	}
}
