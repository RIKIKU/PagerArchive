package acceptance

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// M22: large events (R87-R87e, ADR 0027). Every page is made up, from the
// example network: deployment codes are DG and 4 digits (SPEC.md section 10).

// dgPage is an Emergency page on job with deployment code code, for group.
func dgPage(job, code, group string) string {
	return fmt.Sprintf("@@%s GRS3 %s GRASS FIRE @ SAMPLE RD, MADEUPTON #%s", job, code, group)
}

// jobPage is Emergency page n of job, for group, naming appliances (R8's
// units field) if any are given.
func jobPage(job string, n int, group string, units ...string) string {
	named := ""
	if len(units) > 0 {
		named = " UNITS " + strings.Join(units, " ")
	}
	return fmt.Sprintf("@@%s HSF1 SMOKE FROM FACTORY UPDATE %d @ 12 FICTION ST, MADEUPTON%s #%s", job, n, named, group)
}

// appliances is n made-up appliance names from U<from>.
func appliances(from, n int) []string {
	var out []string
	for i := from; i < from+n; i++ {
		out = append(out, fmt.Sprintf("U%02d", i))
	}
	return out
}

// aliased is the nth of the fixture's four aliased capcodes, in turn.
func aliased(n int) string { return fmt.Sprintf("010000%d", n%4+1) }

// clocked moves the API's clock with the integrations' (e.advance), so pages
// can be posted hours apart and an event can end (R87a).
func (e *env) clocked() {
	e.srv.Now = e.clock
}

func (e *env) largeEvents(m *member) []server.LargeEvent {
	e.t.Helper()
	var out server.LargeEventPage
	if code, p := m.call(http.MethodGet, "/api/v1/large-events", nil, &out); code != http.StatusOK {
		e.t.Fatalf("large events: %d %s", code, p.Error)
	}
	return out.Events
}

func (e *env) largeEvent(m *member, id int64) server.LargeEvent {
	e.t.Helper()
	var out server.LargeEvent
	if code, p := m.call(http.MethodGet, fmt.Sprintf("/api/v1/large-events/%d", id), nil, &out); code != http.StatusOK {
		e.t.Fatalf("large event %d: %d %s", id, code, p.Error)
	}
	return out
}

// eventPages is a large event's pages, as its view lists them.
func (e *env) eventPages(m *member, id int64) []server.Message {
	e.t.Helper()
	var out server.MessagePage
	if code, p := m.call(http.MethodGet, fmt.Sprintf("/api/v1/messages?event=%d&ignored=true&limit=200", id), nil, &out); code != http.StatusOK {
		e.t.Fatalf("event pages: %d %s", code, p.Error)
	}
	return out.Messages
}

// noLevelsOrCodes checks an event's JSON for what it no longer has (owner,
// 2026-11-13): no levels, no count or list of codes, no appliances.
func (e *env) noLevelsOrCodes(m *member, id int64) {
	e.t.Helper()
	var raw map[string]any
	if code, _ := m.call(http.MethodGet, fmt.Sprintf("/api/v1/large-events/%d", id), nil, &raw); code != http.StatusOK {
		e.t.Fatalf("large event %d: %d", id, code)
	}
	for k := range raw {
		if strings.Contains(k, "level") || strings.Contains(k, "code") || k == "units" || k == "appliances" {
			e.t.Errorf("the event's JSON has %q: %v", k, raw)
		}
	}
}

