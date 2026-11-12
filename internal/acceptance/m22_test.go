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

// unitsPage is an Emergency page on job naming units.
func unitsPage(job, what string, units []string, group string) string {
	return fmt.Sprintf("@@%s HSF1 %s @ 12 FICTION ST, MADEUPTON UNITS %s #%s", job, what, strings.Join(units, " "), group)
}

// units is n made-up unit names from U<from>.
func units(from, n int) []string {
	var out []string
	for i := from; i < from+n; i++ {
		out = append(out, fmt.Sprintf("U%02d", i))
	}
	return out
}

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

func levelNames(ev server.LargeEvent) []string {
	var out []string
	for _, l := range ev.Levels {
		out = append(out, l.Level)
	}
	return out
}

func levelAt(ev server.LargeEvent, level string) time.Time {
	for _, l := range ev.Levels {
		if l.Level == level {
			return l.At
		}
	}
	return time.Time{}
}

// A84. Large events open and group.
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
	// capcode: one event at level deployment.
	first := e.mustPost("a", "0100001", page1, t10).MessageID
	evs := e.largeEvents(e.user)
	if len(evs) != 1 || !slices.Equal(levelNames(evs[0]), []string{"deployment"}) {
		t.Fatalf("after DG0801: %+v, want one event at level deployment", evs)
	}
	if !levelAt(evs[0], "deployment").Equal(t10) || !evs[0].Start.Equal(t10) {
		t.Errorf("deployment met at %v, start %v, want %v", levelAt(evs[0], "deployment"), evs[0].Start, t10)
	}
	id := evs[0].ID

	// 15:00: DG0802 on job EX0000102 joins it, which reaches several
	// deployments.
	e.advance(5 * time.Hour)
	t15 := e.clock().Truncate(time.Second)
	second := e.mustPost("a", "0100002", dgPage("EX0000102", "DG0802", "STN2"), t15).MessageID
	evs = e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].ID != id || !slices.Equal(levelNames(evs[0]), []string{"deployment", "several_deployments"}) {
		t.Fatalf("after DG0802: %+v, want the same event at deployment and several deployments", evs)
	}
	if !levelAt(evs[0], "several_deployments").Equal(t15) || evs[0].Codes != 2 || evs[0].Jobs != 2 || evs[0].Pages != 2 {
		t.Errorf("event %+v: want several deployments at %v, 2 codes, 2 jobs, 2 pages", evs[0], t15)
	}
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
	// 04:00: a deployment-code page opens a new one.
	e.advance(59 * time.Minute)
	e.mustPost("a", "0100003", dgPage("EX0000103", "DG0803", "STN3"), e.clock())
	evs = e.largeEvents(e.user)
	if len(evs) != 2 || evs[1].ID != id || !slices.Equal(levelNames(evs[0]), []string{"deployment"}) {
		t.Fatalf("at 04:00: %+v, want a new event at deployment, newest first, beside the old one", evs)
	}
	t.Logf("A84: event %d %v from %v to %v; then event %d at %v", id, levelNames(evs[1]), evs[1].Start, evs[1].End, evs[0].ID, evs[0].Start)
}

