package acceptance

import (
	"net/http"
	"strings"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// A135, A136 at the API level (R77e, R77f): who the frame shows the app
// switcher to, worked out by the server.
func TestA136SwitcherAPI(t *testing.T) {
	e := setup(t)
	other := e.newMember("other@example.com", "user", e.admin)
	show := func(m *member) bool {
		t.Helper()
		var me server.Me
		if code, p := m.call(http.MethodGet, "/api/v1/me", nil, &me); code != http.StatusOK {
			t.Fatalf("%s: me: %d %s", m.email, code, p.Error)
		}
		return me.ShowSwitcher
	}
	meta := func() server.Meta {
		t.Helper()
		var m server.Meta
		if code, p := e.user.call(http.MethodGet, "/api/v1/meta", nil, &m); code != http.StatusOK {
			t.Fatalf("meta: %d %s", code, p.Error)
		}
		return m
	}
	mark := func(m *member, sees bool) {
		t.Helper()
		if code, p := e.admin.call(http.MethodPut, "/api/v1/admin/users/"+itoa(m.id)+"/switcher", server.SeesSwitcherRequest{SeesSwitcher: sees}, nil); code != http.StatusNoContent {
			t.Fatalf("mark %s %v: %d %s", m.email, sees, code, p.Error)
		}
	}
	everyone := func(on bool) {
		t.Helper()
		var out server.SwitcherSettings
		if code, p := e.admin.call(http.MethodPut, "/api/v1/admin/switcher", server.SwitcherSettings{Everyone: on}, &out); code != http.StatusOK || out.Everyone != on {
			t.Fatalf("everyone %v: %d %s %+v", on, code, p.Error, out)
		}
	}

	// Standalone: no RadioArchive in meta, and nobody sees it, marked or for everyone.
	if m := meta(); m.RadioArchiveURL != "" || m.RadioArchiveName != "" {
		t.Errorf("standalone meta: %q %q, want neither", m.RadioArchiveURL, m.RadioArchiveName)
	}
	mark(e.user, true)
	everyone(true)
	if show(e.user) || show(other) {
		t.Error("standalone: a switcher shown")
	}
	mark(e.user, false)
	everyone(false)

	e.srv.RadioArchiveURL, e.srv.RadioArchiveName = "http://radio.example.test/", "Radio"
	if m := meta(); m.RadioArchiveURL != "http://radio.example.test/" || m.RadioArchiveName != "Radio" {
		t.Errorf("combined meta: %q %q", m.RadioArchiveURL, m.RadioArchiveName)
	}
	// A new install: nobody marked, everyone off.
	var st server.SwitcherSettings
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/switcher", nil, &st); code != http.StatusOK || st.Everyone {
		t.Errorf("everyone to start with: %d %+v, want off", code, st)
	}
	if show(e.user) || show(other) || show(e.admin) {
		t.Error("nobody marked: a switcher shown")
	}
	mark(e.user, true)
	mark(e.user, true) // already so: no change
	if !show(e.user) || show(other) {
		t.Errorf("user marked: user %v, other %v", show(e.user), show(other))
	}
	var users []server.UserRow
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/users", nil, &users); code != http.StatusOK {
		t.Fatalf("users: %d", code)
	}
	for _, u := range users {
		if want := u.ID == e.user.id; u.SeesSwitcher != want {
			t.Errorf("%s: sees_switcher %v, want %v", u.Email, u.SeesSwitcher, want)
		}
	}
	everyone(true)
	if !show(other) || !show(e.admin) {
		t.Error("everyone on: not shown to the unmarked")
	}
	everyone(false)
	mark(e.user, false)
	if show(e.user) {
		t.Error("unmarked again: still shown")
	}

	// Only admins change them; there's no such user to mark.
	if code, _ := e.user.call(http.MethodPut, "/api/v1/admin/switcher", server.SwitcherSettings{Everyone: true}, nil); code != http.StatusForbidden {
		t.Errorf("a user, everyone: %d, want 403", code)
	}
	if code, _ := e.user.call(http.MethodPut, "/api/v1/admin/users/"+itoa(other.id)+"/switcher", server.SeesSwitcherRequest{SeesSwitcher: true}, nil); code != http.StatusForbidden {
		t.Errorf("a user, a mark: %d, want 403", code)
	}
	if code, _ := e.admin.call(http.MethodPut, "/api/v1/admin/users/999999/switcher", server.SeesSwitcherRequest{SeesSwitcher: true}, nil); code != http.StatusNotFound {
		t.Errorf("no such user: %d, want 404", code)
	}
	if !strings.Contains(e.logs.String(), `"msg":"switcher mark"`) {
		t.Error("a mark isn't logged")
	}
}
