package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Large events follow re-parsing, and ignored aliases don't count (owner,
// 2026-11-14; R87, R87a, R23a, R27; A87; ADR 0027 §9). Every page is made
// up, from the example network.

// reparseNow runs the re-parse worker once, as the API's worker does after a
// change: it re-parses, then rebuilds the events around the pages.
func (e *env) reparseNow() {
	e.t.Helper()
	if _, err := e.worker.RunOnce(e.ctx); err != nil {
		e.t.Fatalf("re-parse: %v", err)
	}
}

// adminAddAlias adds an alias with the fixture's template, as an admin in
// the browser does (R53).
func (e *env) adminAddAlias(capcode, name string) int64 {
	e.t.Helper()
	tpl := e.templateID("Example Fire")
	var c server.Created
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/aliases", catalog.AliasInput{Pattern: capcode, Name: name, TemplateID: &tpl}, &c); code != http.StatusCreated {
		e.t.Fatalf("add alias %s: %d %s", capcode, code, p.Error)
	}
	return c.ID
}

// setIgnored marks one of the fixture's aliases ignored, or not, as an admin
// in the browser does (R53).
func (e *env) setIgnored(capcode string, ignored bool) {
	e.t.Helper()
	var id int64
	var name string
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT id, name FROM aliases WHERE pattern = $1", capcode).Scan(&id, &name); err != nil {
		e.t.Fatal(err)
	}
	tpl := e.templateID("Example Fire")
	in := catalog.AliasInput{Pattern: capcode, Name: name, TemplateID: &tpl, Ignored: ignored}
	if code, p := e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/aliases/%d", id), in, nil); code != http.StatusOK && code != http.StatusNoContent {
		e.t.Fatalf("set %s ignored %v: %d %s", capcode, ignored, code, p.Error)
	}
}

// heldBy is an event's page IDs, oldest first.
func (e *env) heldBy(id int64) []int64 {
	e.t.Helper()
	var out []int64
	for _, m := range e.eventPages(e.user, id) {
		out = append(out, m.ID)
	}
	slices.Sort(out)
	return out
}

func (e *env) eventGone(id int64) bool {
	e.t.Helper()
	code, _ := e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/large-events/%d", id), nil, nil)
	return code == http.StatusNotFound
}

// deliveryRows counts deliveries, and those still waiting to be sent.
func (e *env) deliveryRows() (all, pending int) {
	e.t.Helper()
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT count(*), count(*) FILTER (WHERE status = 'pending') FROM deliveries").Scan(&all, &pending); err != nil {
		e.t.Fatal(err)
	}
	return all, pending
}

