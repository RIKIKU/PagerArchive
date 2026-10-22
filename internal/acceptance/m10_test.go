package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/icon"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// M10 at API level (ADR 0011): templates for admins (R64) and their icons
// (R65, R66). The browser's checks are in web/tests/e2e.spec.ts and the
// module's in internal/stacktest.

func testIcon(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../testdata/icons", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// upload sends body as m to PUT path, with a Content-Type of contentType
// (the API ignores it), and decodes the reply.
func (m *member) upload(path string, body []byte, contentType string) (int, server.Problem, server.IconSet) {
	m.e.t.Helper()
	req, _ := http.NewRequest(http.MethodPut, m.e.url+path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if m.cookie != nil {
		req.AddCookie(m.cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		m.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var p server.Problem
	var set server.IconSet
	if resp.StatusCode >= 400 {
		_ = json.NewDecoder(resp.Body).Decode(&p)
	} else {
		_ = json.NewDecoder(resp.Body).Decode(&set)
	}
	return resp.StatusCode, p, set
}

func (e *env) adminTemplates() map[string]server.AdminTemplate {
	e.t.Helper()
	var ts []server.AdminTemplate
	if code, p := e.admin.call(http.MethodGet, "/api/v1/admin/templates", nil, &ts); code != http.StatusOK {
		e.t.Fatalf("templates: %d %s", code, p.Error)
	}
	out := map[string]server.AdminTemplate{}
	for _, t := range ts {
		out[t.Name] = t
	}
	return out
}

// storedIcon is a template's icon as stored, or nil.
func (e *env) storedIcon(template string) []byte {
	e.t.Helper()
	var b []byte
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT icon_png FROM templates WHERE name = $1", template).Scan(&b); err != nil {
		e.t.Fatal(err)
	}
	return b
}

// newestMessage is the newest message from capcode as m sees it.
func (e *env) newestMessage(m *member, capcode string) server.Message {
	e.t.Helper()
	var page server.MessagePage
	if code, p := m.call(http.MethodGet, "/api/v1/messages?capcode="+capcode, nil, &page); code != http.StatusOK || len(page.Messages) == 0 {
		e.t.Fatalf("messages for %s: %d %s %d", capcode, code, p.Error, len(page.Messages))
	}
	return page.Messages[0]
}

// A56. Admins add, edit and delete templates (R64); users can't.
func TestA56AdminManagesTemplates(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100004", "@@EX0000044 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN4", e.now)

	var created server.Created
	in := catalog.TemplateInput{Name: "Example Rescue", Agency: "Example Rescue Service"}
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/templates", in, &created); code != http.StatusCreated {
		t.Fatalf("add template: %d %s", code, p.Error)
	}
	ts := e.adminTemplates()
	if r := ts["Example Rescue"]; r.ID != created.ID || r.Agency != "Example Rescue Service" || r.Aliases != 0 || r.AgencyProfile != nil || r.Icon != nil {
		t.Errorf("listed as %+v", r)
	}
	if ts["Example Fire"].Aliases != 4 {
		t.Errorf("Example Fire: %+v", ts["Example Fire"])
	}

	// Moving "Station 4" shows the new agency at once.
	var station4 int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT id FROM aliases WHERE pattern = '0100004'").Scan(&station4); err != nil {
		t.Fatal(err)
	}
	e.adminEdit(station4, catalog.AliasInput{Pattern: "0100004", Name: "Station 4", TemplateID: &created.ID})
	if _, err := e.worker.RunOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if m := e.newestMessage(e.user, "0100004"); deref(m.Agency) != "Example Rescue Service" {
		t.Errorf("0100004's page shows agency %q", deref(m.Agency))
	}
	if n := e.adminTemplates()["Example Rescue"].Aliases; n != 1 {
		t.Errorf("Example Rescue has %d aliases", n)
	}

	// Deleting a template in use is refused, saying how many use it.
	fire := e.templateID("Example Fire")
	code, p := e.admin.call(http.MethodDelete, fmt.Sprintf("/api/v1/admin/templates/%d", fire), nil, nil)
	if code != http.StatusConflict || p.Code != "template_in_use" || !strings.Contains(p.Error, "3 aliases use it") {
		t.Errorf("delete Example Fire: %d %s %q", code, p.Code, p.Error)
	}

	// Renaming, and a name that's taken.
	rename := func(id int64, name string) (int, server.Problem) {
		return e.admin.call(http.MethodPatch, fmt.Sprintf("/api/v1/admin/templates/%d", id), catalog.TemplateChange{Name: &name}, nil)
	}
	if code, p := rename(created.ID, "Example Rescue North"); code != http.StatusNoContent {
		t.Errorf("rename: %d %s", code, p.Error)
	}
	if code, p := rename(created.ID, "Example Fire"); code != http.StatusConflict || p.Code != "template_exists" {
		t.Errorf("rename to a taken name: %d %s", code, p.Code)
	}
	if _, ok := e.adminTemplates()["Example Rescue North"]; !ok {
		t.Error("not renamed")
	}

	// Choosing a loaded agency profile re-parses the template's pages (R23a).
	var profiles []string
	if code, p := e.admin.call(http.MethodGet, "/api/v1/admin/agency-profiles", nil, &profiles); code != http.StatusOK || strings.Join(profiles, ",") != "example-fire" {
		t.Errorf("agency profiles: %d %s %v", code, p.Error, profiles)
	}
	if m := e.newestMessage(e.user, "0100004"); m.JobNumber == nil || m.EventType != nil {
		t.Errorf("before: %+v %+v", m.JobNumber, m.EventType)
	}
	profile := "example-fire"
	if code, p := e.admin.call(http.MethodPatch, fmt.Sprintf("/api/v1/admin/templates/%d", created.ID), catalog.TemplateChange{AgencyProfile: &profile}, nil); code != http.StatusNoContent {
		t.Fatalf("set profile: %d %s", code, p.Error)
	}
	if _, err := e.worker.RunOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if m := e.newestMessage(e.user, "0100004"); deref(m.EventType) != "HSF1" {
		t.Errorf("after: event type %q", deref(m.EventType))
	}
	unknown := "no-such-profile"
	if code, p := e.admin.call(http.MethodPatch, fmt.Sprintf("/api/v1/admin/templates/%d", created.ID), catalog.TemplateChange{AgencyProfile: &unknown}, nil); code != http.StatusUnprocessableEntity || p.Code != "unknown_profile" {
		t.Errorf("unknown profile: %d %s", code, p.Code)
	}

	// An unused template is deleted.
	var spare server.Created
	if code, _ := e.admin.call(http.MethodPost, "/api/v1/admin/templates", catalog.TemplateInput{Name: "Spare", Agency: "Spare Agency"}, &spare); code != http.StatusCreated {
		t.Fatal("add Spare")
	}
	if code, p := e.admin.call(http.MethodDelete, fmt.Sprintf("/api/v1/admin/templates/%d", spare.ID), nil, nil); code != http.StatusNoContent {
		t.Errorf("delete Spare: %d %s", code, p.Error)
	}

	// User B can't change templates through the API (A56), or see the
	// admins' list, and nothing changes.
	agency := "Changed by B"
	path := fmt.Sprintf("/api/v1/admin/templates/%d", fire)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPatch, path, catalog.TemplateChange{Agency: &agency}},
		{http.MethodPost, "/api/v1/admin/templates", catalog.TemplateInput{Name: "B's", Agency: "B's"}},
		{http.MethodDelete, fmt.Sprintf("/api/v1/admin/templates/%d", created.ID), nil},
		{http.MethodDelete, path + "/icon", nil},
		{http.MethodGet, "/api/v1/admin/templates", nil},
		{http.MethodGet, "/api/v1/admin/agency-profiles", nil},
	} {
		if code, _ := e.user.call(c.method, c.path, c.body, nil); code != http.StatusForbidden {
			t.Errorf("user B: %s %s: %d, want 403", c.method, c.path, code)
		}
	}
	if code, _, _ := e.user.upload(path+"/icon", testIcon(t, "example-icon.png"), "image/png"); code != http.StatusForbidden {
		t.Errorf("user B uploads an icon: %d, want 403", code)
	}
	after := e.adminTemplates()
	if after["Example Fire"].Agency != "Example Fire Service" || after["Example Fire"].Icon != nil || len(after) != 2 {
		t.Errorf("templates changed: %+v", after)
	}
}