// A85. A big job opens an event.
func TestA85BigJobOpensAnEvent(t *testing.T) {
	e := setup(t)
	e.clocked()
	t0 := e.clock().Add(-30 * time.Minute).Truncate(time.Second)
	// Job EX0000201's Emergency pages name 19 distinct units (one twice).
	var posted []int64
	for i, us := range [][]string{units(1, 5), units(6, 5), units(11, 5), append(units(16, 4), "U01")} {
		posted = append(posted, e.mustPost("a", fmt.Sprintf("010000%d", i+1), unitsPage("EX0000201", "SMOKE FROM FACTORY", us, fmt.Sprintf("STN%d", i+1)), t0.Add(time.Duration(i)*time.Minute)).MessageID)
	}
	// A Non-emergency follow-up of the job, which doesn't count.
	e.mustPost("a", "0100001", "HbRE EX0000201 MORE CREWS #STN1", t0.Add(5*time.Minute))
	if evs := e.largeEvents(e.user); len(evs) != 0 {
		t.Fatalf("19 units opened %d events", len(evs))
	}
	// One more page names a 20th unit: an event at level big job, holding the
	// job's pages.
	at20 := t0.Add(10 * time.Minute)
	posted = append(posted, e.mustPost("a", "0100002", unitsPage("EX0000201", "MORE CREWS NEEDED", []string{"U20"}, "STN2"), at20).MessageID)
	evs := e.largeEvents(e.user)
	if len(evs) != 1 || !slices.Equal(levelNames(evs[0]), []string{"big_job"}) || !levelAt(evs[0], "big_job").Equal(at20) {
		t.Fatalf("after the 20th unit: %+v, want one event at level big job, met at %v", evs, at20)
	}
	if evs[0].Units != 20 || evs[0].Jobs != 1 || evs[0].Codes != 0 || !evs[0].Start.Equal(t0) {
		t.Errorf("event %+v: want 20 units, 1 job, no deployment codes, starting with the job's first page", evs[0])
	}
	var held []int64
	for _, m := range e.eventPages(e.user, evs[0].ID) {
		held = append(held, m.ID)
	}
	slices.Sort(held)
	if !slices.Equal(held, posted) {
		t.Errorf("the event holds %v, want the job's Emergency pages %v", held, posted)
	}

	// Only an admin sets the threshold.
	if code, _ := e.user.call(http.MethodPut, "/api/v1/admin/large-events", server.LargeEventSettings{Units: 25}, nil); code != http.StatusForbidden {
		t.Errorf("a user setting the threshold: %d, want 403", code)
	}
	for _, n := range []int{0, 1, 1001} {
		if code, _ := e.admin.call(http.MethodPut, "/api/v1/admin/large-events", server.LargeEventSettings{Units: n}, nil); code != http.StatusUnprocessableEntity {
			t.Errorf("threshold %d: %d, want 422", n, code)
		}
	}
	var st server.LargeEventSettings
	if code, _ := e.admin.call(http.MethodPut, "/api/v1/admin/large-events", server.LargeEventSettings{Units: 25}, &st); code != http.StatusOK || st.Units != 25 {
		t.Fatalf("setting 25: %d %+v", code, st)
	}
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/large-events", nil, &st); code != http.StatusOK || st.Units != 25 {
		t.Fatalf("threshold read back: %d %+v", code, st)
	}
	// A new job reaching 20 units opens none, a day later and nearby alike.
	for _, later := range []time.Duration{0, 24 * time.Hour} {
		e.advance(later)
		job := "EX0000202"
		if later > 0 {
			job = "EX0000203"
		}
		base := e.clock().Add(-5 * time.Minute)
		for i, us := range [][]string{units(30, 10), units(40, 10)} {
			e.mustPost("a", fmt.Sprintf("010000%d", i+3), unitsPage(job, "SMOKE FROM SHED", us, "STN3"), base.Add(time.Duration(i)*time.Minute))
		}
	}
	if evs = e.largeEvents(e.user); len(evs) != 1 {
		t.Fatalf("with the threshold at 25, jobs of 20 units gave %d events, want still 1", len(evs))
	}
	t.Logf("A85: 19 units, no event; the 20th opened event %d (%d pages, %d units); at 25, 20 units opened none", evs[0].ID, evs[0].Pages, evs[0].Units)
}