// A87 (owner, 2026-11-14): a capcode aliased after its pages arrived makes
// them form the event live ingest would have made, and nothing is sent.
func TestA87AliasAddedLater(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	hook := standin.Webhook()
	defer hook.Close()
	e.createIntegration(e.user, integrations.Request{Name: "Large events", Kind: integrations.Webhook, LargeEvents: true,
		Secrets: integrations.Secrets{URL: hook.URL + "/hook"}})

	// Job EX0000801's 20 Emergency pages to 0100009, which has no alias, and
	// a page with a code to 0100008, which has none either: no event.
	t0 := e.now.Add(-90 * time.Minute)
	var job []int64
	for i := range 20 {
		job = append(job, e.mustPost("a", "0100009", jobPage("EX0000801", i+1, "STN9"), t0.Add(time.Duration(i)*time.Minute)).MessageID)
	}
	coded := e.mustPost("a", "0100008", dgPage("EX0000802", "DG0861", "STN8"), t0.Add(30*time.Minute)).MessageID
	if evs := e.largeEvents(e.user); len(evs) != 0 {
		t.Fatalf("unaliased pages opened %d events", len(evs))
	}

	// 0100009 is aliased: the re-parse gives the job's event, from its first page.
	e.adminAddAlias("0100009", "Station 9")
	e.reparseNow()
	evs := e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].EmergencyPages != 20 || evs[0].Jobs != 1 || !evs[0].Start.Equal(t0) || !evs[0].End.Equal(t0.Add(19*time.Minute)) {
		t.Fatalf("after aliasing 0100009: %+v, want one event of the job's 20 pages from %v", evs, t0)
	}
	id := evs[0].ID
	if got := e.heldBy(id); !slices.Equal(got, job) {
		t.Errorf("the event holds %v, want the job's pages %v (not the unaliased page with a code)", got, job)
	}

	// 0100008 is aliased too: its page with a code joins the same event.
	e.adminAddAlias("0100008", "Station 8")
	e.reparseNow()
	evs = e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].ID != id || evs[0].EmergencyPages != 21 || evs[0].Jobs != 2 {
		t.Fatalf("after aliasing 0100008: %+v, want event %d with 21 pages and 2 jobs", evs, id)
	}
	if got := e.heldBy(id); !slices.Contains(got, coded) {
		t.Errorf("the event holds %v, without the page with a code %d", got, coded)
	}

	// A rebuild sends nothing (R87d: only live ingest is told of events).
	time.Sleep(1500 * time.Millisecond)
	if n, _ := e.deliveryRows(); n != 0 || len(hook.Requests()) != 0 {
		t.Errorf("the rebuild queued %d deliveries and the webhook got %d requests; want none", n, len(hook.Requests()))
	}
	t.Logf("A87: aliasing 0100009 later made event %d of its job's 20 pages; aliasing 0100008 added its page with a code; nothing sent", id)
}