// A84. Large events open and group (owner, 2026-11-13: no levels).
func TestA84LargeEventsOpenAndGroup(t *testing.T) {
	e := setup(t)
	e.clocked()
	t10 := e.clock().Truncate(time.Second) // "10:00"

	// The same page on a capcode with no alias, or as a Non-emergency page:
	// no event opens.
	page1 := dgPage("EX0000101", "DG0801", "STN1")
	e.mustPost("a", "0999999", page1, t10)
	e.mustPost("a", "0100001", "Hb"+page1[2:], t10)
	if evs := e.largeEvents(e.user); len(evs) != 0 {
		t.Fatalf("an unaliased or Non-emergency page opened %d events", len(evs))
	}

	// 10:00: an Emergency page with DG0801 on job EX0000101, to an aliased
	// capcode: one event, from that page.
	first := e.mustPost("a", "0100001", page1, t10).MessageID
	evs := e.largeEvents(e.user)
	if len(evs) != 1 || !evs[0].Start.Equal(t10) || evs[0].Jobs != 1 || evs[0].EmergencyPages != 1 {
		t.Fatalf("after DG0801: %+v, want one event from %v, 1 job, 1 Emergency page", evs, t10)
	}
	id := evs[0].ID

	// 15:00: DG0802 on job EX0000102 joins it.
	e.advance(5 * time.Hour)
	t15 := e.clock().Truncate(time.Second)
	second := e.mustPost("a", "0100002", dgPage("EX0000102", "DG0802", "STN2"), t15).MessageID
	evs = e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].ID != id || evs[0].Jobs != 2 || evs[0].EmergencyPages != 2 || !evs[0].End.Equal(t15) {
		t.Fatalf("after DG0802: %+v, want the same event with 2 jobs and 2 Emergency pages, to %v", evs, t15)
	}
	ev := e.largeEvent(e.user, id)
	if !slices.Equal(ev.JobList, []string{"EX0000101", "EX0000102"}) {
		t.Errorf("the event's jobs: %v", ev.JobList)
	}
	e.noLevelsOrCodes(e.user, id)
	var ids []int64
	for _, m := range e.eventPages(e.user, id) {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []int64{second, first}) {
		t.Errorf("the event's pages are %v, want %v (newest first; not the unaliased or Non-emergency copy)", ids, []int64{second, first})
	}

	// No page joins until 03:01 the next day: the event takes no more pages.
	e.advance(12*time.Hour + time.Minute)
	if evs = e.largeEvents(e.user); !evs[0].End.Equal(t15) {
		t.Errorf("end %v, want its last page's %v", evs[0].End, t15)
	}
	// 04:00: a page with a code opens a new one.
	e.advance(59 * time.Minute)
	e.mustPost("a", "0100003", dgPage("EX0000103", "DG0803", "STN3"), e.clock())
	evs = e.largeEvents(e.user)
	if len(evs) != 2 || evs[1].ID != id || evs[0].EmergencyPages != 1 {
		t.Fatalf("at 04:00: %+v, want a new event, newest first, beside the old one", evs)
	}
	t.Logf("A84: event %d from %v to %v, jobs %v; then event %d at %v", id, evs[1].Start, evs[1].End, ev.JobList, evs[0].ID, evs[0].Start)
}