// A57 at API level: an icon from the browser's upload is stored, shown on
// every message view, served with long-lived caching, and refused with a
// reason for an SVG, a 200 KB PNG or a text file renamed .png.
func TestA57IconsShown(t *testing.T) {
	e := setup(t)
	pr := e.mustPost("a", "0100001", "@@EX0000057 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1", e.now)
	fire := fmt.Sprintf("/api/v1/admin/templates/%d", e.templateID("Example Fire"))

	// The type comes from the bytes: a PNG sent as text/plain is accepted.
	code, p, set := e.admin.upload(fire+"/icon", testIcon(t, "example-icon.png"), "text/plain")
	if code != http.StatusOK || set.Icon == nil || !strings.HasPrefix(*set.Icon, "/api/v1/icons/") {
		t.Fatalf("upload: %d %s %v", code, p.Error, set.Icon)
	}
	url := *set.Icon

	// On every view of the sender, for users too, with the agency name for
	// its text alternative (the browser draws it).
	m := e.newestMessage(e.user, "0100001")
	if deref(m.Icon) != url || deref(m.Agency) != "Example Fire Service" {
		t.Errorf("message list: icon %q agency %q", deref(m.Icon), deref(m.Agency))
	}
	var detail server.Message
	e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", pr.MessageID), nil, &detail)
	var job struct{ Messages []server.Message }
	e.user.call(http.MethodGet, "/api/v1/jobs/EX0000057", nil, &job)
	var alias server.AliasView
	e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/aliases/%d", *m.AliasID), nil, &alias)
	var choices []server.TemplateChoice
	e.user.call(http.MethodGet, "/api/v1/templates", nil, &choices)
	if deref(detail.Icon) != url || len(job.Messages) != 1 || deref(job.Messages[0].Icon) != url || deref(alias.Icon) != url || len(choices) != 1 || deref(choices[0].Icon) != url {
		t.Errorf("views: detail %q, job %v, alias %q, templates %v", deref(detail.Icon), job.Messages, deref(alias.Icon), choices)
	}
	if deref(e.adminTemplates()["Example Fire"].Icon) != url {
		t.Error("not in the admins' template list")
	}
	// Live updates too.
	live := e.sse("", e.user.cookie, "")
	until(t, live, "ready", 2*time.Second)
	e.mustPost("a", "0100002", "@@EX0000058 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2", e.now.Add(time.Minute))
	ev, _ := until(t, live, "message", 2*time.Second)
	var lm server.Message
	if err := json.Unmarshal([]byte(ev.Data), &lm); err != nil || deref(lm.Icon) != url {
		t.Errorf("live update: %v %q", err, deref(lm.Icon))
	}

	// Served as a PNG, never sniffed, cached for a year, to signed-in users.
	resp, err := e.user.do(http.MethodGet, e.url+url, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	h := resp.Header
	if resp.StatusCode != http.StatusOK || h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Cache-Control") != "private, max-age=31536000, immutable" || !bytes.Equal(body, e.storedIcon("Example Fire")) {
		t.Errorf("served: %d %v", resp.StatusCode, h)
	}
	if c, err := png.DecodeConfig(bytes.NewReader(body)); err != nil || c.Width != 64 || c.Height != 64 {
		t.Errorf("served icon: %+v %v", c, err)
	}
	for _, c := range []struct {
		who  *member
		path string
		want int
	}{
		{nil, url, http.StatusUnauthorized},
		{e.user, "/api/v1/icons/" + strings.Repeat("0", 64) + ".png", http.StatusNotFound},
		{e.user, "/api/v1/icons/icon.png", http.StatusNotFound},
		{e.user, strings.TrimSuffix(url, ".png"), http.StatusNotFound},
	} {
		resp, err := c.who.do(http.MethodGet, e.url+c.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("GET %s: %d, want %d", c.path, resp.StatusCode, c.want)
		}
	}

	// Refused with the reason, and the icon unchanged.
	for _, c := range []struct {
		file, contentType string
		status            int
		code              string
	}{
		{"example.svg", "image/svg+xml", http.StatusUnprocessableEntity, "icon_svg"},
		{"large-200kb.png", "image/png", http.StatusRequestEntityTooLarge, "icon_too_large"},
		{"not-an-image.png", "image/png", http.StatusUnprocessableEntity, "icon_type"},
		{"huge-canvas.png", "image/png", http.StatusUnprocessableEntity, "icon_dimensions"},
	} {
		code, p, _ := e.admin.upload(fire+"/icon", testIcon(t, c.file), c.contentType)
		if code != c.status || p.Code != c.code || p.Error == "" {
			t.Errorf("%s: %d %s %q, want %d %s", c.file, code, p.Code, p.Error, c.status, c.code)
		}
		if got := deref(e.newestMessage(e.user, "0100001").Icon); got != url {
			t.Errorf("%s changed the icon to %q", c.file, got)
		}
	}
	if !strings.Contains(e.logs.String(), `"code":"icon_svg"`) {
		t.Error("refusals aren't logged")
	}

	// Every accepted type; replacing gives a new URL, and the old one goes.
	for _, f := range []string{"several-sizes.ico", "animated.gif", "with-exif.jpg", "wide.webp", "example-icon.png"} {
		code, p, set := e.admin.upload(fire+"/icon", testIcon(t, f), "")
		if code != http.StatusOK || set.Icon == nil {
			t.Errorf("%s: %d %s", f, code, p.Error)
		}
	}
	code, _, set = e.admin.upload(fire+"/icon", testIcon(t, "wide.webp"), "")
	if code != http.StatusOK || deref(set.Icon) == url {
		t.Fatalf("replace: %d %v", code, set.Icon)
	}
	if resp, err := e.user.do(http.MethodGet, e.url+url, nil); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("the replaced icon's URL: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}

	// Removing it: the row shows no icon, and the colour stays.
	if _, err := e.st.Pool.Exec(e.ctx, "UPDATE templates SET colour = '#b3261e' WHERE name = 'Example Fire'"); err != nil {
		t.Fatal(err)
	}
	if code, p := e.admin.call(http.MethodDelete, fire+"/icon", nil, nil); code != http.StatusNoContent {
		t.Errorf("remove: %d %s", code, p.Error)
	}
	if m := e.newestMessage(e.user, "0100001"); m.Icon != nil || deref(m.Colour) != "#b3261e" || e.storedIcon("Example Fire") != nil {
		t.Errorf("after removal: icon %v colour %q", m.Icon, deref(m.Colour))
	}
	if code, p, _ := e.admin.upload("/api/v1/admin/templates/999999/icon", testIcon(t, "example-icon.png"), ""); code != http.StatusNotFound || p.Code != "no_template" {
		t.Errorf("no such template: %d %s", code, p.Code)
	}
}

// counting reads from a stream of n bytes and counts what was read.
type counting struct {
	n, read int64
}

func (c *counting) Read(p []byte) (int, error) {
	if c.read >= c.n {
		return 0, io.EOF
	}
	k := min(int64(len(p)), c.n-c.read)
	for i := range k {
		p[i] = 'x'
	}
	c.read += k
	return int(k), nil
}

// R65: the size limit applies before the body is read, not after. A 10 MB
// upload is refused having read at most one byte more than 100 KB, or none
// when its declared length is too large.
func TestIconSizeLimitBeforeReading(t *testing.T) {
	e := setup(t)
	path := fmt.Sprintf("/api/v1/admin/templates/%d/icon", e.templateID("Example Fire"))
	for _, declared := range []bool{false, true} {
		body := &counting{n: 10 << 20}
		req := httptest.NewRequest(http.MethodPut, path, body)
		req.ContentLength = -1
		if declared {
			req.ContentLength = body.n
		}
		req.AddCookie(e.admin.cookie)
		rec := httptest.NewRecorder()
		e.srv.Handler().ServeHTTP(rec, req)
		var p server.Problem
		_ = json.NewDecoder(rec.Body).Decode(&p)
		if rec.Code != http.StatusRequestEntityTooLarge || p.Code != "icon_too_large" {
			t.Errorf("declared %v: %d %s", declared, rec.Code, p.Code)
		}
		if limit := int64(icon.MaxBytes + 1); body.read > limit || declared && body.read != 0 {
			t.Errorf("declared %v: read %d bytes of the body", declared, body.read)
		}
	}
	// Exactly 100 KB is read in full (and refused only as not an image).
	body := &counting{n: icon.MaxBytes}
	req := httptest.NewRequest(http.MethodPut, path, body)
	req.AddCookie(e.admin.cookie)
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "icon_type") || body.read != icon.MaxBytes {
		t.Errorf("100 KB: %d %s, read %d", rec.Code, rec.Body.String(), body.read)
	}
}