// A87 (owner, 2026-11-14): an alias marked ignored takes its pages out of
// events, live and rebuilt. An event left with no pages goes, a job that
// drops below the threshold takes its event with it unless something else
// holds it, and nothing is sent; unmarking it brings them back.
func TestA87IgnoredAliasLeavesEvents(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	hook := standin.Webhook()
	defer hook.Close()
	v := e.createIntegration(e.user, integrations.Request{Name: "Large events", Kind: integrations.Webhook, LargeEvents: true,
		Secrets: integrations.Secrets{URL: hook.URL + "/hook"}})

	base := e.now.Add(-72 * time.Hour)
	// E1: one page with a code, to Station 1 (0100001).
	e.mustPost("a", "0100001", dgPage("EX0000811", "DG0871", "STN1"), base)
	// E2, a day later: job EX0000812's 20 Emergency pages, 5 to each station.
	for i := range 20 {
		e.mustPost("a", aliased(i), jobPage("EX0000812", i+1, "STN2"), base.Add(24*time.Hour+time.Duration(i)*time.Minute))
	}
	// E3, now: job EX0000813's 20 pages (5 to Station 1), then a page with a
	// code on job EX0000814 to Station 2.
	t3 := e.now.Add(-4 * time.Minute)
	var k []int64
	for i := range 20 {
		k = append(k, e.mustPost("a", aliased(i), jobPage("EX0000813", i+1, "STN3"), t3.Add(time.Duration(i)*10*time.Second)).MessageID)
	}
	lp := e.mustPost("a", "0100002", dgPage("EX0000814", "DG0873", "STN2"), e.now.Add(-30*time.Second)).MessageID
	evs := e.largeEvents(e.user)
	if len(evs) != 3 || evs[0].EmergencyPages != 21 || evs[1].EmergencyPages != 20 || evs[2].EmergencyPages != 1 {
		t.Fatalf("before: %+v, want E3 (21 pages), E2 (20) and E1 (1)", evs)
	}
	e3, e2, e1 := evs[0].ID, evs[1].ID, evs[2].ID
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) == 1 }) {
		t.Fatalf("E3's opening: %d requests, want 1", len(hook.Requests()))
	}
	if n, _ := e.deliveryRows(); n != 3 {
		t.Fatalf("%d deliveries, want one per event (E1's and E2's too old to send)", n)
	}

	// Station 1 is marked ignored: its pages leave every event.
	e.setIgnored("0100001", true)
	e.reparseNow()
	evs = e.largeEvents(e.user)
	if len(evs) != 1 || evs[0].ID != e3 || evs[0].EmergencyPages != 1 || evs[0].Jobs != 1 {
		t.Fatalf("after ignoring Station 1: %+v, want only E3 (%d) holding the page with a code", evs, e3)
	}
	if got := e.heldBy(e3); !slices.Equal(got, []int64{lp}) {
		t.Errorf("E3 holds %v, want only %d: job EX0000813 has 15 pages that count, below 20", got, lp)
	}
	if ev := e.largeEvent(e.user, e3); !slices.Equal(ev.JobList, []string{"EX0000814"}) {
		t.Errorf("E3's jobs: %v", ev.JobList)
	}
	// E1 had only Station 1's page; E2's job dropped to 15 pages. Their links
	// find nothing.
	if !e.eventGone(e1) || !e.eventGone(e2) {
		t.Errorf("E1 (%d) or E2 (%d) is still there", e1, e2)
	}

	// Live: Station 1's pages don't count. A page with a code opens nothing,
	// and a new job's 20 pages, 5 of them to Station 1, open nothing.
	e.mustPost("a", "0100001", dgPage("EX0000815", "DG0875", "STN1"), e.now)
	jt := e.now.Add(-3 * time.Minute)
	for i := range 20 {
		e.mustPost("a", aliased(i), jobPage("EX0000816", i+1, "STN4"), jt.Add(time.Duration(i)*5*time.Second))
	}
	if evs = e.largeEvents(e.user); len(evs) != 1 || evs[0].EmergencyPages != 1 {
		t.Fatalf("live pages to an ignored alias counted: %+v", evs)
	}

	// Nothing was sent, and the deliveries stay: E3's opening page (job
	// EX0000813's 20th) is in no event now, so its delivery is about none.
	time.Sleep(1500 * time.Millisecond)
	if all, pending := e.deliveryRows(); all != 3 || pending != 0 || len(hook.Requests()) != 1 {
		t.Errorf("after ignoring: %d deliveries (%d pending), %d requests; want the 3 from before, none pending, 1 request", all, pending, len(hook.Requests()))
	}
	for _, d := range e.deliveries(e.user, v.ID) {
		if d.MessageID == k[19] && (d.EventID != nil || d.Status != "delivered") {
			t.Errorf("E3's delivery %+v: want delivered, about no event", d)
		}
	}

	// Unmarked, Station 1's pages count again: the events are back, live
	// ingest's as if it had never been ignored, and still nothing is sent.
	e.setIgnored("0100001", false)
	e.reparseNow()
	evs = e.largeEvents(e.user)
	if len(evs) != 3 || evs[0].ID != e3 || evs[0].Jobs != 4 || evs[1].EmergencyPages != 20 || evs[2].EmergencyPages != 1 {
		t.Fatalf("after unmarking: %+v, want E3 again (jobs EX0000813-816), E2 and E1", evs)
	}
	if evs[1].ID == e2 || evs[2].ID == e1 || !e.eventGone(e1) {
		t.Errorf("E1 and E2 came back as %d and %d; their old IDs %d and %d went with them", evs[2].ID, evs[1].ID, e1, e2)
	}
	time.Sleep(1500 * time.Millisecond)
	if all, pending := e.deliveryRows(); all != 3 || pending != 0 || len(hook.Requests()) != 1 {
		t.Errorf("after unmarking: %d deliveries (%d pending), %d requests; want 3, 0, 1", all, pending, len(hook.Requests()))
	}
	for _, d := range e.deliveries(e.user, v.ID) {
		if d.MessageID == k[19] && (d.EventID == nil || *d.EventID != e3) {
			t.Errorf("E3's delivery %+v: want about E3 again", d)
		}
	}
	t.Logf("A87: ignoring Station 1 left E3 (%d) with 1 page and removed E1 (%d) and E2 (%d); unmarking gave E3 with %d pages and E1, E2 as %d, %d; no delivery", e3, e1, e2, evs[0].EmergencyPages, evs[2].ID, evs[1].ID)
}

