package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// M6: aliases in the browser (R53-R58, ADR 0007).

// private marks text (names, notes, feedback) that must never be logged:
// the log check at the end of every test looks for it as for page text.
func (e *env) private(s string) string {
	e.posts = append(e.posts, s)
	return s
}

func (e *env) templateID(name string) int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT id FROM templates WHERE name = $1", name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func ptr[T any](v T) *T { return &v }

// request submits an alias request as m and returns its id.
func (e *env) request(m *member, in catalog.RequestInput) int64 {
	e.t.Helper()
	var c server.Created
	if code, p := m.call(http.MethodPost, "/api/v1/alias-requests", in, &c); code != http.StatusCreated {
		e.t.Fatalf("%s requests %+v: %d %s", m.email, in, code, p.Error)
	}
	return c.ID
}

func (e *env) myRequests(m *member) []catalog.RequestView {
	e.t.Helper()
	var out []catalog.RequestView
	if code, p := m.call(http.MethodGet, "/api/v1/alias-requests", nil, &out); code != http.StatusOK {
		e.t.Fatalf("my requests: %d %s", code, p.Error)
	}
	return out
}

func findRequest(rs []catalog.RequestView, id int64) *catalog.RequestView {
	for i := range rs {
		if rs[i].ID == id {
			return &rs[i]
		}
	}
	return nil
}

// myRequest is one of m's requests as m sees it.
func (e *env) myRequest(m *member, id int64) catalog.RequestView {
	e.t.Helper()
	r := findRequest(e.myRequests(m), id)
	if r == nil {
		e.t.Fatalf("request %d isn't in %s's requests", id, m.email)
	}
	return *r
}

func (e *env) waitingCount(m *member) *int64 {
	e.t.Helper()
	var meta server.Meta
	if code, p := m.call(http.MethodGet, "/api/v1/meta", nil, &meta); code != http.StatusOK {
		e.t.Fatalf("meta: %d %s", code, p.Error)
	}
	return meta.WaitingRequests
}

func (e *env) adminAlias(id int64) server.AdminAlias {
	e.t.Helper()
	var a server.AdminAlias
	if code, p := e.admin.call(http.MethodGet, fmt.Sprintf("/api/v1/admin/aliases/%d", id), nil, &a); code != http.StatusOK {
		e.t.Fatalf("admin alias %d: %d %s", id, code, p.Error)
	}
	return a
}

func (e *env) aliasName(pattern string) string {
	e.t.Helper()
	var name string
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT name FROM aliases WHERE pattern_key = $1", pattern).Scan(&name); err != nil {
		return "<none>"
	}
	return name
}

func (e *env) adminEdit(id int64, in catalog.AliasInput) {
	e.t.Helper()
	if code, p := e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/aliases/%d", id), in, nil); code != http.StatusNoContent {
		e.t.Fatalf("admin edits alias %d: %d %s", id, code, p.Error)
	}
}

// A42. Admins edit aliases (R53): a rename shows at once in the message
// list; a second alias for a capcode is refused with the reason (R27a).
func TestA42AdminEditsAliases(t *testing.T) {
	e := setup(t)
	tpl := e.templateID("Example Fire")
	e.mustPost("a", "0100001", "@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1", time.Now().UTC())
	station1 := e.aliasID("Station 1")

	e.adminEdit(station1, catalog.AliasInput{Pattern: "0100001", Name: e.private("Station One"), TemplateID: &tpl})
	// No re-parse runs here: the name is read through the join (R23a).
	msgs := e.list("")
	if len(msgs) != 1 || s(msgs[0].Alias) != "Station One" {
		t.Fatalf("message list after the rename: %+v", msgs)
	}
	t.Logf("A42: renamed; the message list shows %q for 0100001 at once", s(msgs[0].Alias))

	code, p := e.admin.call(http.MethodPost, "/api/v1/admin/aliases", catalog.AliasInput{Pattern: "100001", Name: e.private("Another station"), TemplateID: &tpl}, nil)
	t.Logf("A42: second alias for 100001: %d %q", code, p.Error)
	if code != http.StatusConflict || p.Code != "duplicate_alias" || !strings.Contains(p.Error, `"Station One"`) || !strings.Contains(p.Error, "edit") {
		t.Errorf("second alias: %d %+v", code, p)
	}
	if got := e.aliasName("0100001"); got != "Station One" {
		t.Errorf("0100001 is now %q", got)
	}
	// The change is in history, as made by the admin (R57).
	h := e.adminAlias(station1).History
	if len(h) != 1 || h[0].Action != "changed" || h[0].Source != catalog.SourceAdmin || !strings.Contains(h[0].MadeBy, "admin@example.com") ||
		h[0].RequestedBy != nil || h[0].Changes["name"] != [2]any{"Station 1", "Station One"} {
		t.Errorf("history: %+v", h)
	}
	// Moving an alias to a capcode that has one is refused the same way.
	code, p = e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/aliases/%d", station1), catalog.AliasInput{Pattern: "0100002", Name: "Station One", TemplateID: &tpl}, nil)
	if code != http.StatusConflict || !strings.Contains(p.Error, `"Station 2"`) {
		t.Errorf("move onto Station 2: %d %+v", code, p)
	}
}

