package acceptance

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/web"
)

// M15 at API level (ADR 0028): the look, saved on the account (R82d, A74),
// and the app's page sent in it. The browser's side is in
// web/tests/e2e.spec.ts.

const builtPage = `<!doctype html><html lang="en-AU"><head></head><body></body></html>`

// page is the app's page as m's browser gets it from the server (nil: no
// session), with the server's own look for the request (Server.PageLook).
func (e *env) page(m *member, path string) string {
	e.t.Helper()
	h := web.Serve(fstest.MapFS{"index.html": {Data: []byte(builtPage)}}, e.srv.PageLook)
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if m != nil {
		r.AddCookie(m.cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	b, _ := io.ReadAll(w.Result().Body)
	return string(b)
}

func (e *env) setLook(m *member, look string) (int, server.Me) {
	e.t.Helper()
	var me server.Me
	code, _ := m.call(http.MethodPatch, "/api/v1/me", server.MeUpdate{Look: look}, &me)
	return code, me
}

func TestA74LookSavedOnTheAccount(t *testing.T) {
	e := setup(t)
	b := e.user
	c := e.inviteAndEnrol(e.admin, "c@example.com")
	const consoleRoot = `<html data-look="console" lang="en-AU">`

	// Everyone starts on Harbour.
	for _, m := range []*member{b, c, e.admin} {
		if me := e.me(m); me.Look != "harbour" {
			t.Fatalf("%s starts on %q", m.email, me.Look)
		}
	}
	if p := e.page(b, "/"); !strings.Contains(p, `<html data-look="harbour" lang="en-AU">`) {
		t.Errorf("B's page on Harbour: %s", p)
	}

	// B chooses Console: the answer is B's account, with the look.
	code, me := e.setLook(b, "console")
	if code != http.StatusOK || me.Look != "console" || me.Email != b.email {
		t.Fatalf("B chooses Console: %d %+v", code, me)
	}
	if me := e.me(b); me.Look != "console" {
		t.Errorf("GET /api/v1/me after choosing: %q", me.Look)
	}

	// B signs in on another device (a phone): a new session, and the page
	// it's sent is already Console, on every route.
	phone, err := e.signIn(b.claims)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/messages/1", "/map", "/admin", "/account"} {
		if p := e.page(phone, path); !strings.Contains(p, consoleRoot) {
			t.Errorf("B's phone, %s: %s", path, p)
		}
	}
	// C, and the admin, still have Harbour.
	for _, m := range []*member{c, e.admin} {
		if me := e.me(m); me.Look != "harbour" {
			t.Errorf("%s: %q after B chose Console", m.email, me.Look)
		}
		if p := e.page(m, "/"); strings.Contains(p, "console") {
			t.Errorf("%s's page: %s", m.email, p)
		}
	}
	// Before sign-in, and on the sign-in page, the page is Harbour: as
	// built, even with B's session.
	if p := e.page(nil, "/"); p != builtPage {
		t.Errorf("no session: %s", p)
	}
	if p := e.page(b, "/signin"); p != builtPage {
		t.Errorf("B on the sign-in page: %s", p)
	}
	stale := &member{e: e, cookie: &http.Cookie{Name: "pa_session", Value: "not-a-session"}}
	if p := e.page(stale, "/"); p != builtPage {
		t.Errorf("a session that isn't one: %s", p)
	}

	// Only the looks there are, and nothing else in the body.
	for _, bad := range []string{"", "Console", "ledger", `console" onload="x`} {
		if code, _ := e.setLook(b, bad); code != http.StatusBadRequest {
			t.Errorf("look %q: %d", bad, code)
		}
	}
	if code, _ := b.call(http.MethodPatch, "/api/v1/me", map[string]string{"look": "harbour", "role": "admin"}, nil); code != http.StatusBadRequest {
		t.Errorf("a role in the body: %d", code)
	}
	if me := e.me(b); me.Look != "console" || me.Role != accounts.RoleUser {
		t.Errorf("after the refusals: %+v", me)
	}

	// Back to Harbour.
	if code, me := e.setLook(b, "harbour"); code != http.StatusOK || me.Look != "harbour" {
		t.Errorf("back to Harbour: %d %+v", code, me)
	}
	if p := e.page(phone, "/"); strings.Contains(p, "console") {
		t.Errorf("B's phone after going back: %s", p)
	}

	// After C deletes their account, their old session gets Harbour, and
	// the deleted account keeps no look.
	if code, _ := e.setLook(c, "console"); code != http.StatusOK {
		t.Fatalf("C chooses Console: %d", code)
	}
	if code, p := c.call(http.MethodDelete, "/api/v1/me", nil, nil); code != http.StatusNoContent {
		t.Fatalf("C deletes their account: %d %s", code, p.Error)
	}
	if p := e.page(c, "/"); p != builtPage {
		t.Errorf("C's page after deleting: %s", p)
	}
	u, err := e.st.UserByID(e.ctx, c.id)
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != "deleted" || u.Look != "harbour" {
		t.Errorf("C's account after deleting: %s, look %q", u.Status, u.Look)
	}
	t.Log("A74 (API): B's look saved on the account and sent with the page on a new session; C and the admin kept Harbour; signed out and /signin get the page as built; only harbour or console accepted")
}