// A85. A job opens an event at 20 Emergency pages to aliased capcodes, each
// stored message once (owner, 2026-11-13).
func TestA85JobOpensAnEvent(t *testing.T) {
	e := setup(t)
	e.clocked()
	t0 := e.clock().Add(-30 * time.Minute).Truncate(time.Second)
	// Job EX0000201's 19 Emergency pages to aliased capcodes, the first
	// naming 25 appliances: appliances don't count, pages do.
	var posted []int64
	for i := range 19 {
		var named []string
		if i == 0 {
			named = appliances(1, 25)
		}
		posted = append(posted, e.mustPost("a", aliased(i), jobPage("EX0000201", i+1, "STN1", named...), t0.Add(time.Duration(i)*time.Minute)).MessageID)
	}
	// Pages that don't count: the 19th again from a second receiver (the
	// same message, R19), a Non-emergency follow-up, and an Emergency page of
	// the job to a capcode with no alias.
	if dup := e.mustPost("b", aliased(18), jobPage("EX0000201", 19, "STN1"), t0.Add(18*time.Minute+2*time.Second)); dup.MessageID != posted[18] {
		t.Fatalf("the second receiver's copy is message %d, want %d", dup.MessageID, posted[18])
	}
	e.mustPost("a", "0100001", "HbRE EX0000201 MORE CREWS #STN1", t0.Add(19*time.Minute))
	e.mustPost("a", "0999999", jobPage("EX0000201", 20, "STN1"), t0.Add(19*time.Minute))
	if evs := e.largeEvents(e.user); len(evs) != 0 {
		t.Fatalf("19 Emergency pages opened %d events", len(evs))
	}
	// The 20th: an event holding the job's pages.
	at20 := t0.Add(20 * time.Minute)
	posted = append(posted, e.mustPost("a", aliased(19), jobPage("EX0000201", 20, "STN2"), at20).MessageID)
	evs := e.largeEvents(e.user)
	if len(evs) != 1 {
		t.Fatalf("after the 20th page: %+v, want one event", evs)
	}
	if evs[0].EmergencyPages != 20 || evs[0].Jobs != 1 || !evs[0].Start.Equal(t0) || !evs[0].End.Equal(at20) {
		t.Errorf("event %+v: want 20 Emergency pages, 1 job, from the job's first page to its 20th", evs[0])
	}
	var held []int64
	for _, m := range e.eventPages(e.user, evs[0].ID) {
		held = append(held, m.ID)
	}
	slices.Sort(held)
	if !slices.Equal(held, posted) {
		t.Errorf("the event holds %v, want the job's Emergency pages to aliases %v", held, posted)
	}
	// No levels, codes or appliance count in the API (owner, 2026-11-13).
	e.noLevelsOrCodes(e.user, evs[0].ID)

	// Only an admin sets the threshold, from 2 to 1,000 pages.
	if code, _ := e.user.call(http.MethodPut, "/api/v1/admin/large-events", server.LargeEventSettings{JobPages: 25}, nil); code != http.StatusForbidden {
		t.Errorf("a user setting the threshold: %d, want 403", code)
	}
	for _, n := range []int{0, 1, 1001} {
		if code, _ := e.admin.call(http.MethodPut, "/api/v1/admin/large-events", server.LargeEventSettings{JobPages: n}, nil); code != http.StatusUnprocessableEntity {
			t.Errorf("threshold %d: %d, want 422", n, code)
		}
	}
	var st server.LargeEventSettings
	if code, _ := e.admin.call(http.MethodPut, "/api/v1/admin/large-events", server.LargeEventSettings{JobPages: 25}, &st); code != http.StatusOK || st.JobPages != 25 {
		t.Fatalf("setting 25: %d %+v", code, st)
	}
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/large-events", nil, &st); code != http.StatusOK || st.JobPages != 25 {
		t.Fatalf("threshold read back: %d %+v", code, st)
	}
	// A new job reaching 20 Emergency pages opens none, nearby and a day
	// later alike.
	for _, later := range []time.Duration{0, 24 * time.Hour} {
		e.advance(later)
		job := "EX0000202"
		if later > 0 {
			job = "EX0000203"
		}
		base := e.clock().Add(-25 * time.Minute)
		for i := range 20 {
			e.mustPost("a", aliased(i), jobPage(job, i+1, "STN3"), base.Add(time.Duration(i)*time.Minute))
		}
	}
	if evs = e.largeEvents(e.user); len(evs) != 1 {
		t.Fatalf("with the threshold at 25, jobs of 20 Emergency pages gave %d events, want still 1", len(evs))
	}
	t.Logf("A85: 19 Emergency pages (one naming 25 appliances, one copy from a second receiver), no event; the 20th opened event %d (%d Emergency pages); at 25, 20 pages opened none", evs[0].ID, evs[0].EmergencyPages)
}

// R87, R87a, R19: a job's pages arriving out of order, each from two
// receivers, give the event pages in order give: one event holding all 21,
// from the first page by sent time.
func TestJobOutOfOrder(t *testing.T) {
	base := time.Now().Add(-6 * time.Hour).UTC().Truncate(time.Second)
	sentAt := func(i int) time.Time { return base.Add(time.Duration(i) * time.Minute) }
	run := func(order []int, twice bool) (start, end time.Time, pages int64, n int) {
		e := setup(t)
		for _, i := range order {
			e.mustPost("a", aliased(i), jobPage("EX0000701", i+1, "STN1"), sentAt(i))
			if twice {
				e.mustPost("b", aliased(i), jobPage("EX0000701", i+1, "STN1"), sentAt(i).Add(3*time.Second))
			}
		}
		evs := e.largeEvents(e.user)
		if len(evs) == 0 {
			return time.Time{}, time.Time{}, 0, 0
		}
		return evs[0].Start, evs[0].End, evs[0].EmergencyPages, len(evs)
	}
	var inOrder, reversed, shuffled []int
	for i := range 21 {
		inOrder = append(inOrder, i)
		reversed = append(reversed, 20-i)
		shuffled = append(shuffled, (i*8)%21) // 8 and 21 share no factor: every page once
	}
	for _, c := range []struct {
		name  string
		order []int
		twice bool
	}{{"in order", inOrder, false}, {"newest first, from two receivers", reversed, true}, {"shuffled, from two receivers", shuffled, true}} {
		start, end, pages, n := run(c.order, c.twice)
		if n != 1 || !start.Equal(sentAt(0)) || !end.Equal(sentAt(20)) || pages != 21 {
			t.Errorf("%s: %d events, from %v to %v, %d Emergency pages; want 1 from %v to %v, 21 pages",
				c.name, n, start, end, pages, sentAt(0), sentAt(20))
		}
	}
}