// A43. A request approved: the Admin link counts it, approval creates the
// alias, history names who asked and who approved, and the requester sees
// "approved".
func TestA43RequestApproved(t *testing.T) {
	e := setup(t)
	b := e.user
	tpl := e.templateID("Example Fire")
	if n := e.waitingCount(e.admin); n == nil || *n != 0 {
		t.Fatalf("waiting before: %v", n)
	}
	id := e.request(b, catalog.RequestInput{Kind: catalog.KindAdd, Pattern: "100009", Name: ptr(e.private("Station 9")), TemplateID: &tpl, Note: e.private("New pager at station 9")})
	n := e.waitingCount(e.admin)
	t.Logf("A43: admin's count after B's request: %d; B's meta has none: %v", *n, e.waitingCount(b) == nil)
	if *n != 1 || e.waitingCount(b) != nil {
		t.Fatalf("waiting count: admin %d, B %v", *n, e.waitingCount(b))
	}
	var waiting []catalog.RequestView
	e.admin.call(http.MethodGet, "/api/v1/admin/alias-requests", nil, &waiting)
	if len(waiting) != 1 || waiting[0].Pattern != "0100009" || *waiting[0].Proposed.Name != "Station 9" || !strings.Contains(waiting[0].RequestedBy, "user@example.com") ||
		waiting[0].Note == nil || *waiting[0].Note != "New pager at station 9" {
		t.Fatalf("admin's waiting list: %+v", waiting)
	}

	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/approve", id), nil, nil); code != http.StatusNoContent {
		t.Fatalf("approve: %d %s", code, p.Error)
	}
	if got := e.aliasName("0100009"); got != "Station 9" {
		t.Fatalf("0100009 after approval: %q", got)
	}
	aliasID := e.aliasID("Station 9")
	var view server.AliasView
	e.admin.call(http.MethodGet, fmt.Sprintf("/api/v1/aliases/%d", aliasID), nil, &view)
	if len(view.History) != 1 {
		t.Fatalf("history: %+v", view.History)
	}
	h := view.History[0]
	t.Logf("A43: history: %s by %q, requested by %q, source %s", h.Action, h.MadeBy, s(h.RequestedBy), h.Source)
	if h.Action != "added" || h.Source != catalog.SourceRequest || !strings.Contains(h.MadeBy, "admin@example.com") ||
		h.RequestedBy == nil || !strings.Contains(*h.RequestedBy, "user@example.com") || h.RequestID == nil || *h.RequestID != id {
		t.Errorf("history entry: %+v", h)
	}
	r := e.myRequest(b, id)
	t.Logf("A43: B's request: %s", r.Status)
	if r.Status != catalog.StatusApproved || r.AliasID == nil || *r.AliasID != aliasID || r.RequestedBy != "" || r.ReviewedBy != "" {
		t.Errorf("B's request: %+v", r)
	}
	if n := e.waitingCount(e.admin); *n != 0 {
		t.Errorf("waiting after approval: %d", *n)
	}
	// History is for admins only (owner, 2026-10-16): B's alias view has no
	// history field at all.
	code, body := b.raw(http.MethodGet, fmt.Sprintf("/api/v1/aliases/%d", aliasID), nil)
	if code != http.StatusOK || strings.Contains(body, `"history"`) || strings.Contains(body, "admin@example.com") {
		t.Errorf("B's alias view: %d %s", code, body)
	}
}