// A58 at API level: the stored icon is a fresh 64×64 PNG with nothing else
// from the file, and no endpoint takes a URL: the server never fetches one.
func TestA58StoredNotLinked(t *testing.T) {
	e := setup(t)
	fire := e.templateID("Example Fire")
	path := fmt.Sprintf("/api/v1/admin/templates/%d", fire)

	in := testIcon(t, "not-kept.png")
	if !bytes.Contains(in, []byte("NOT-KEPT")) {
		t.Fatal("the test icon has no text chunk")
	}
	if code, p, _ := e.admin.upload(path+"/icon", in, "image/png"); code != http.StatusOK {
		t.Fatalf("upload: %d %s", code, p.Error)
	}
	stored := e.storedIcon("Example Fire")
	if c, err := png.DecodeConfig(bytes.NewReader(stored)); err != nil || c.Width != 64 || c.Height != 64 {
		t.Errorf("stored: %+v %v", c, err)
	}
	if bytes.Contains(stored, []byte("NOT-KEPT")) || bytes.Contains(stored, []byte("tEXt")) || bytes.Equal(stored, in) {
		t.Error("the stored icon kept the uploaded file's text")
	}
	if hits := e.rowsContaining("NOT-KEPT"); len(hits) > 0 {
		t.Errorf("NOT-KEPT in %v", hits)
	}

	// A web server that counts requests, serving a real icon.
	var fetched atomic.Int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testIcon(t, "wide.webp"))
	}))
	defer site.Close()
	link := site.URL + "/icon.png"

	// A URL as the upload is refused with the reason, not fetched.
	code, p, _ := e.admin.upload(path+"/icon", []byte(link), "text/uri-list")
	if code != http.StatusUnprocessableEntity || p.Code != "icon_url" || !strings.Contains(p.Error, "never fetches") {
		t.Errorf("a URL as the icon: %d %s %q", code, p.Code, p.Error)
	}
	// Template endpoints take no icon field, by any name.
	for _, body := range []string{
		`{"name":"Linked","agency":"Linked","icon":"` + link + `"}`,
		`{"name":"Linked","agency":"Linked","icon_url":"` + link + `"}`,
	} {
		if code, _ := e.admin.call(http.MethodPost, "/api/v1/admin/templates", json.RawMessage(body), nil); code != http.StatusBadRequest {
			t.Errorf("add with %s: %d, want 400", body, code)
		}
	}
	for _, body := range []string{`{"icon":"` + link + `"}`, `{"icon_url":"` + link + `"}`, `{"icon_uri":"` + link + `"}`} {
		if code, _ := e.admin.call(http.MethodPatch, path, json.RawMessage(body), nil); code != http.StatusBadRequest {
			t.Errorf("change with %s: %d, want 400", body, code)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := fetched.Load(); n != 0 {
		t.Errorf("the server fetched the URL %d times", n)
	}
	if !bytes.Equal(e.storedIcon("Example Fire"), stored) {
		t.Error("the icon changed")
	}
}