// eventDelivery checks one large-event delivery's body (R87d): the event's
// id, start, the page that opened it, its jobs and Emergency pages so far
// and its link; never page text, appliances, codes or levels.
func (e *env) eventDelivery(who string, r standin.Request, eventID, opener int64, openedAt time.Time) {
	e.t.Helper()
	link := "http://pager.example.test/large-events/" + fmt.Sprint(eventID)
	got := r.JSON
	ev, _ := got["large_event"].(map[string]any)
	if got["event_type"] != "large_event" || got["content"] != "summary" || got["id"] != float64(opener) || got["url"] != link || ev == nil {
		e.t.Errorf("%s's delivery: %s", who, r.Body)
		return
	}
	ob, _ := ev["opened_by"].(map[string]any)
	if ev["id"] != float64(eventID) || ev["url"] != link || ev["started_at"] == nil || ob == nil || ob["id"] != float64(opener) ||
		ob["sent_at"] != openedAt.UTC().Format(time.RFC3339) || ev["jobs"] == nil || ev["emergency_pages"] == nil {
		e.t.Errorf("%s's summary: %v", who, ev)
	}
	if len(ev) != 6 {
		e.t.Errorf("%s's summary has %d keys, want 6: %v", who, len(ev), ev)
	}
	for _, k := range []string{"text", "fields", "extra", "location", "address"} {
		if _, ok := got[k]; ok {
			e.t.Errorf("%s's delivery has %q", who, k)
		}
	}
	e.noPageText(who+"'s delivery", string(r.Body))
	for _, s := range []string{"SAMPLE RD", "DG08", "deploy", "level", "units", "appliance"} {
		if strings.Contains(string(r.Body), s) {
			e.t.Errorf("%s's delivery holds %q: %s", who, s, r.Body)
		}
	}
}