// A44. A request rejected: feedback is required, the alias is unchanged,
// and the requester sees "rejected" with the feedback.
func TestA44RequestRejected(t *testing.T) {
	e := setup(t)
	b := e.user
	station1 := e.aliasID("Station 1")
	id := e.request(b, catalog.RequestInput{Kind: catalog.KindChange, AliasID: &station1, Name: ptr(e.private("Station 7"))})

	code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/reject", id), server.Feedback{Feedback: "  "}, nil)
	t.Logf("A44: reject without feedback: %d %q", code, p.Error)
	if code != http.StatusUnprocessableEntity || p.Code != "feedback_required" {
		t.Errorf("reject without feedback: %d %+v", code, p)
	}
	if r := e.myRequest(b, id); r.Status != catalog.StatusWaiting {
		t.Errorf("after refused rejection: %s", r.Status)
	}
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/reject", id), server.Feedback{Feedback: e.private("Wrong station")}, nil); code != http.StatusNoContent {
		t.Fatalf("reject: %d %s", code, p.Error)
	}
	if got := e.aliasName("0100001"); got != "Station 1" {
		t.Errorf("alias after rejection: %q", got)
	}
	r := e.myRequest(b, id)
	t.Logf("A44: B's request: %s, feedback %q; alias still %q", r.Status, s(r.Feedback), e.aliasName("0100001"))
	if r.Status != catalog.StatusRejected || r.Feedback == nil || *r.Feedback != "Wrong station" {
		t.Errorf("B's request: %+v", r)
	}
	// A rejection is final (R56) and writes no history (R57).
	if code, _ := b.call(http.MethodPut, fmt.Sprintf("/api/v1/alias-requests/%d", id), catalog.RequestInput{Name: ptr("Station 8")}, nil); code != http.StatusConflict {
		t.Errorf("editing a rejected request: %d", code)
	}
	if h := e.adminAlias(station1).History; len(h) != 0 {
		t.Errorf("history after a rejection: %+v", h)
	}
}

// A45. Out-of-date requests: a rename request after the admin renamed
// the alias is marked "changed since requested" with the current and
// proposed names; a request for a capcode that got an alias meanwhile
// can't be approved.
func TestA45OutOfDateRequests(t *testing.T) {
	e := setup(t)
	b := e.user
	tpl := e.templateID("Example Fire")
	station1 := e.aliasID("Station 1")
	rename := e.request(b, catalog.RequestInput{Kind: catalog.KindChange, AliasID: &station1, Name: ptr(e.private("Station 7"))})
	if r := e.myRequest(b, rename); r.ChangedSince {
		t.Fatalf("marked before any change: %+v", r)
	}
	e.adminEdit(station1, catalog.AliasInput{Pattern: "0100001", Name: "Station One", TemplateID: &tpl, Ignored: true})
	r := e.myRequest(b, rename)
	t.Logf("A45: B's request: changed since requested %v, current %q, proposed %q", r.ChangedSince, r.Current.Name, s(r.Proposed.Name))
	if !r.ChangedSince || r.Current == nil || r.Current.Name != "Station One" || s(r.Proposed.Name) != "Station 7" {
		t.Errorf("B's request after the admin's rename: %+v", r)
	}
	var waiting []catalog.RequestView
	e.admin.call(http.MethodGet, "/api/v1/admin/alias-requests", nil, &waiting)
	if w := findRequest(waiting, rename); w == nil || !w.ChangedSince {
		t.Errorf("admin's view: %+v", w)
	}
	// Approving applies exactly what was asked: the name. The ignored flag
	// the admin set stays.
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/approve", rename), nil, nil); code != http.StatusNoContent {
		t.Fatalf("approve rename: %d %s", code, p.Error)
	}
	a := e.adminAlias(station1)
	if a.Name != "Station 7" || !a.Ignored {
		t.Errorf("after approving the rename: %q ignored=%v", a.Name, a.Ignored)
	}

	add := e.request(b, catalog.RequestInput{Kind: catalog.KindAdd, Pattern: "0100009", Name: ptr(e.private("Station 9")), TemplateID: &tpl})
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/aliases", catalog.AliasInput{Pattern: "0100009", Name: e.private("Nine"), TemplateID: &tpl}, nil); code != http.StatusCreated {
		t.Fatalf("admin adds 0100009: %d %s", code, p.Error)
	}
	if r := e.myRequest(b, add); r.NowAliasedAs == nil || *r.NowAliasedAs != "Nine" {
		t.Errorf("B's add request isn't marked: %+v", r)
	}
	code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/approve", add), nil, nil)
	t.Logf("A45: approving B's request for 0100009: %d %q", code, p.Error)
	if code != http.StatusConflict || p.Code != "duplicate_alias" || !strings.Contains(p.Error, `"Nine"`) {
		t.Errorf("approve: %d %+v", code, p)
	}
	if r := e.myRequest(b, add); r.Status != catalog.StatusWaiting {
		t.Errorf("after refused approval: %s", r.Status)
	}
	if got := e.aliasName("0100009"); got != "Nine" {
		t.Errorf("0100009: %q", got)
	}
}