// A87, R5 (owner, 2026-11-14): a network profile that gains the code
// pattern regroups the pages already stored at its next start.
func TestA87CodePatternAdded(t *testing.T) {
	e := setup(t)
	without := *e.srv.Ingest.Events.Network
	without.DeploymentCodes = nil
	e.srv.Ingest.Events = &events.Detector{Network: &without, Fire: e.integ.EnqueueEvent}
	t0 := e.now.Add(-time.Hour)
	p1 := e.mustPost("a", "0100001", dgPage("EX0000821", "DG0881", "STN1"), t0).MessageID
	p2 := e.mustPost("a", "0100002", dgPage("EX0000822", "DG0882", "STN2"), t0.Add(10*time.Minute)).MessageID
	if evs := e.largeEvents(e.user); len(evs) != 0 {
		t.Fatalf("with no pattern: %d events", len(evs))
	}
	// The profiles changed: the start re-parses everything (section 14).
	e.worker.ProfileHash = "with a code pattern"
	if err := e.worker.CheckProfiles(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.reparseNow()
	evs := e.largeEvents(e.user)
	if len(evs) != 1 || !slices.Equal(e.heldBy(evs[0].ID), []int64{p1, p2}) || !evs[0].Start.Equal(t0) {
		t.Fatalf("after the pattern was added: %+v, want one event of both pages from %v", evs, t0)
	}
}

// R87, R23a: a rebuild is bounded and cheap. A busy month of made-up pages
// (1,000 a day, or PA_REGROUP_COST_PER_DAY; every one an Emergency page on
// a job to an aliased capcode, so every one counts, with a few codes and big
// jobs) is grouped, then one alias's template changes, which re-parses its
// quarter of the month and rebuilds the events around those pages. The
// events come out the same, the times are logged, and each batch holds the
// lock (and so delays a live page) for well under a second.
func TestRegroupCost(t *testing.T) {
	e := setup(t)
	const days = 30
	perDay := 1000
	if s := os.Getenv("PA_REGROUP_COST_PER_DAY"); s != "" {
		var err error
		if perDay, err = strconv.Atoi(s); err != nil || perDay < 1000 {
			t.Fatalf("PA_REGROUP_COST_PER_DAY %q: a whole number, 1000 or more", s)
		}
	}
	n := days * perDay
	start := e.now.Add(-time.Duration(days+1) * 24 * time.Hour).Truncate(time.Hour)
	began := time.Now()
	// Page i, on job i/5, to Station i%4+1. Every 6,000th carries a code; the
	// 25 pages from every 7,000th are one big job.
	if _, err := e.st.Pool.Exec(e.ctx, `
		WITH g AS (
			SELECT i, $1::timestamptz + i * (interval '1 day' / $2) AS at,
			       CASE WHEN i % 7000 < 25 THEN 'EX' || (9000000 + i / 7000) ELSE 'EX' || (8000000 + i / 5) END AS job
			FROM generate_series(0, $3 - 1) i
		)
		INSERT INTO messages (capcode, text, priority, sent_at, dedup_key, alias_id, job_number)
		SELECT '010000' || (i % 4 + 1),
		       CASE WHEN i % 6000 = 3000 THEN '@@' || job || ' GRS3 DG' || lpad((i / 6000)::text, 4, '0') || ' GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1'
		            ELSE '@@' || job || ' HSF1 SMOKE FROM FACTORY UPDATE ' || i || ' @ 12 FICTION ST, MADEUPTON #STN1' END,
		       'Emergency', at, decode(md5(i::text), 'hex'),
		       (SELECT id FROM aliases WHERE pattern = '010000' || (i % 4 + 1)), job
		FROM g`, start, perDay, n); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO receptions (message_id, client_id, received_at) SELECT id, 'a', sent_at FROM messages`); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d pages over %d days stored in %v", n, days, time.Since(began).Round(time.Millisecond))

	// The month's events, as a rebuild of every day makes them.
	var all events.Affected
	for d := range days + 1 {
		all.Add(start.Add(time.Duration(d) * 24 * time.Hour))
	}
	r := &events.Regrouper{Network: e.srv.Ingest.Network}
	began = time.Now()
	built, err := r.Rebuild(e.ctx, e.st, &all)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the whole month grouped in %v: %+v", time.Since(began).Round(time.Millisecond), built)
	snapshot := func() string {
		rows, err := e.st.Pool.Query(e.ctx, `SELECT e.id, e.first_at, e.last_at, count(lem.message_id)
			FROM large_events e LEFT JOIN large_event_messages lem ON lem.event_id = e.id GROUP BY e.id ORDER BY e.id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var id, pages int64
			var first, last time.Time
			if err := rows.Scan(&id, &first, &last, &pages); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%d %s %s %d\n", id, first.UTC().Format(time.RFC3339), last.UTC().Format(time.RFC3339), pages)
		}
		return b.String()
	}
	before := snapshot()
	if c := strings.Count(before, "\n"); c < 4 {
		t.Fatalf("the month has %d events, want several (its codes and big jobs):\n%s", c, before)
	}

	// Station 1's template changes: its quarter of the pages is re-parsed, and the
	// events around them rebuilt.
	agency := "example-fire"
	if _, err := e.st.CreateTemplate(e.ctx, db.CreateTemplateParams{Name: "Other Fire", Agency: "Other Fire Service", AgencyProfile: &agency}); err != nil {
		t.Fatal(err)
	}
	if err := e.catSet("0100001", "Station 1", "Other Fire"); err != nil {
		t.Fatal(err)
	}
	began = time.Now()
	reparsed, err := e.worker.RunOnce(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	total := time.Since(began)
	var line struct {
		Batches, Pages, Events, Created, Deleted, Moved int
		MS                                              int64 `json:"ms"`
		Longest                                         int64 `json:"longest_batch_ms"`
	}
	logs := strings.Split(e.logs.String(), "\n")
	for i := len(logs) - 1; i >= 0; i-- {
		if strings.Contains(logs[i], `"msg":"rebuilt large events"`) {
			if err := json.Unmarshal([]byte(logs[i]), &line); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	t.Logf("R87 rebuild cost: %d pages re-parsed in %v; the rebuild took %d ms of it in %d batches (the longest %d ms), reading %d pages that count, %d events, %d created, %d deleted, %d pages moved",
		reparsed, total.Round(time.Millisecond), line.MS, line.Batches, line.Longest, line.Pages, line.Events, line.Created, line.Deleted, line.Moved)
	if reparsed != n/4 || line.Batches == 0 {
		t.Fatalf("re-parsed %d, %d batches; want %d and some", reparsed, line.Batches, n/4)
	}
	if after := snapshot(); after != before {
		t.Errorf("a template change that changes no job or code changed the events:\nbefore\n%s\nafter\n%s", before, after)
	}
	if line.Created+line.Deleted+line.Moved != 0 {
		t.Errorf("the rebuild wrote %d new events, %d deletions and %d moves; want none", line.Created, line.Deleted, line.Moved)
	}
	// The budget: the rebuild costs less than the re-parse it follows, and a
	// batch holds the lock for under a second (R51a gives a live page 5).
	if time.Duration(line.MS)*time.Millisecond > total/2 || line.Longest > 1000 {
		t.Errorf("rebuild %d ms of %v, the longest batch %d ms: over budget", line.MS, total, line.Longest)
	}
}