// A86. Large-event integrations fire once.
func TestA86LargeEventIntegrationsFireOnce(t *testing.T) {
	e := setup(t)
	e.clocked()
	e.allowLoopback()
	e.runWorker()
	userC := e.newMember("c@example.com", accounts.RoleUser, e.admin)
	hookB, hookC := standin.Webhook(), standin.Webhook()
	defer hookB.Close()
	defer hookC.Close()
	e.createIntegration(e.user, integrations.Request{Name: "Several deployments", Kind: integrations.Webhook, LargeEvents: "several_deployments",
		Secrets: integrations.Secrets{URL: hookB.URL + "/hook"}})
	vc := e.createIntegration(userC, integrations.Request{Name: "Any deployment", Kind: integrations.Webhook, LargeEvents: "deployment",
		Content: integrations.Full, Secrets: integrations.Secrets{URL: hookC.URL + "/hook"}})
	if vc.Content != integrations.Summary || vc.LargeEvents == nil || *vc.LargeEvents != "deployment" {
		t.Errorf("C's integration: content %q, large events %v; want summary at deployment", vc.Content, vc.LargeEvents)
	}

	// 10:00: DG0801. C's webhook gets one delivery within 5 s; B's none.
	start := time.Now()
	p1 := e.mustPost("a", "0100001", dgPage("EX0000101", "DG0801", "STN1"), e.clock()).MessageID
	if !eventually(5*time.Second-time.Since(start), func() bool { return len(hookC.Requests()) == 1 }) {
		t.Fatalf("C's webhook got %d requests within 5 s, want 1", len(hookC.Requests()))
	}
	t.Logf("C's delivery %v after the page", time.Since(start).Round(time.Millisecond))
	// 15:00: DG0802. B's gets one; C's nothing more.
	e.advance(5 * time.Hour)
	start = time.Now()
	p2 := e.mustPost("a", "0100002", dgPage("EX0000102", "DG0802", "STN2"), e.clock()).MessageID
	if !eventually(5*time.Second-time.Since(start), func() bool { return len(hookB.Requests()) == 1 }) {
		t.Fatalf("B's webhook got %d requests within 5 s, want 1", len(hookB.Requests()))
	}
	t.Logf("B's delivery %v after the page", time.Since(start).Round(time.Millisecond))
	// A third deployment code joins: neither gets another.
	e.mustPost("a", "0100003", dgPage("EX0000103", "DG0803", "STN3"), e.clock())
	// And the same page from a second client (R51b).
	e.mustPost("b", "0100003", dgPage("EX0000103", "DG0803", "STN3"), e.clock().Add(2*time.Second))
	time.Sleep(1500 * time.Millisecond)
	if b, c := len(hookB.Requests()), len(hookC.Requests()); b != 1 || c != 1 {
		t.Fatalf("after DG0803: B got %d, C %d; want 1 each", b, c)
	}

	evs := e.largeEvents(e.user)
	if len(evs) != 1 {
		t.Fatalf("%d events, want 1", len(evs))
	}
	link := "http://pager.example.test/large-events/" + fmt.Sprint(evs[0].ID)
	for _, d := range []struct {
		who, level string
		page       int64
		codes      []any
		r          standin.Request
	}{
		{"C", "deployment", p1, []any{"DG0801"}, hookC.Requests()[0]},
		{"B", "several_deployments", p2, []any{"DG0801", "DG0802"}, hookB.Requests()[0]},
	} {
		got := d.r.JSON
		ev, _ := got["large_event"].(map[string]any)
		if got["event_type"] != "large_event" || got["content"] != "summary" || got["id"] != float64(d.page) || got["url"] != link || ev == nil {
			t.Errorf("%s's delivery: %s", d.who, d.r.Body)
			continue
		}
		if ev["level"] != d.level || ev["url"] != link || fmt.Sprint(ev["deployment_codes"]) != fmt.Sprint(d.codes) || ev["started_at"] == nil || ev["reached_at"] == nil {
			t.Errorf("%s's summary: %v", d.who, ev)
		}
		// Never page text (R87d): no text, address or fields, and none of it.
		for _, k := range []string{"text", "fields", "extra", "location", "address"} {
			if _, ok := got[k]; ok {
				t.Errorf("%s's delivery has %q", d.who, k)
			}
		}
		e.noPageText(d.who+"'s delivery", string(d.r.Body))
		if strings.Contains(string(d.r.Body), "SAMPLE RD") {
			t.Errorf("%s's delivery holds the address", d.who)
		}
	}
	// The Large events page lists the event with both deployment codes (and
	// the third).
	ev := e.largeEvent(e.user, evs[0].ID)
	if !slices.Contains(ev.CodeList, "DG0801") || !slices.Contains(ev.CodeList, "DG0802") || evs[0].Codes != 3 {
		t.Errorf("event %+v: want DG0801 and DG0802 among its 3 codes", ev)
	}
	// Each owner sees their delivery, carried by the page, about the event.
	ds := e.deliveries(userC, vc.ID)
	if len(ds) != 1 || ds[0].EventID == nil || *ds[0].EventID != evs[0].ID || ds[0].Status != "delivered" {
		t.Errorf("C's deliveries: %+v", ds)
	}
	t.Logf("A86: C got 1 delivery at the first code, B 1 at the second, nothing at the third: %s", hookB.Requests()[0].Body)
}