// A46. Import doesn't overwrite (R58): a browser rename survives a CSV
// that still has the old name, the difference is listed, new capcodes are
// added, and an export imports with no differences.
func TestA46ImportDoesntOverwrite(t *testing.T) {
	e := setup(t)
	tpl := e.templateID("Example Fire")
	e.adminEdit(e.aliasID("Station 1"), catalog.AliasInput{Pattern: "0100001", Name: "Station One", TemplateID: &tpl})
	csvText := "id,address,alias,agency,icon,color,ignore\n1,0100001,Station 1,Example Fire,,,\n2,0100010,Station 10,Example Fire,,,\n"
	res, err := e.cat.ImportCSV(e.ctx, strings.NewReader(csvText), catalog.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("A46: import: added %d, differences %+v", res.AliasesAdded, res.Differences)
	if got := e.aliasName("0100001"); got != "Station One" {
		t.Errorf("0100001 after import: %q", got)
	}
	if len(res.Differences) != 1 || res.Differences[0].Pattern != "0100001" || res.Differences[0].Changes[0] != `name: "Station One" → "Station 1"` {
		t.Errorf("differences: %+v", res.Differences)
	}
	if res.AliasesAdded != 1 || e.aliasName("0100010") != "Station 10" {
		t.Errorf("0100010 not added: %+v", res)
	}
	if h := e.adminAlias(e.aliasID("Station 10")).History; len(h) != 1 || h[0].Source != catalog.SourceImport || h[0].MadeBy != "import" {
		t.Errorf("import's history entry: %+v", h)
	}

	// A name a spreadsheet would run as a formula, from the command line.
	if err := e.cat.AddAlias(e.ctx, catalog.Alias{Pattern: "0100011", Name: "=1+2", Template: "Example Fire"}); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.user.raw(http.MethodGet, "/api/v1/admin/aliases/export", nil); code != http.StatusForbidden {
		t.Errorf("user exports: %d", code)
	}
	resp, err := e.admin.do(http.MethodGet, e.url+"/api/v1/admin/aliases/export", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	export := string(b)
	t.Logf("A46: export (%s, %s):\n%s", resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"), export)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(export, "id,address,alias,agency,icon,color,ignore\r\n") ||
		!strings.Contains(export, ",0100001,Station One,Example Fire,,,false\r\n") || !strings.Contains(export, ",0100011,'=1+2,Example Fire,,,false\r\n") {
		t.Errorf("export: %d", resp.StatusCode)
	}
	again, err := e.cat.ImportCSV(e.ctx, strings.NewReader(export), catalog.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("A46: export imported: added %d, unchanged %d, differences %d", again.AliasesAdded, again.Unchanged, len(again.Differences))
	if len(again.Differences) != 0 || again.AliasesAdded != 0 || again.Unchanged != 6 {
		t.Errorf("re-import of the export: %+v", again)
	}
}

// A47. Users can't edit aliases directly, and deactivating a user
// withdraws their waiting requests.
func TestA47UsersCantEditDirectly(t *testing.T) {
	e := setup(t)
	b := e.user
	tpl := e.templateID("Example Fire")
	station1 := e.aliasID("Station 1")
	id := e.request(b, catalog.RequestInput{Kind: catalog.KindChange, AliasID: &station1, Name: ptr(e.private("Station 7"))})
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, fmt.Sprintf("/api/v1/admin/aliases/%d", station1), catalog.AliasInput{Pattern: "0100001", Name: "Station 7", TemplateID: &tpl}},
		{http.MethodPost, "/api/v1/admin/aliases", catalog.AliasInput{Pattern: "0100009", Name: "Station 9", TemplateID: &tpl}},
		{http.MethodDelete, fmt.Sprintf("/api/v1/admin/aliases/%d", station1), nil},
		{http.MethodGet, "/api/v1/admin/aliases", nil},
		{http.MethodGet, "/api/v1/admin/alias-requests", nil},
		{http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/approve", id), nil},
		{http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/reject", id), server.Feedback{Feedback: "x"}},
	} {
		code, _ := b.call(c.method, c.path, c.body, nil)
		t.Logf("A47: user B %s %s: %d", c.method, c.path, code)
		if code != http.StatusForbidden {
			t.Errorf("user B %s %s: %d, want 403", c.method, c.path, code)
		}
	}
	if got := e.aliasName("0100001"); got != "Station 1" {
		t.Errorf("alias changed: %q", got)
	}
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", b.id), server.DeactivateRequest{}, nil); code != http.StatusOK {
		t.Fatalf("deactivate B: %d %s", code, p.Error)
	}
	var closed []catalog.RequestView
	e.admin.call(http.MethodGet, "/api/v1/admin/alias-requests?status=closed", nil, &closed)
	r := findRequest(closed, id)
	if r == nil {
		t.Fatalf("B's request is still waiting after B was deactivated")
	}
	t.Logf("A47: after deactivating B, the request is %q", r.Status)
	if r.Status != catalog.StatusWithdrawn {
		t.Errorf("B's request after deactivation: %+v", r)
	}
	if n := e.waitingCount(e.admin); *n != 0 {
		t.Errorf("waiting: %d", *n)
	}
}

// People and requests over time (ADR 0007 §5): an invite tree's
// deactivation, deleting an account, and alias deletion.
func TestM6PeopleAndRequestsOverTime(t *testing.T) {
	e := setup(t)
	tpl := e.templateID("Example Fire")
	b := e.user
	c := e.inviteAndEnrol(b, "c@example.com")
	d := e.inviteAndEnrol(e.admin, "d@example.com")
	station2 := e.aliasID("Station 2")

	// An invite tree: C's request goes when B and their tree are deactivated.
	cReq := e.request(c, catalog.RequestInput{Kind: catalog.KindChange, AliasID: &station2, Ignored: ptr(true), Note: e.private("C's note")})
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", b.id), server.DeactivateRequest{Tree: true}, nil); code != http.StatusOK {
		t.Fatalf("deactivate tree: %d %s", code, p.Error)
	}
	var closed []catalog.RequestView
	e.admin.call(http.MethodGet, "/api/v1/admin/alias-requests?status=closed", nil, &closed)
	if r := findRequest(closed, cReq); r == nil || r.Status != catalog.StatusWithdrawn {
		t.Errorf("C's request after the tree's deactivation: %+v", r)
	}
	// Reactivating C doesn't reopen it.
	if code, _ := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/reactivate", c.id), nil, nil); code != http.StatusNoContent && code != http.StatusOK {
		t.Errorf("reactivate C: %d", code)
	}
	e.admin.call(http.MethodGet, "/api/v1/admin/alias-requests?status=closed", nil, &closed)
	if r := findRequest(closed, cReq); r == nil || r.Status != catalog.StatusWithdrawn {
		t.Errorf("C's request after reactivation: %+v", r)
	}
	c, err := e.signIn(c.claims) // deactivation ended C's sessions
	if err != nil {
		t.Fatal(err)
	}

	// Deleting an account: D's approved request stays in history as
	// "deleted user"; D's waiting one is withdrawn and notes are cleared.
	note := e.private("Call me about this, D")
	added := e.request(d, catalog.RequestInput{Kind: catalog.KindAdd, Pattern: "0100020", Name: ptr("Station 20"), TemplateID: &tpl, Note: note})
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/approve", added), nil, nil); code != http.StatusNoContent {
		t.Fatalf("approve D's request: %d %s", code, p.Error)
	}
	station20 := e.aliasID("Station 20")
	waiting := e.request(d, catalog.RequestInput{Kind: catalog.KindDelete, AliasID: &station20, Note: e.private("Second note from D")})
	if code, p := d.call(http.MethodDelete, "/api/v1/me", nil, nil); code != http.StatusNoContent {
		t.Fatalf("D deletes their account: %d %s", code, p.Error)
	}
	e.admin.call(http.MethodGet, "/api/v1/admin/alias-requests?status=closed", nil, &closed)
	if r := findRequest(closed, waiting); r == nil || r.Status != catalog.StatusWithdrawn || r.Note != nil || r.RequestedBy != "deleted user" {
		t.Errorf("D's waiting request after deletion: %+v", r)
	}
	if r := findRequest(closed, added); r == nil || r.Status != catalog.StatusApproved || r.Note != nil {
		t.Errorf("D's approved request after deletion: %+v", r)
	}
	if hits := e.rowsContaining(note); len(hits) != 0 {
		t.Errorf("D's note is still stored: %d rows", len(hits))
	}
	h := e.adminAlias(station20).History
	t.Logf("history of Station 20 after D's deletion: requested by %q", s(h[0].RequestedBy))
	if len(h) != 1 || h[0].RequestedBy == nil || *h[0].RequestedBy != "deleted user" {
		t.Errorf("history: %+v", h)
	}

	// Alias deletion closes waiting requests as "alias deleted", and the
	// history stays with the capcode (owner, 2026-10-16).
	bob := e.newMember("bob@example.com", "user", e.admin)
	change := e.request(bob, catalog.RequestInput{Kind: catalog.KindChange, AliasID: &station2, Name: ptr("Station Two")})
	del := e.request(c, catalog.RequestInput{Kind: catalog.KindDelete, AliasID: &station2})
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/approve", del), nil, nil); code != http.StatusNoContent {
		t.Fatalf("approve deletion: %d %s", code, p.Error)
	}
	r := e.myRequest(bob, change)
	t.Logf("after Station 2 was deleted: Bob's request %q, alias_id %v, still names %q (%s)", r.Status, r.AliasID, s(r.AliasName), r.Pattern)
	if r.Status != catalog.StatusAliasDeleted || r.AliasID != nil || s(r.AliasName) != "Station 2" || r.Pattern != "0100002" {
		t.Errorf("Bob's request: %+v", r)
	}
	if r := e.myRequest(c, del); r.Status != catalog.StatusApproved {
		t.Errorf("C's deletion request: %+v", r)
	}
	var cv server.CapcodeView
	if code, p := e.admin.call(http.MethodGet, "/api/v1/capcodes/0100002", nil, &cv); code != http.StatusOK {
		t.Fatalf("capcode view: %d %s", code, p.Error)
	}
	if len(cv.History) != 1 || cv.History[0].Action != "deleted" || !cv.History[0].Earlier || cv.History[0].AliasName != "Station 2" {
		t.Errorf("capcode history: %+v", cv.History)
	}
	if code, body := bob.raw(http.MethodGet, "/api/v1/capcodes/0100002", nil); strings.Contains(body, `"history"`) {
		t.Errorf("a user sees capcode history: %d %s", code, body)
	}
	var created server.Created
	e.admin.call(http.MethodPost, "/api/v1/admin/aliases", catalog.AliasInput{Pattern: "0100002", Name: "Station 2 again", TemplateID: &tpl}, &created)
	h = e.adminAlias(created.ID).History
	if len(h) != 2 || h[0].Action != "added" || h[0].Earlier || h[1].Action != "deleted" || !h[1].Earlier {
		t.Errorf("history of the new alias for 0100002: %+v", h)
	}
}