// A86. Large-event integrations fire once: when the event opens (owner,
// 2026-11-13).
func TestA86LargeEventIntegrationsFireOnce(t *testing.T) {
	e := setup(t)
	e.clocked()
	e.allowLoopback()
	e.runWorker()
	userC := e.newMember("c@example.com", accounts.RoleUser, e.admin)
	hookB, hookC := standin.Webhook(), standin.Webhook()
	defer hookB.Close()
	defer hookC.Close()
	vb := e.createIntegration(e.user, integrations.Request{Name: "Large events", Kind: integrations.Webhook, LargeEvents: true,
		Secrets: integrations.Secrets{URL: hookB.URL + "/hook"}})
	vc := e.createIntegration(userC, integrations.Request{Name: "Big days", Kind: integrations.Webhook, LargeEvents: true,
		Content: integrations.Full, Secrets: integrations.Secrets{URL: hookC.URL + "/hook"}})
	if vc.Content != integrations.Summary || !vc.LargeEvents || !vb.LargeEvents {
		t.Errorf("C's integration: content %q, large events %v; want summary, large events", vc.Content, vc.LargeEvents)
	}

	// 10:00: DG0801 opens the event. Each webhook gets one delivery within 5 s.
	t10 := e.clock().Truncate(time.Second)
	start := time.Now()
	p1 := e.mustPost("a", "0100001", dgPage("EX0000101", "DG0801", "STN1"), t10).MessageID
	if !eventually(5*time.Second-time.Since(start), func() bool { return len(hookB.Requests()) == 1 && len(hookC.Requests()) == 1 }) {
		t.Fatalf("within 5 s: B's webhook got %d requests, C's %d; want 1 each", len(hookB.Requests()), len(hookC.Requests()))
	}
	t.Logf("both deliveries %v after the page", time.Since(start).Round(time.Millisecond))
	// 15:00: a second code and job join; then a third code, an ordinary page
	// of a job in the event, and a page from a second client (R51b): none
	// sends anything.
	e.advance(5 * time.Hour)
	e.mustPost("a", "0100002", dgPage("EX0000102", "DG0802", "STN2"), e.clock())
	e.mustPost("a", "0100003", dgPage("EX0000103", "DG0803", "STN3"), e.clock())
	e.mustPost("a", "0100004", "@@EX0000103 GRS3 MORE CREWS @ SAMPLE RD, MADEUPTON #STN4", e.clock().Add(time.Minute))
	e.mustPost("b", "0100003", dgPage("EX0000103", "DG0803", "STN3"), e.clock().Add(2*time.Second))
	time.Sleep(1500 * time.Millisecond)
	if b, c := len(hookB.Requests()), len(hookC.Requests()); b != 1 || c != 1 {
		t.Fatalf("after more codes and jobs joined: B got %d, C %d; want 1 each", b, c)
	}

	evs := e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].Jobs != 3 || evs[0].EmergencyPages != 4 {
		t.Fatalf("events %+v, want 1 with 3 jobs and 4 Emergency pages", evs)
	}
	e.eventDelivery("B", hookB.Requests()[0], evs[0].ID, p1, t10)
	e.eventDelivery("C", hookC.Requests()[0], evs[0].ID, p1, t10)
	// Each owner sees their one delivery, carried by the page, about the event.
	for _, c := range []struct {
		m  *member
		id int64
	}{{e.user, vb.ID}, {userC, vc.ID}} {
		ds := e.deliveries(c.m, c.id)
		if len(ds) != 1 || ds[0].EventID == nil || *ds[0].EventID != evs[0].ID || ds[0].MessageID != p1 || ds[0].Status != "delivered" {
			t.Errorf("deliveries of integration %d: %+v", c.id, ds)
		}
	}
	t.Logf("A86: one delivery each when the event opened, nothing as codes and jobs joined: %s", hookB.Requests()[0].Body)
}

// R87a, R87d: two events, each told of when it opened, merged by a page
// between them: the merge sends nothing, and the event keeps both deliveries.
func TestLargeEventMergeFiresNothing(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	hook := standin.Webhook()
	defer hook.Close()
	v := e.createIntegration(e.user, integrations.Request{Name: "Large events", Kind: integrations.Webhook, LargeEvents: true,
		Secrets: integrations.Secrets{URL: hook.URL + "/hook"}})
	at := e.now.Add(-2 * time.Minute)
	// Two events 20 hours apart by sent time. The older page is too old to
	// send (R51a), but its delivery is recorded all the same.
	e.mustPost("a", "0100001", dgPage("EX0000901", "DG0901", "STN1"), at)
	e.mustPost("a", "0100002", dgPage("EX0000902", "DG0902", "STN2"), at.Add(-20*time.Hour))
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) == 1 }) {
		t.Fatalf("got %d deliveries, want 1", len(hook.Requests()))
	}
	if evs := e.largeEvents(e.user); len(evs) != 2 {
		t.Fatalf("%d events, want 2", len(evs))
	}
	if ds := e.deliveries(e.user, v.ID); len(ds) != 2 {
		t.Fatalf("before the merge: deliveries %+v, want one per event", ds)
	}
	// A page 10 hours before the newer: within 12 hours of both.
	e.mustPost("a", "0100003", dgPage("EX0000903", "DG0903", "STN3"), at.Add(-10*time.Hour))
	time.Sleep(1500 * time.Millisecond)
	if n := len(hook.Requests()); n != 1 {
		t.Errorf("after the merge: %d requests, want still 1", n)
	}
	evs := e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].EmergencyPages != 3 {
		t.Fatalf("after the merge: %+v, want one event of 3 pages", evs)
	}
	ds := e.deliveries(e.user, v.ID)
	if len(ds) != 2 {
		t.Fatalf("after the merge: deliveries %+v, want still the two openings'", ds)
	}
	for _, d := range ds {
		if d.EventID == nil || *d.EventID != evs[0].ID {
			t.Errorf("delivery %+v isn't about the merged event %d", d, evs[0].ID)
		}
	}
}

