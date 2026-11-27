package acceptance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// fakeGroup stands in for Authentik's API for the switcher's group (R77f):
// its members by subject. stack-checks.sh runs the same against the real
// Authentik (A158).
type fakeGroup struct {
	mu      sync.Mutex
	members map[string]bool
	missing bool
	down    bool // Authentik doesn't answer
	adds    int
	removes int
}

func (f *fakeGroup) GroupMembers(_ context.Context, group string) (string, map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.down:
		return "", nil, errors.New("find group: connection refused")
	case f.missing:
		return "", nil, fmt.Errorf("find group: Authentik has no %s group yet: %w", group, accounts.ErrNoGroup)
	}
	return "g-switcher", maps.Clone(f.members), nil
}

func (f *fakeGroup) SetGroupMember(_ context.Context, groupID, subject string, member bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down || groupID != "g-switcher" {
		return errors.New("add to group: connection refused")
	}
	if member {
		f.adds++
		f.members[subject] = true
	} else {
		f.removes++
		delete(f.members, subject)
	}
	return nil
}

func (f *fakeGroup) has(subject string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members[subject]
}

// combined sets e's API beside a made-up RadioArchive, with the switcher's
// group standing in for Authentik's, read once as serve's start does.
func (e *env) combined(f *fakeGroup) *accounts.SwitcherGroup {
	e.t.Helper()
	e.srv.RadioArchiveURL, e.srv.RadioArchiveName = "http://radio.example.test", "Radio"
	g := &accounts.SwitcherGroup{Name: accounts.DefaultSwitcherGroup, Dir: f, Log: slog.New(slog.NewJSONHandler(e.logs, nil))}
	g.Read(e.ctx)
	e.acc.Switcher = g
	return g
}

// A140, A141 at the API level (R77e, R77f): who the frame shows the app
// switcher to, worked out by the server from the Authentik group, and the
// admin's mark adding and removing the membership.
func TestA141SwitcherAPI(t *testing.T) {
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
	markCode := func(m *member, sees bool) (int, server.Problem) {
		t.Helper()
		return e.admin.call(http.MethodPut, "/api/v1/admin/users/"+itoa(m.id)+"/switcher", server.SeesSwitcherRequest{SeesSwitcher: sees}, nil)
	}
	mark := func(m *member, sees bool) {
		t.Helper()
		if code, p := markCode(m, sees); code != http.StatusNoContent {
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
	settings := func() server.SwitcherSettings {
		t.Helper()
		var st server.SwitcherSettings
		if code, p := e.admin.call(http.MethodGet, "/api/v1/admin/switcher", nil, &st); code != http.StatusOK {
			t.Fatalf("switcher settings: %d %s", code, p.Error)
		}
		return st
	}

	// Standalone: no RadioArchive in meta, nobody sees it even for everyone,
	// no group, and a mark is refused.
	if m := meta(); m.RadioArchiveURL != "" || m.RadioArchiveName != "" {
		t.Errorf("standalone meta: %q %q, want neither", m.RadioArchiveURL, m.RadioArchiveName)
	}
	everyone(true)
	if show(e.user) || show(other) {
		t.Error("standalone: a switcher shown")
	}
	if st := settings(); st.Group != "" || st.GroupProblem != "" {
		t.Errorf("standalone settings: %+v, want no group", st)
	}
	if code, p := markCode(e.user, true); code != http.StatusConflict || p.Code != "no_radioarchive" {
		t.Errorf("standalone mark: %d %s, want 409", code, p.Code)
	}
	everyone(false)

	// Beside RadioArchive, the group holding someone who isn't a PagerArchive
	// user (the radio's own service account, say).
	f := &fakeGroup{members: map[string]bool{"radio-operator": true}}
	e.combined(f)
	if m := meta(); m.RadioArchiveURL != "http://radio.example.test" || m.RadioArchiveName != "Radio" {
		t.Errorf("combined meta: %q %q", m.RadioArchiveURL, m.RadioArchiveName)
	}
	// A new install: nobody marked, everyone off.
	if st := settings(); st.Everyone || st.Group != "radioarchive-preview" || st.GroupProblem != "" {
		t.Errorf("to start with: %+v, want everyone off, the group named, no problem", st)
	}
	if show(e.user) || show(other) || show(e.admin) {
		t.Error("nobody in the group: a switcher shown")
	}
	mark(e.user, true)
	mark(e.user, true) // already so: no change
	if !f.has(e.user.claims.Subject) || !f.has("radio-operator") {
		t.Errorf("marked: in the group %v, the radio's member kept %v", f.has(e.user.claims.Subject), f.has("radio-operator"))
	}
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
	if show(e.user) || f.has(e.user.claims.Subject) || !f.has("radio-operator") {
		t.Errorf("unmarked again: shown %v, in the group %v, the radio's member kept %v", show(e.user), f.has(e.user.claims.Subject), f.has("radio-operator"))
	}

	// Someone added in Authentik itself shows once the group is read again.
	f.mu.Lock()
	f.members[other.claims.Subject] = true
	f.mu.Unlock()
	e.acc.Switcher.Read(e.ctx)
	if !show(other) {
		t.Error("added in Authentik: not shown after the next read")
	}

	// Someone who has never signed in has no Authentik user to add.
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE users SET subject = NULL WHERE id = $1`, other.id); err != nil {
		t.Fatal(err)
	}
	if code, p := markCode(other, true); code != http.StatusConflict || p.Code != "never_signed_in" || !strings.Contains(p.Error, "radioarchive-preview") {
		t.Errorf("never signed in: %d %s %q, want 409", code, p.Code, p.Error)
	}
	// Authentik down: nothing changes, and the admin is told; the members
	// read before stay.
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	e.acc.Switcher.Read(e.ctx)
	if code, p := markCode(e.user, true); code != http.StatusServiceUnavailable || p.Code != "directory_unavailable" {
		t.Errorf("Authentik down: %d %s, want 503", code, p.Code)
	}
	if st := settings(); st.GroupProblem != "unreadable" {
		t.Errorf("Authentik down: problem %q, want unreadable", st.GroupProblem)
	}
	// No such group: said, and a mark is refused.
	f.mu.Lock()
	f.down, f.missing = false, true
	f.mu.Unlock()
	e.acc.Switcher.Read(e.ctx)
	if st := settings(); st.GroupProblem != "missing" {
		t.Errorf("no group: problem %q, want missing", st.GroupProblem)
	}
	if code, p := markCode(e.user, true); code != http.StatusConflict || p.Code != "no_group" {
		t.Errorf("no group: %d %s, want 409", code, p.Code)
	}
	f.mu.Lock()
	f.missing = false
	f.mu.Unlock()

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
