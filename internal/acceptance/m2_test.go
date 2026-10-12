package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

func (e *env) getJSON(path string, v any) int {
	e.t.Helper()
	resp, err := e.as.do(http.MethodGet, e.url+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			e.t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func (e *env) list(query string) []server.Message {
	e.t.Helper()
	var page server.MessagePage
	if code := e.getJSON("/api/v1/messages?"+query, &page); code != http.StatusOK {
		e.t.Fatalf("GET messages?%s: %d", query, code)
	}
	return page.Messages
}

func s(p *string) string {
	if p == nil {
		return "<null>"
	}
	return *p
}

// A1. Every group's copy, one job.
func TestA1EveryGroupsCopyOneJob(t *testing.T) {
	e := setup(t)
	for n := 1; n <= 4; n++ {
		e.mustPost("a", fmt.Sprintf("010000%d", n), fmt.Sprintf("@@EX0000004 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN%d", n), e.now)
	}
	ms := e.list("")
	if len(ms) != 4 {
		t.Fatalf("%d messages stored, want 4", len(ms))
	}
	groups := map[string]bool{}
	for _, m := range ms {
		groups[s(m.Group)] = true
	}
	if len(groups) != 4 || !groups["STN1"] || !groups["STN4"] {
		t.Errorf("groups %v", groups)
	}
	var job server.Job
	if code := e.getJSON("/api/v1/jobs/EX0000004", &job); code != http.StatusOK || len(job.Messages) != 4 {
		t.Fatalf("job view: %d, %d messages", code, len(job.Messages))
	}
	t.Logf("A1: 4 messages, groups %v, job EX0000004 has %d messages", groups, len(job.Messages))
}

// A6 (search). Unknown capcode in the "capcodes with no alias" search.
func TestA6UnknownCapcodeSearch(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0999999", "QDFirst page to an unknown pager", e.now.Add(-time.Minute))
	e.mustPost("a", "0999999", "@@Second page to an unknown pager", e.now)
	e.mustPost("a", "0100001", "QDA page to Station 1", e.now)
	ms := e.list("unaliased=true")
	if len(ms) != 2 || ms[0].Capcode != "0999999" || ms[0].Alias != nil {
		t.Fatalf("unaliased search: %+v", ms)
	}
	var caps []server.UnaliasedCapcode
	e.getJSON("/api/v1/capcodes/unaliased", &caps)
	if len(caps) != 1 || caps[0].Capcode != "0999999" || caps[0].Messages != 2 || !caps[0].LastSeen.Equal(e.now) || !caps[0].FirstSeen.Equal(e.now.Add(-time.Minute)) {
		t.Fatalf("unaliased capcodes: %+v", caps)
	}
	t.Logf("A6: unaliased capcodes %+v", caps)
}

// A10. Marker hidden in display.
func TestA10MarkerHiddenInDisplay(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", row1, e.now)
	ms := e.list("")
	if len(ms) != 1 {
		t.Fatalf("%d messages", len(ms))
	}
	m := ms[0]
	if !strings.HasPrefix(m.DisplayText, "EX0000001") || s(m.Priority) != "Emergency" || m.Text != row1 {
		t.Errorf("display %q, priority %s, text %q", m.DisplayText, s(m.Priority), m.Text)
	}
	if s(m.Alias) != "Station 1" || s(m.Agency) != "Example Fire Service" || m.Labels["event_type"] != "House fire" || s(m.Category) != "Structure fire" {
		t.Errorf("alias %s, agency %s, labels %v, category %s", s(m.Alias), s(m.Agency), m.Labels, s(m.Category))
	}
}

// R33: every criterion, alone.
func TestSearchCriteria(t *testing.T) {
	e := setup(t)
	day := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	e.mustPost("a", "0100001", row1, day)
	e.mustPost("a", "0100002", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2", day.Add(24*time.Hour))
	e.mustPost("a", "0100001", "HbRE EX0000001 CREWS RETURNING #STN1", day.Add(48*time.Hour))
	e.mustPost("a", "0999999", "plain text no marker", day.Add(72*time.Hour))
	tests := []struct {
		query string
		want  int
	}{
		{"", 4},
		{"q=crews+returning", 1}, // case-insensitive substring
		{"q=" + url.QueryEscape("100%"), 0},
		{"capcode=100001", 2},
		{"alias=station+2", 1},
		{"agency=example+fire", 3},
		{"group=stn1", 2},
		{"priority=Emergency", 2},
		{"priority=none", 1},
		{"category=" + url.QueryEscape("Structure fire"), 1},
		{"category=Other", 2}, // no event type, or unmapped
		{"job=EX0000001", 2},
		{"from=2026-10-02&to=2026-10-03", 2}, // inclusive dates
		{"unaliased=true", 1},
		{"capcode=100001&priority=Non-emergency", 1},
	}
	for _, tt := range tests {
		if got := len(e.list(tt.query)); got != tt.want {
			t.Errorf("%q: %d messages, want %d", tt.query, got, tt.want)
		}
	}
	if code := e.getJSON("/api/v1/messages?priority=Urgent", nil); code != http.StatusBadRequest {
		t.Errorf("bad priority: %d", code)
	}
	if code := e.getJSON("/api/v1/messages?category=Nope", nil); code != http.StatusBadRequest {
		t.Errorf("bad category: %d", code)
	}
}

func TestPaginationNewestFirst(t *testing.T) {
	e := setup(t)
	for i := 0; i < 55; i++ {
		e.mustPost("a", "0100001", fmt.Sprintf("QDpage %02d", i), e.now.Add(time.Duration(i-60)*time.Minute))
	}
	var p1, p2 server.MessagePage
	e.getJSON("/api/v1/messages", &p1)
	if len(p1.Messages) != 50 || p1.Next == "" || p1.Messages[0].DisplayText != "page 54" {
		t.Fatalf("page 1: %d messages, next %q, first %q", len(p1.Messages), p1.Next, p1.Messages[0].DisplayText)
	}
	e.getJSON("/api/v1/messages?before="+p1.Next, &p2)
	if len(p2.Messages) != 5 || p2.Next != "" || p2.Messages[4].DisplayText != "page 00" {
		t.Fatalf("page 2: %d messages, next %q", len(p2.Messages), p2.Next)
	}
}

// R35c, R33, R39: admin-only data stays out of non-admin responses.
func TestAdminOnlyData(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", "@@GARBLED 123", e.now)
	e.mustPost("b", "0100001", "@@GARBLED 123", e.now.Add(time.Second))
	e.mustPost("a", "0100001", row1, e.now)

	ms := e.list("parse_failed=true") // ignored for users
	if len(ms) != 2 || ms[0].Flags != nil || ms[1].Flags != nil {
		t.Errorf("user: %d messages, flags %v", len(ms), ms[0].Flags)
	}
	var detail server.Message
	e.getJSON(fmt.Sprintf("/api/v1/messages/%d", ms[1].ID), &detail)
	if len(detail.Receptions) != 2 || detail.Receptions[0].Client != "Receiver 1" || detail.Flags != nil {
		t.Errorf("user detail: %+v, flags %v", detail.Receptions, detail.Flags)
	}
	if code := e.getJSON("/api/v1/admin/clients", nil); code != http.StatusForbidden {
		t.Errorf("user client status: %d", code)
	}

	e.as = e.admin
	ms = e.list("parse_failed=true")
	if len(ms) != 1 || ms[0].Flags == nil || !ms[0].Flags.ParseFailed {
		t.Fatalf("admin flag filter: %+v", ms)
	}
	e.getJSON(fmt.Sprintf("/api/v1/messages/%d", ms[0].ID), &detail)
	if detail.Receptions[0].Client != "Client A" || detail.Receptions[1].Client != "Client B" {
		t.Errorf("admin detail receptions: %+v", detail.Receptions)
	}
	var clients []server.ClientStatus
	if code := e.getJSON("/api/v1/admin/clients", &clients); code != http.StatusOK || len(clients) != 2 {
		t.Errorf("admin client status: %d %+v", code, clients)
	}
}

// R27: an ignored alias's pages are stored but hidden by default.
func TestIgnoredAlias(t *testing.T) {
	e := setup(t)
	if err := e.cat.AddAlias(e.ctx, catalog.Alias{Pattern: "0500%", Name: "Paging test", Template: "Example Fire", Ignored: true}); err != nil {
		t.Fatal(err)
	}
	e.mustPost("a", "0500001", "QDtest page", e.now)
	e.mustPost("a", "0100001", "QDreal page", e.now)
	if ms := e.list(""); len(ms) != 1 || ms[0].Capcode != "0100001" {
		t.Errorf("default list: %+v", ms)
	}
	if ms := e.list("ignored=true"); len(ms) != 2 {
		t.Errorf("with ignored: %d", len(ms))
	}
}

// R23a: alias and template changes re-parse stored messages, whichever
// tool made them (here the catalog, as the CLI does).
func TestReparseOnAliasAndTemplateChange(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0777777", "@@EX0000007 HSF1 SHED FIRE @ 3 FICTION ST, MADEUPTON #STN7", e.now)
	m := e.list("")[0]
	if m.Alias != nil || m.Format != nil || m.JobNumber == nil {
		t.Fatalf("before: alias %s, format %s, job %s", s(m.Alias), s(m.Format), s(m.JobNumber))
	}
	if err := e.cat.AddAlias(e.ctx, catalog.Alias{Pattern: "777777", Name: "Station 7", Template: "Example Fire"}); err != nil {
		t.Fatal(err)
	}
	if n, err := e.worker.RunOnce(e.ctx); err != nil || n != 1 {
		t.Fatalf("re-parse: %d, %v", n, err)
	}
	m = e.list("")[0]
	if s(m.Alias) != "Station 7" || s(m.Format) != "emergency" || s(m.Group) != "STN7" || s(m.Address) != "3 FICTION ST" {
		t.Fatalf("after alias: alias %s, format %s, group %s, address %s", s(m.Alias), s(m.Format), s(m.Group), s(m.Address))
	}
	// Renaming needs no re-parse: names are read through the join.
	if err := e.cat.UpdateAlias(e.ctx, catalog.Alias{Pattern: "0777777", Name: "Station Seven", Template: "Example Fire"}); err != nil {
		t.Fatal(err)
	}
	if m = e.list("")[0]; s(m.Alias) != "Station Seven" {
		t.Errorf("renamed alias shows %s", s(m.Alias))
	}
	// Template loses its profile: pages become text only.
	if err := e.cat.UpdateTemplate(e.ctx, catalog.Template{Name: "Example Fire", Agency: "Example Fire Service"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.worker.RunOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if m = e.list("")[0]; m.Format != nil || m.Group != nil || s(m.JobNumber) != "EX0000007" {
		t.Errorf("after template: format %s, group %s, job %s", s(m.Format), s(m.Group), s(m.JobNumber))
	}
	// Deleting the alias leaves the message unaliased.
	if err := e.cat.DeleteAlias(e.ctx, "0777777"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.worker.RunOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := e.list("unaliased=true"); len(ms) != 1 {
		t.Errorf("after delete: %d unaliased", len(ms))
	}
}

// Section 14: a profile change (new hash) queues a full re-parse once.
func TestProfileChangeQueuesReparse(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", row1, e.now)
	e.worker.ProfileHash = "one"
	if err := e.worker.CheckProfiles(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.worker.RunOnce(e.ctx); n != 1 {
		t.Errorf("first start: re-parsed %d, want 1", n)
	}
	if err := e.worker.CheckProfiles(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.worker.RunOnce(e.ctx); n != 0 {
		t.Errorf("same hash: re-parsed %d, want 0", n)
	}
	e.worker.ProfileHash = "two"
	_ = e.worker.CheckProfiles(e.ctx)
	if n, _ := e.worker.RunOnce(e.ctx); n != 1 {
		t.Errorf("changed hash: re-parsed %d, want 1", n)
	}
}

// The worker wakes on the notification a change sends, without polling.
func TestReparseWorkerWakesOnNotify(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0777777", "@@EX0000007 HSF1 SHED FIRE @ 3 FICTION ST, MADEUPTON #STN7", e.now)
	e.worker.Poll = time.Hour
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	go e.worker.Run(ctx)
	time.Sleep(300 * time.Millisecond) // let it LISTEN
	if err := e.cat.AddAlias(e.ctx, catalog.Alias{Pattern: "0777777", Name: "Station 7", Template: "Example Fire"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := e.list(""); len(m) == 1 && m[0].Format != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("message not re-parsed within 5 s of the change")
}

// stream opens a live-update connection and returns its messages.
func (e *env) stream(query string) <-chan server.Message {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.ctx)
	e.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.url+"/api/v1/stream?"+query, nil)
	req.AddCookie(e.as.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		e.t.Fatalf("stream: %v %v", err, resp)
	}
	out := make(chan server.Message, 16)
	ready := make(chan struct{})
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if line == ": connected" {
				close(ready)
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var m server.Message
				if json.Unmarshal([]byte(data), &m) == nil {
					out <- m
				}
			}
		}
	}()
	<-ready
	for e.srv.Hub.Len() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	return out
}

// A3 and A4 at the API level (the browser versions run in Playwright):
// a new page reaches unfiltered viewers within 2 s, and not viewers whose
// filter it doesn't match.
func TestLiveUpdatesRespectFilters(t *testing.T) {
	e := setup(t)
	filtered := e.stream("group=STN2")
	before := e.srv.Hub.Len()
	all := e.stream("")
	for e.srv.Hub.Len() <= before {
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	e.mustPost("a", "0100001", row1, e.now)
	select {
	case m := <-all:
		if !strings.HasPrefix(m.DisplayText, "EX0000001") {
			t.Errorf("unfiltered got %q", m.DisplayText)
		}
		t.Logf("A3: unfiltered viewer got the page %v after the post", time.Since(start))
	case <-time.After(2 * time.Second):
		t.Fatal("A3: unfiltered viewer didn't get the page within 2 s")
	}
	select {
	case m := <-filtered:
		t.Fatalf("A4: STN2 viewer got an STN1 page: %q", m.DisplayText)
	case <-time.After(5 * time.Second):
	}
	e.mustPost("a", "0100002", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2", e.now)
	select {
	case m := <-filtered:
		if s(m.Group) != "STN2" {
			t.Errorf("filtered got group %s", s(m.Group))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("STN2 viewer didn't get the STN2 page")
	}
}

// A22 (API part; the taps are browser tests). The alias view lists only
// messages from the alias's capcodes, and a capcode with no alias has its own
// summary (R35d, R35e).
func TestA22AliasAndCapcodeViews(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", row1, e.now.Add(-2*time.Minute))
	e.mustPost("a", "0100002", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2", e.now.Add(-time.Minute))
	e.mustPost("a", "0100001", "HbRE EX0000001 CREWS RETURNING #STN1", e.now)
	e.mustPost("a", "0999999", "QDFirst page to an unknown pager", e.now.Add(-3*time.Minute))
	e.mustPost("a", "0999999", "@@Second page to an unknown pager", e.now)

	ms := e.list("capcode=0100001")
	if len(ms) != 2 || ms[0].AliasID == nil {
		t.Fatalf("station 1 messages: %+v", ms)
	}
	id := *ms[0].AliasID
	var a server.AliasView
	if code := e.getJSON(fmt.Sprintf("/api/v1/aliases/%d", id), &a); code != http.StatusOK {
		t.Fatalf("alias view: %d", code)
	}
	if a.Name != "Station 1" || a.Pattern != "0100001" || s(a.Agency) != "Example Fire Service" || a.Ignored {
		t.Errorf("alias view %+v", a)
	}
	got := e.list(fmt.Sprintf("alias_id=%d", id))
	if len(got) != 2 {
		t.Fatalf("alias_id filter: %d messages, want 2", len(got))
	}
	for _, m := range got {
		if m.Capcode != "0100001" {
			t.Errorf("alias view lists capcode %s", m.Capcode)
		}
	}
	if code := e.getJSON("/api/v1/aliases/999999", nil); code != http.StatusNotFound {
		t.Errorf("missing alias: %d, want 404", code)
	}

	var c server.CapcodeView
	if code := e.getJSON("/api/v1/capcodes/999999", &c); code != http.StatusOK {
		t.Fatalf("capcode view: %d", code)
	}
	if c.Capcode != "0999999" || c.AliasID != nil || c.Messages != 2 ||
		c.FirstSeen == nil || !c.FirstSeen.Equal(e.now.Add(-3*time.Minute)) || c.LastSeen == nil || !c.LastSeen.Equal(e.now) {
		t.Errorf("capcode view %+v", c)
	}
	var aliased server.CapcodeView
	if code := e.getJSON("/api/v1/capcodes/0100001", &aliased); code != http.StatusOK || aliased.AliasID == nil || *aliased.AliasID != id {
		t.Errorf("aliased capcode: %d, %+v", code, aliased)
	}
	if code := e.getJSON("/api/v1/capcodes/0123456", nil); code != http.StatusNotFound {
		t.Errorf("unheard capcode: %d, want 404", code)
	}
	t.Logf("A22: alias %d (%s) lists %d messages; capcode 0999999 has %d messages", id, a.Name, len(got), c.Messages)
}