// R87d: a large-event integration's selection, content and Send test.
func TestLargeEventIntegrationRules(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	url := integrations.Secrets{URL: hook.URL + "/hook"}
	if code, _ := e.user.call(http.MethodPost, "/api/v1/integrations",
		integrations.Request{Name: "Both", Kind: integrations.Webhook, LargeEvents: true, Capcodes: []string{"0100001"}, Secrets: url}, nil); code != http.StatusUnprocessableEntity {
		t.Errorf("large events and a capcode: %d, want 422", code)
	}
	// A level, as main had before migration 00019, is refused: there are none.
	if code, _ := e.user.call(http.MethodPost, "/api/v1/integrations",
		map[string]any{"name": "Level", "kind": "webhook", "large_events": "big_job", "secrets": map[string]string{"url": url.URL}}, nil); code < 400 || code > 499 {
		t.Errorf("a level: %d, want a 4xx", code)
	}
	v := e.createIntegration(e.user, integrations.Request{Name: "Large events", Kind: integrations.Webhook, LargeEvents: true, Secrets: url})
	if len(v.Capcodes)+len(v.Aliases) != 0 || v.Content != integrations.Summary || !v.LargeEvents {
		t.Errorf("view %+v", v)
	}
	var res integrations.TestResult
	if code, p := e.user.call(http.MethodPost, fmt.Sprintf("/api/v1/integrations/%d/test", v.ID), nil, &res); code != http.StatusOK || !res.OK {
		t.Fatalf("send test: %d %s %+v", code, p.Error, res)
	}
	got := hook.Requests()[0].JSON
	ev, _ := got["large_event"].(map[string]any)
	if got["test"] != true || got["event_type"] != "test" || ev == nil || ev["opened_by"] == nil || ev["level"] != nil {
		t.Errorf("test delivery: %s", hook.Requests()[0].Body)
	}
	// It never fires for a page: a page delivery's selection is capcodes and aliases.
	e.mustPost("a", "0100001", row1Text, e.now)
	var rows int
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT count(*) FROM deliveries").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("a page queued %d deliveries for a large-event integration (%v)", rows, err)
	}
	// Changed to capcodes, it's a page integration again.
	var out integrations.View
	if code, p := e.user.call(http.MethodPut, fmt.Sprintf("/api/v1/integrations/%d", v.ID), integrations.Request{Name: "Now pages", Capcodes: []string{"0100001"}}, &out); code != http.StatusOK || out.LargeEvents || len(out.Aliases) != 1 { // 0100001 is Station 1 (R48)
		t.Errorf("changed to a capcode: %d %s %+v", code, p.Error, out)
	}
}

// R87a, R19: pages that arrive late, out of order or from several clients
// give the same events as pages in order.
func TestLargeEventsOutOfOrder(t *testing.T) {
	base := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	pages := []struct {
		capcode, text string
		at            time.Duration
	}{
		{"0100001", dgPage("EX0000301", "DG0811", "STN1"), 0},
		{"0100002", dgPage("EX0000302", "DG0812", "STN2"), 5 * time.Hour},
		{"0100003", dgPage("EX0000303", "DG0813", "STN3"), 12 * time.Hour}, // bridges the two below
		{"0100004", dgPage("EX0000304", "DG0814", "STN4"), 20 * time.Hour},
		{"0100001", dgPage("EX0000305", "DG0815", "STN1"), -3 * time.Hour}, // before the first
		{"0100002", dgPage("EX0000306", "DG0816", "STN2"), 40 * time.Hour}, // an event of its own
	}
	type got struct {
		start, end  time.Time
		pages, jobs int64
	}
	run := func(order []int, twice bool) []got {
		e := setup(t)
		for _, i := range order {
			p := pages[i]
			e.mustPost("a", p.capcode, p.text, base.Add(p.at))
			if twice {
				// The same page from another client, 3 s later: a duplicate.
				e.mustPost("b", p.capcode, p.text, base.Add(p.at+3*time.Second))
			}
		}
		var out []got
		for _, ev := range e.largeEvents(e.user) {
			out = append(out, got{ev.Start, ev.End, ev.EmergencyPages, ev.Jobs})
		}
		return out
	}
	inOrder := run([]int{4, 0, 1, 2, 3, 5}, false)
	want := []got{
		{base.Add(40 * time.Hour), base.Add(40 * time.Hour), 1, 1},
		{base.Add(-3 * time.Hour), base.Add(20 * time.Hour), 5, 5},
	}
	if fmt.Sprint(inOrder) != fmt.Sprint(want) {
		t.Fatalf("in order: %v, want %v", inOrder, want)
	}
	for _, order := range [][]int{{0, 1, 3, 5, 2, 4}, {5, 3, 2, 1, 0, 4}, {3, 0, 4, 5, 1, 2}} {
		if g := run(order, true); fmt.Sprint(g) != fmt.Sprint(want) {
			t.Errorf("arriving in the order %v, from two clients: %v, want %v", order, g, want)
		}
	}
}