// R87d: a large-event integration's selection, content and Send test.
func TestLargeEventIntegrationRules(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	url := integrations.Secrets{URL: hook.URL + "/hook"}
	for _, req := range []integrations.Request{
		{Name: "Both", Kind: integrations.Webhook, LargeEvents: "deployment", Capcodes: []string{"0100001"}, Secrets: url},
		{Name: "Unknown level", Kind: integrations.Webhook, LargeEvents: "huge", Secrets: url},
	} {
		if code, _ := e.user.call(http.MethodPost, "/api/v1/integrations", req, nil); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d, want 422", req.Name, code)
		}
	}
	v := e.createIntegration(e.user, integrations.Request{Name: "Big jobs", Kind: integrations.Webhook, LargeEvents: "big_job", Secrets: url})
	if len(v.Capcodes)+len(v.Aliases) != 0 || v.Content != integrations.Summary {
		t.Errorf("view %+v", v)
	}
	var res integrations.TestResult
	if code, p := e.user.call(http.MethodPost, fmt.Sprintf("/api/v1/integrations/%d/test", v.ID), nil, &res); code != http.StatusOK || !res.OK {
		t.Fatalf("send test: %d %s %+v", code, p.Error, res)
	}
	got := hook.Requests()[0].JSON
	ev, _ := got["large_event"].(map[string]any)
	if got["test"] != true || got["event_type"] != "test" || ev == nil || ev["level"] != "big_job" {
		t.Errorf("test delivery: %s", hook.Requests()[0].Body)
	}
	// It never fires for a page: a page delivery's selection is capcodes and aliases.
	e.mustPost("a", "0100001", row1Text, e.now)
	var rows int
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT count(*) FROM deliveries").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("a page queued %d deliveries for a large-event integration (%v)", rows, err)
	}
	// Changed to capcodes, it's a page integration again.
	v2 := v
	var out integrations.View
	if code, p := e.user.call(http.MethodPut, fmt.Sprintf("/api/v1/integrations/%d", v2.ID), integrations.Request{Name: "Now pages", Capcodes: []string{"0100001"}}, &out); code != http.StatusOK || out.LargeEvents != nil || len(out.Aliases) != 1 { // 0100001 is Station 1 (R48)
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
		start, end, several time.Time
		pages, codes        int64
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
			out = append(out, got{ev.Start, ev.End, levelAt(ev, "several_deployments"), ev.Pages, ev.Codes})
		}
		return out
	}
	inOrder := run([]int{4, 0, 1, 2, 3, 5}, false)
	want := []got{
		{base.Add(40 * time.Hour), base.Add(40 * time.Hour), time.Time{}, 1, 1},
		{base.Add(-3 * time.Hour), base.Add(20 * time.Hour), base, 5, 5},
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
	if !evs[0].Start.Equal(t0) || !evs[0].End.Equal(t0.Add(time.Hour)) || !levelAt(evs[0], "deployment").Equal(t0.Add(10*time.Minute)) {
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
	if len(evs) != 2 || evs[0].Codes != 2 || evs[0].Jobs != 2 {
		t.Fatalf("before the run: %+v", evs)
	}
	if _, err := e.job.RunOnce(e.ctx, run); err != nil {
		t.Fatal(err)
	}
	evs = e.largeEvents(e.user)
	if len(evs) != 1 {
		t.Fatalf("after the run: %d events, want 1 (the old one gone with its pages)", len(evs))
	}
	if ev := evs[0]; ev.Pages != 1 || ev.Codes != 1 || ev.Jobs != 1 || !ev.Start.Equal(cut.Add(2*time.Hour)) ||
		!slices.Equal(levelNames(ev), []string{"deployment", "several_deployments"}) {
		t.Errorf("the thinned event: %+v; want 1 page, 1 code, 1 job, starting at its remaining page, its levels kept", ev)
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
// aliased capcodes, each on a job naming units (the pages detection reads
// most for), are posted with detection on and off; the time each takes is
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
		return unitsPage(fmt.Sprintf("EX%07d", 7000000+i), "SMOKE FROM SHED", units(i%50, 5), "STN1")
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
	if evs := e.largeEvents(e.user); len(evs) != 1 || evs[0].Pages != 201 {
		t.Errorf("events %+v, want 1 of 201 pages", evs)
	}
}