// An integration that selected a deleted one-capcode alias keeps the
// capcode; a deleted range alias leaves the selection (owner, 2026-10-16).
func TestM6DeletedAliasInIntegrations(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	rangeID := e.addAlias("0200%", "Range")
	req := webhookTo("Station 3 and range", hook, "0100003")
	req.AliasIDs = []int64{rangeID}
	v := e.createIntegration(e.user, req)
	if len(v.Aliases) != 2 || len(v.Capcodes) != 0 {
		t.Fatalf("selection: %+v %+v", v.Aliases, v.Capcodes)
	}
	var d catalog.Deleted
	if code, p := e.admin.call(http.MethodDelete, fmt.Sprintf("/api/v1/admin/aliases/%d", e.aliasID("Station 3")), nil, &d); code != http.StatusOK || d.Integrations != 1 {
		t.Fatalf("delete Station 3: %d %s %+v", code, p.Error, d)
	}
	if code, _ := e.admin.call(http.MethodDelete, fmt.Sprintf("/api/v1/admin/aliases/%d", rangeID), nil, &d); code != http.StatusOK || d.Integrations != 1 {
		t.Fatalf("delete range: %d %+v", code, d)
	}
	after := e.integration(e.user, v.ID)
	t.Logf("integration selection after the deletions: capcodes %v, aliases %v", after.Capcodes, after.Aliases)
	if len(after.Aliases) != 0 || len(after.Capcodes) != 1 || after.Capcodes[0] != "0100003" {
		t.Errorf("selection after deletions: %+v %+v", after.Capcodes, after.Aliases)
	}
}