// R87a: an ordinary Emergency page of a job in an event joins it; other
// jobs' pages, and pages that don't count, don't.
func TestLargeEventJobPagesJoin(t *testing.T) {
	e := setup(t)
	t0 := e.now.Add(-2 * time.Hour)
	// A page of job EX0000401 that came first, with no code.
	early := e.mustPost("a", "0100002", "@@EX0000401 GRS3 GRASS FIRE SPREADING @ SAMPLE RD, MADEUPTON #STN2", t0).MessageID
	dg := e.mustPost("a", "0100001", dgPage("EX0000401", "DG0821", "STN1"), t0.Add(10*time.Minute)).MessageID
	later := e.mustPost("a", "0100003", "@@EX0000401 GRS3 MORE CREWS @ SAMPLE RD, MADEUPTON #STN3", t0.Add(time.Hour)).MessageID
	e.mustPost("a", "0100004", "@@EX0000402 GRS3 ANOTHER FIRE @ SAMPLE RD, MADEUPTON #STN4", t0.Add(time.Hour))
	e.mustPost("a", "0100001", "HbRE EX0000401 CREWS RETURNING #STN1", t0.Add(90*time.Minute))
	e.mustPost("a", "0999999", "@@EX0000401 GRS3 UNALIASED COPY @ SAMPLE RD, MADEUPTON #STN1", t0.Add(90*time.Minute))
	evs := e.largeEvents(e.user)
	if len(evs) != 1 {
		t.Fatalf("%d events", len(evs))
	}
	var held []int64
	for _, m := range e.eventPages(e.user, evs[0].ID) {
		held = append(held, m.ID)
	}
	if want := []int64{later, dg, early}; !slices.Equal(held, want) {
		t.Errorf("the event holds %v, want job EX0000401's Emergency pages to aliases %v", held, want)
	}
	if !evs[0].Start.Equal(t0) || !evs[0].End.Equal(t0.Add(time.Hour)) || evs[0].Jobs != 1 {
		t.Errorf("event %+v", evs[0])
	}
}

// R87e: retention deletes an event with its last page, and the numbers of
// one it thins follow the pages it still holds.
func TestLargeEventRetention(t *testing.T) {
	e := setup(t)
	run := time.Now()
	const days = 730
	if err := e.job.SetDays(e.ctx, days); err != nil {
		t.Fatal(err)
	}
	cut := e.job.Cutoff(run, days)
	// An event wholly before the cut-off, and one across it.
	e.mustPost("a", "0100001", dgPage("EX0000501", "DG0831", "STN1"), cut.Add(-48*time.Hour))
	e.mustPost("a", "0100002", dgPage("EX0000502", "DG0832", "STN2"), cut.Add(-47*time.Hour))
	e.mustPost("a", "0100003", dgPage("EX0000503", "DG0833", "STN3"), cut.Add(-2*time.Hour))
	e.mustPost("a", "0100004", dgPage("EX0000504", "DG0834", "STN4"), cut.Add(2*time.Hour))
	evs := e.largeEvents(e.user)
	if len(evs) != 2 || evs[0].EmergencyPages != 2 || evs[0].Jobs != 2 {
		t.Fatalf("before the run: %+v", evs)
	}
	if _, err := e.job.RunOnce(e.ctx, run); err != nil {
		t.Fatal(err)
	}
	evs = e.largeEvents(e.user)
	if len(evs) != 1 {
		t.Fatalf("after the run: %d events, want 1 (the old one gone with its pages)", len(evs))
	}
	if ev := evs[0]; ev.EmergencyPages != 1 || ev.Jobs != 1 || !ev.Start.Equal(cut.Add(2*time.Hour)) {
		t.Errorf("the thinned event: %+v; want 1 page, 1 job, starting at its remaining page", ev)
	}
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT count(*) FROM large_event_messages").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d event pages left (%v), want 1", n, err)
	}
}