// What a request may be (R54, ADR 0007 §2), who may touch it, and that two
// admins can't both review one.
func TestM6RequestRules(t *testing.T) {
	e := setup(t)
	b := e.user
	tpl := e.templateID("Example Fire")
	station1 := e.aliasID("Station 1")
	refused := func(what string, in catalog.RequestInput, status int, code string) {
		t.Helper()
		st, p := b.call(http.MethodPost, "/api/v1/alias-requests", in, nil)
		t.Logf("%s: %d %q", what, st, p.Error)
		if st != status || p.Code != code {
			t.Errorf("%s: %d %+v, want %d %s", what, st, p, status, code)
		}
	}
	refused("new alias for a capcode that has one", catalog.RequestInput{Kind: catalog.KindAdd, Pattern: "100001", Name: ptr("Other"), TemplateID: &tpl}, http.StatusConflict, "duplicate_alias")
	refused("changing the capcode", catalog.RequestInput{Kind: catalog.KindChange, AliasID: &station1, Pattern: "0100005", Name: ptr("Station 5")}, http.StatusUnprocessableEntity, "pattern_fixed")
	refused("changing nothing", catalog.RequestInput{Kind: catalog.KindChange, AliasID: &station1, Name: ptr("Station 1"), SetTemplate: true, TemplateID: &tpl}, http.StatusUnprocessableEntity, "nothing_to_change")
	refused("a formula-like name", catalog.RequestInput{Kind: catalog.KindAdd, Pattern: "0100030", Name: ptr("=HYPERLINK(1)"), TemplateID: &tpl}, http.StatusUnprocessableEntity, "bad_name")
	refused("an unknown template", catalog.RequestInput{Kind: catalog.KindAdd, Pattern: "0100030", Name: ptr("Thirty"), TemplateID: ptr(int64(999999))}, http.StatusUnprocessableEntity, "no_template")
	refused("a deletion that also renames", catalog.RequestInput{Kind: catalog.KindDelete, AliasID: &station1, Name: ptr("x")}, http.StatusUnprocessableEntity, "bad_request")

	id := e.request(b, catalog.RequestInput{Kind: catalog.KindChange, AliasID: &station1, Name: ptr("Station 7")})
	refused("a second waiting request for the same alias", catalog.RequestInput{Kind: catalog.KindDelete, AliasID: &station1}, http.StatusConflict, "already_requested")

	// Editing: still checked; marks the request as seen against the alias now.
	if code, p := b.call(http.MethodPut, fmt.Sprintf("/api/v1/alias-requests/%d", id), catalog.RequestInput{Name: ptr("Station 8"), Note: "edited"}, nil); code != http.StatusNoContent {
		t.Fatalf("edit: %d %s", code, p.Error)
	}
	if r := e.myRequest(b, id); s(r.Proposed.Name) != "Station 8" || s(r.Note) != "edited" || r.Kind != catalog.KindChange {
		t.Errorf("after edit: %+v", r)
	}
	// Someone else can't edit, withdraw or see it.
	other := e.newMember("other@example.com", "user", e.admin)
	if code, _ := other.call(http.MethodPut, fmt.Sprintf("/api/v1/alias-requests/%d", id), catalog.RequestInput{Name: ptr("Mine")}, nil); code != http.StatusNotFound {
		t.Errorf("other user edits: %d", code)
	}
	if code, _ := other.call(http.MethodPost, fmt.Sprintf("/api/v1/alias-requests/%d/withdraw", id), nil, nil); code != http.StatusNotFound {
		t.Errorf("other user withdraws: %d", code)
	}
	if len(e.myRequests(other)) != 0 {
		t.Errorf("other user sees B's requests")
	}
	// A template a waiting request names can't be deleted.
	if err := e.cat.AddTemplate(e.ctx, catalog.Template{Name: "Spare", Agency: "Spare Agency"}); err != nil {
		t.Fatal(err)
	}
	spare := e.templateID("Spare")
	station2 := e.aliasID("Station 2")
	tplReq := e.request(b, catalog.RequestInput{Kind: catalog.KindChange, AliasID: &station2, SetTemplate: true, TemplateID: &spare})
	if err := e.cat.DeleteTemplate(e.ctx, "Spare"); err == nil || !strings.Contains(err.Error(), "waiting alias requests") {
		t.Errorf("deleting a template a request names: %v", err)
	}
	// Two admins approve at once: one succeeds, the other is told.
	admin2 := e.newMember("admin2@example.com", "admin", e.admin)
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, m := range []*member{e.admin, admin2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = m.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/approve", tplReq), nil, nil)
		}()
	}
	wg.Wait()
	t.Logf("two admins approve at once: %v", codes)
	if !(codes[0] == http.StatusNoContent && codes[1] == http.StatusConflict || codes[1] == http.StatusNoContent && codes[0] == http.StatusConflict) {
		t.Errorf("concurrent approvals: %v", codes)
	}
	var tplName string
	e.st.Pool.QueryRow(e.ctx, "SELECT t.name FROM aliases a JOIN templates t ON t.id = a.template_id WHERE a.id = $1", station2).Scan(&tplName)
	if tplName != "Spare" {
		t.Errorf("Station 2's template: %q", tplName)
	}
	if h := e.adminAlias(station2).History; len(h) != 1 {
		t.Errorf("one approval, %d history entries", len(h))
	}
	// Withdraw, then it's final.
	if code, _ := b.call(http.MethodPost, fmt.Sprintf("/api/v1/alias-requests/%d/withdraw", id), nil, nil); code != http.StatusNoContent {
		t.Errorf("withdraw: %d", code)
	}
	if code, _ := b.call(http.MethodPost, fmt.Sprintf("/api/v1/alias-requests/%d/withdraw", id), nil, nil); code != http.StatusConflict {
		t.Errorf("withdraw twice: %d", code)
	}
	// At most 20 waiting.
	for i := range catalog.MaxWaitingPerUser {
		e.request(b, catalog.RequestInput{Kind: catalog.KindAdd, Pattern: fmt.Sprintf("06%05d", i), Name: ptr(fmt.Sprintf("Pager %d", i))})
	}
	refused("a 21st waiting request", catalog.RequestInput{Kind: catalog.KindAdd, Pattern: "0699999", Name: ptr("One too many")}, http.StatusConflict, "too_many")
}

// The template list users choose from (R54).
func TestM6Templates(t *testing.T) {
	e := setup(t)
	var ts []server.TemplateChoice
	if code, p := e.user.call(http.MethodGet, "/api/v1/templates", nil, &ts); code != http.StatusOK {
		t.Fatalf("templates: %d %s", code, p.Error)
	}
	b, _ := json.Marshal(ts)
	if len(ts) != 1 || ts[0].Name != "Example Fire" || ts[0].Agency != "Example Fire Service" || strings.Contains(string(b), "example-fire") {
		t.Errorf("templates: %s", b)
	}
}