// R87c: every signed-in user sees events; nobody else.
func TestLargeEventsSignedIn(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", dgPage("EX0000601", "DG0841", "STN1"), e.now)
	for _, path := range []string{"/api/v1/large-events", "/api/v1/large-events/1", "/api/v1/messages?event=1"} {
		resp, err := (*member)(nil).do(http.MethodGet, e.url+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a session: %d, want 401", path, resp.StatusCode)
		}
	}
	evs := e.largeEvents(e.admin)
	if len(evs) != 1 || len(e.largeEvents(e.user)) != 1 {
		t.Fatalf("admin and user see %d events", len(evs))
	}
	if code, _ := e.user.call(http.MethodGet, "/api/v1/large-events/999999", nil, nil); code != http.StatusNotFound {
		t.Errorf("no such event: %d", code)
	}
	if code, _ := e.user.call(http.MethodGet, "/api/v1/messages?event=999999", nil, nil); code != http.StatusNotFound {
		t.Errorf("no such event's pages: %d", code)
	}
	// The scrubber's marks in its view cover its days.
	var c server.Counts
	if code, _ := e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/counts?event=%d", evs[0].ID), nil, &c); code != http.StatusOK || len(c.Marks) == 0 {
		t.Fatalf("counts: %d %+v", code, c)
	}
	var total int64
	for _, m := range c.Marks {
		total += m.Count
	}
	if total != 1 {
		t.Errorf("the scrubber counts %d of the event's pages, want 1", total)
	}
}

// R87, R51a: detection is cheap on the ingest path. 200 Emergency pages to
// aliased capcodes, each on a job of its own (detection reads each one's job
// pages), are posted with detection on and off; the time each takes is
// logged, and the slowest must stay far inside R51a's 5 seconds. A third run
// has every page join one open event.
func TestLargeEventsCost(t *testing.T) {
	e := setup(t)
	detector := e.srv.Ingest.Events
	run := func(label string, from int, text func(i int) string) time.Duration {
		var total, worst time.Duration
		for i := range 200 {
			start := time.Now()
			e.mustPost("a", fmt.Sprintf("010000%d", i%4+1), text(from+i), e.now.Add(-time.Duration(200-i)*time.Second))
			d := time.Since(start)
			total += d
			worst = max(worst, d)
		}
		t.Logf("%s: 200 pages, %v each on average, the slowest %v", label, (total / 200).Round(10*time.Microsecond), worst.Round(10*time.Microsecond))
		if worst > time.Second {
			t.Errorf("%s: a page took %v to store", label, worst)
		}
		return total
	}
	plain := func(i int) string {
		return jobPage(fmt.Sprintf("EX%07d", 7000000+i), 1, "STN1", appliances(i%50, 5)...)
	}
	e.srv.Ingest.Events = nil
	off := run("detection off", 0, plain)
	e.srv.Ingest.Events = detector
	on := run("detection on", 1000, plain)
	e.mustPost("a", "0100001", dgPage("EX0007999", "DG0851", "STN1"), e.now.Add(-201*time.Second))
	joined := run("detection on, every page joining an open event", 2000, func(i int) string {
		return dgPage(fmt.Sprintf("EX%07d", 7000000+i), fmt.Sprintf("DG%04d", i%9000), "STN1")
	})
	t.Logf("R87: detection added %v a page; joining an event, %v", ((on - off) / 200).Round(10*time.Microsecond), ((joined - off) / 200).Round(10*time.Microsecond))
	if evs := e.largeEvents(e.user); len(evs) != 1 || evs[0].EmergencyPages != 201 {
		t.Errorf("events %+v, want 1 of 201 pages", evs)
	}
}
