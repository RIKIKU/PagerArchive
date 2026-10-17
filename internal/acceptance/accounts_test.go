package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// fakeDir stands in for Authentik in API-level tests. It models what the
// app relies on: single-use invitations that expire and only enrol their
// own address, and users that can be disabled or deleted. The same checks
// run against a real Authentik in internal/stacktest.
type fakeDir struct {
	mu      sync.Mutex
	now     func() time.Time
	next    int
	invites map[string]accounts.DirectoryInvitation
	users   map[string]*fakeUser // by subject
	sent    map[string]string    // invitation ID -> address it was emailed to
}

type fakeUser struct {
	username, email, invitation string
	active                      bool
}

func newFakeDir() *fakeDir {
	return &fakeDir{now: time.Now, invites: map[string]accounts.DirectoryInvitation{}, users: map[string]*fakeUser{}, sent: map[string]string{}}
}

func (d *fakeDir) CreateInvitation(_ context.Context, inv accounts.DirectoryInvitation) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := fmt.Sprintf("inv-%d", d.next)
	d.invites[id] = inv
	return id, nil
}

func (d *fakeDir) SendInvitation(_ context.Context, id, address string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent[id] = address
	return nil
}

func (d *fakeDir) DeleteInvitation(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.invites, id)
	return nil
}

func (d *fakeDir) EnrolmentURL(id string) string {
	return "http://authentik.test/if/flow/enrol/?itoken=" + id
}

func (d *fakeDir) SetActive(_ context.Context, subject string, active bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if u := d.users[subject]; u != nil {
		u.active = active
	}
	return nil
}

func (d *fakeDir) DeleteUser(_ context.Context, subject string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.users, subject)
	return nil
}

func (d *fakeDir) UserByUsername(_ context.Context, username string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for sub, u := range d.users {
		if u.username == username {
			return sub, true, nil
		}
	}
	return "", false, nil
}

// enrol is the enrolment flow: the invitation must exist and not have
// expired; it's used up; a user is created for its address. (The emailed
// code is checked by the real stack test.)
func (d *fakeDir) enrol(id, name string) (accounts.Claims, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	inv, ok := d.invites[id]
	if !ok || !inv.Expires.After(d.now()) {
		return accounts.Claims{}, fmt.Errorf("invalid invite/invite not found")
	}
	delete(d.invites, id)
	for _, u := range d.users {
		if u.username == inv.Key {
			return accounts.Claims{}, fmt.Errorf("username taken")
		}
	}
	d.next++
	sub := strconv.Itoa(1000 + d.next)
	app := strconv.FormatInt(inv.AppID, 10)
	d.users[sub] = &fakeUser{username: inv.Key, email: inv.Address, invitation: app, active: true}
	return accounts.Claims{Subject: sub, Email: inv.Address, Name: name, Invitation: app}, nil
}

// signInAllowed is Authentik's password check: only active users.
func (d *fakeDir) signInAllowed(sub string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	u := d.users[sub]
	return u != nil && u.active
}

func (d *fakeDir) invitationFor(address string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, a := range d.sent {
		if _, live := d.invites[id]; live && a == address {
			return id, true
		}
	}
	return "", false
}

// member is a signed-in user with a browser session.
type member struct {
	e      *env
	id     int64
	email  string
	claims accounts.Claims
	cookie *http.Cookie
}

func (m *member) do(method, url string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if m != nil && m.cookie != nil {
		req.AddCookie(m.cookie)
	}
	return http.DefaultClient.Do(req)
}

// call makes an API request as m and decodes a JSON reply into out.
func (m *member) call(method, path string, body, out any) (int, server.Problem) {
	m.e.t.Helper()
	resp, err := m.do(method, m.e.url+path, body)
	if err != nil {
		m.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var p server.Problem
	if resp.StatusCode >= 400 {
		_ = json.NewDecoder(resp.Body).Decode(&p)
	} else if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			m.e.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode, p
}

// signIn is what /auth/callback does after Authentik: the account from
// the claims, and a new session.
func (e *env) signIn(c accounts.Claims) (*member, error) {
	u, err := e.acc.SignIn(e.ctx, c)
	if err != nil {
		return nil, err
	}
	token, err := e.acc.NewSession(e.ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &member{e: e, id: u.ID, email: c.Email, claims: c, cookie: &http.Cookie{Name: "pa_session", Value: token}}, nil
}

// newMember adds a signed-in member directly (fixture), invited by inviter.
func (e *env) newMember(email, role string, inviter *member) *member {
	e.t.Helper()
	k, _ := accounts.ParseEmail(email)
	sub := "fixture-" + email
	p := db.CreateUserParams{Subject: &sub, Email: &email, EmailKey: &k.Key, Role: role}
	if inviter != nil {
		p.InvitedBy = &inviter.id
	}
	u, err := e.st.CreateUser(e.ctx, p)
	if err != nil {
		e.t.Fatal(err)
	}
	e.dir.mu.Lock()
	e.dir.users[sub] = &fakeUser{username: k.Key, email: email, active: true}
	e.dir.mu.Unlock()
	token, err := e.acc.NewSession(e.ctx, u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return &member{e: e, id: u.ID, email: email, claims: accounts.Claims{Subject: sub, Email: email}, cookie: &http.Cookie{Name: "pa_session", Value: token}}
}

// invite has m invite address through the API and enrols the invitee.
func (e *env) inviteAndEnrol(m *member, address string) *member {
	e.t.Helper()
	if code, p := m.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: address}, nil); code != http.StatusCreated {
		e.t.Fatalf("%s invites %s: %d %s", m.email, address, code, p.Error)
	}
	id, ok := e.dir.invitationFor(address)
	if !ok {
		e.t.Fatalf("no invitation emailed to %s", address)
	}
	c, err := e.dir.enrol(id, "")
	if err != nil {
		e.t.Fatalf("%s enrols: %v", address, err)
	}
	n, err := e.signIn(c)
	if err != nil {
		e.t.Fatalf("%s signs in: %v", address, err)
	}
	return n
}

// canSignIn is a full sign-in: Authentik's password check, then the app's.
func (e *env) canSignIn(m *member) bool {
	if !e.dir.signInAllowed(m.claims.Subject) {
		return false
	}
	_, err := e.signIn(m.claims)
	return err == nil
}

func (e *env) me(m *member) server.Me {
	e.t.Helper()
	var me server.Me
	if code, p := m.call(http.MethodGet, "/api/v1/me", nil, &me); code != http.StatusOK {
		e.t.Fatalf("GET /api/v1/me as %s: %d %s", m.email, code, p.Error)
	}
	return me
}

// A24. Invitations.
func TestA24Invitations(t *testing.T) {
	e := setup(t)
	a := e.inviteAndEnrol(e.admin, "a@example.com")
	b := e.inviteAndEnrol(a, "b@example.com")
	me := e.me(b)
	if me.Role != "user" || me.InvitedBy == nil || *me.InvitedBy != "a@example.com" {
		t.Fatalf("B: role %s, invited by %v", me.Role, me.InvitedBy)
	}
	// The same invitation again: Authentik refuses it (single use), and so
	// does the app if the claims are replayed with another user.
	reused := b.claims
	reused.Subject = "someone-else"
	if _, err := e.signIn(reused); err == nil || !strings.Contains(err.Error(), "already been used") {
		t.Errorf("reused invitation: %v", err)
	}
	code, p := a.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "c@example.org"}, nil)
	if code != http.StatusUnprocessableEntity || p.Code != "domain_not_allowed" {
		t.Errorf("c@example.org: %d %+v", code, p)
	}
	// An invitation 7 days and 1 minute old no longer works.
	if code, _ := a.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "late@example.com"}, nil); code != http.StatusCreated {
		t.Fatalf("invite late: %d", code)
	}
	id, _ := e.dir.invitationFor("late@example.com")
	e.dir.now = func() time.Time { return time.Now().Add(7*24*time.Hour + time.Minute) }
	if _, err := e.dir.enrol(id, ""); err == nil {
		t.Error("expired invitation enrolled")
	}
	t.Logf("A24: B is %s invited by %s; reuse, other domain and expired invitation refused (%s)", me.Role, *me.InvitedBy, p.Error)
}

// A25. Deactivating an invite tree.
func TestA25DeactivatingAnInviteTree(t *testing.T) {
	e := setup(t)
	a := e.inviteAndEnrol(e.admin, "a@example.com")
	b := e.inviteAndEnrol(a, "b@example.com")
	c := e.inviteAndEnrol(b, "c@example.com")
	if code, _ := a.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "e@example.com"}, nil); code != http.StatusCreated {
		t.Fatal("A invites e")
	}
	eInvite, _ := e.dir.invitationFor("e@example.com")
	e.as = b
	bStream := e.streamClosed("")
	e.as = c
	cStream := e.streamClosed("")

	// Through the preview (R42b), confirmed with everyone ticked.
	pv := e.preview(e.admin, a.id)
	start := time.Now()
	if code, res, refused := e.confirm(e.admin, a.id, pv.Version, startTicked(pv.Root), ""); code != http.StatusOK || res.Deactivated != 3 {
		t.Fatalf("deactivate tree: %d %+v %s", code, res, refused.Error)
	}
	for name, ch := range map[string]<-chan struct{}{"B": bStream, "C": cStream} {
		select {
		case <-ch:
		case <-time.After(5*time.Second - time.Since(start)):
			t.Fatalf("%s's live connection still open after 5 s", name)
		}
	}
	closed := time.Since(start)
	for _, m := range []*member{a, b, c} {
		for _, path := range []string{"/api/v1/messages", "/api/v1/stream", "/api/v1/me"} {
			if code, _ := m.call(http.MethodGet, path, nil, nil); code != http.StatusUnauthorized {
				t.Errorf("%s GET %s: %d, want 401", m.email, path, code)
			}
		}
		if e.dir.signInAllowed(m.claims.Subject) {
			t.Errorf("%s still active in the directory", m.email)
		}
	}
	if _, err := e.dir.enrol(eInvite, ""); err == nil {
		t.Error("A's unused invitation to e@example.com still works")
	}
	d := e.inviteAndEnrol(e.admin, "d@example.com")
	code, p := d.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "b@example.com"}, nil)
	if code != http.StatusForbidden || p.Code != "deactivated" || !strings.Contains(p.Error, "only an admin") {
		t.Errorf("D re-invites B: %d %+v", code, p)
	}
	var inv server.Invitation
	if code, p := e.admin.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "b@example.com"}, &inv); code != http.StatusCreated || !inv.Reinvite {
		t.Fatalf("admin re-invites B: %d %+v %s", code, inv, p.Error)
	}
	// Using it reactivates the same account, credited to the admin (owner, 2026-10-13).
	id, _ := e.dir.invitationFor("b@example.com")
	claims, err := e.dir.enrol(id, "")
	if err != nil {
		t.Fatal(err)
	}
	b2, err := e.signIn(claims)
	if err != nil || b2.id != b.id {
		t.Fatalf("B enrols again: %v, id %d want %d", err, b2.id, b.id)
	}
	if me := e.me(b2); me.InvitedBy == nil || *me.InvitedBy != "admin@example.com" {
		t.Errorf("B's inviter after re-invitation: %v", me.InvitedBy)
	}
	t.Logf("A25: 3 deactivated, live connections closed after %v, A/B/C get 401, invitation to e cancelled; D refused (%q), admin accepted", closed.Round(time.Millisecond), p.Error)
}

// streamClosed opens a live connection as e.as and returns a channel that
// closes when the server ends it.
func (e *env) streamClosed(query string) <-chan struct{} {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(e.ctx, http.MethodGet, e.url+"/api/v1/stream?"+query, nil)
	req.AddCookie(e.as.cookie)
	before := e.srv.Hub.Len()
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		e.t.Fatalf("stream: %v %v", err, resp)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
	}()
	for e.srv.Hub.Len() == before {
		time.Sleep(10 * time.Millisecond)
	}
	return done
}

// A26. Reactivating one user.
func TestA26ReactivatingOneUser(t *testing.T) {
	e := setup(t)
	a := e.inviteAndEnrol(e.admin, "a@example.com")
	b := e.inviteAndEnrol(a, "b@example.com")
	c := e.inviteAndEnrol(b, "c@example.com")
	e.deactivateTree(e.admin, a.id)
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/reactivate", a.id), nil, nil); code != http.StatusNoContent {
		t.Fatalf("reactivate A: %d %s", code, p.Error)
	}
	if !e.canSignIn(a) {
		t.Error("A can't sign in after reactivation")
	}
	for _, m := range []*member{b, c} {
		if e.canSignIn(m) {
			t.Errorf("%s can sign in", m.email)
		}
	}
	t.Log("A26: A signs in again; B and C are still deactivated")
}

// A27. Deleting your own account.
func TestA27DeletingYourOwnAccount(t *testing.T) {
	e := setup(t)
	b := e.inviteAndEnrol(e.user, "b@example.com")
	c := e.inviteAndEnrol(b, "c@example.com")
	if code, _ := b.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "f@example.com"}, nil); code != http.StatusCreated {
		t.Fatal("B invites f")
	}
	fInvite, _ := e.dir.invitationFor("f@example.com")
	if code, p := b.call(http.MethodDelete, "/api/v1/me", nil, nil); code != http.StatusNoContent {
		t.Fatalf("B deletes their account: %d %s", code, p.Error)
	}
	if e.canSignIn(b) {
		t.Error("B can still sign in")
	}
	if code, _ := b.call(http.MethodGet, "/api/v1/me", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("B's session: %d", code)
	}
	if !e.canSignIn(c) {
		t.Error("C can't sign in")
	}
	if me := e.me(c); me.InvitedBy == nil || *me.InvitedBy != "deleted user" {
		t.Errorf("C's inviter: %v", me.InvitedBy)
	}
	if _, err := e.dir.enrol(fInvite, ""); err == nil {
		t.Error("B's unused invitation to f@example.com still works")
	}
	if rows := e.rowsContaining("b@example.com"); len(rows) > 0 {
		t.Errorf("B's address is still stored: %v", rows)
	}
	// The invite tree stays connected through the placeholder (owner, 2026-10-13).
	if res := e.deactivateTree(e.admin, e.user.id); res.Deactivated != 2 {
		t.Errorf("deactivating the tree above the deleted user: %+v", res)
	}
	if e.canSignIn(c) {
		t.Error("C still signs in after deactivating the tree above B")
	}
	t.Log(`A27: B can't sign in, C can and is credited to "deleted user", B's invitation to f cancelled, B's address gone`)
}

// A28. The last admin stays.
func TestA28TheLastAdminStays(t *testing.T) {
	e := setup(t)
	adminPath := func(m *member, op string) string { return fmt.Sprintf("/api/v1/admin/users/%d/%s", m.id, op) }
	checks := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"demoted", http.MethodPut, adminPath(e.admin, "role"), server.RoleRequest{Role: "user"}},
		{"deactivated", http.MethodPost, adminPath(e.admin, "deactivate"), server.DeactivateRequest{}},
		{"deleted", http.MethodDelete, "/api/v1/me", nil},
	}
	for _, c := range checks {
		if code, p := e.admin.call(c.method, c.path, c.body, nil); code != http.StatusConflict {
			t.Errorf("%s: %d %+v", c.name, code, p)
		}
	}
	// In an invite tree: P, invited by a user, becomes the only admin, then
	// deactivates the tree of the user who invited them.
	inviter := e.user
	p := e.inviteAndEnrol(inviter, "p@example.com")
	if code, _ := e.admin.call(http.MethodPut, adminPath(p, "role"), server.RoleRequest{Role: "admin"}, nil); code != http.StatusNoContent {
		t.Fatal("promote P")
	}
	if code, _ := p.call(http.MethodPut, adminPath(e.admin, "role"), server.RoleRequest{Role: "user"}, nil); code != http.StatusNoContent {
		t.Fatal("P demotes the first admin (second admin: succeeds)")
	}
	// The preview locks the last admin's tick (R42b), and a confirmation
	// that ticks them anyway is refused as a whole.
	pv := e.preview(p, inviter.id)
	if n := node(pv.Root, p.id); n == nil || n.Tick.Can || n.Tick.Start || n.Tick.Why != "last admin" {
		t.Errorf("the last admin in the preview: %+v", n)
	}
	var everyone []int64
	walk(pv.Root, func(n *server.TreeNode) { everyone = append(everyone, n.ID) })
	code, _, refused := e.confirm(p, inviter.id, pv.Version, everyone, "")
	pr := refused.Problem
	if code != http.StatusConflict || pr.Code != "last_admin" {
		t.Errorf("tree containing the last admin: %d %+v", code, pr)
	}
	if me := e.me(p); me.Role != "admin" {
		t.Errorf("P is %s", me.Role)
	}
	if !e.canSignIn(inviter) {
		t.Error("the refused tree deactivation changed something")
	}
	t.Logf("A28: demote, deactivate, delete and tree deactivation of the last admin refused (%q); with two admins, demoting one succeeded", pr.Error)
}

// A29. Signed in, and roles enforced by the server.
func TestA29SignedInAndRolesEnforced(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", "@@GARBLED 123", e.now)
	user := []string{"/api/v1/meta", "/api/v1/messages", "/api/v1/messages/1", "/api/v1/jobs/EX0000001",
		"/api/v1/capcodes/unaliased", "/api/v1/capcodes/0999999", "/api/v1/aliases/1", "/api/v1/me", "/api/v1/invitations",
		"/api/v1/stream", "/api/v1/unknown"}
	admin := []string{"/api/v1/admin/clients", "/api/v1/admin/users", "/api/v1/admin/invitations"}
	var nobody *member
	for _, p := range append(append([]string{}, user...), admin...) {
		resp, err := nobody.do(http.MethodGet, e.url+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("no session: GET %s: %d", p, resp.StatusCode)
		}
	}
	for _, w := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/invitations"}, {http.MethodDelete, "/api/v1/me"},
		{http.MethodPost, "/api/v1/admin/users/1/deactivate"}, {http.MethodPut, "/api/v1/admin/users/1/role"},
		{http.MethodGet, "/api/v1/admin/users/1/deactivation-preview"}, {http.MethodPost, "/api/v1/admin/users/1/deactivate-tree"},
		{http.MethodPut, "/api/v1/admin/users/1/trusted"},
	} {
		resp, _ := nobody.do(w.method, e.url+w.path, map[string]string{})
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("no session: %s %s: %d", w.method, w.path, resp.StatusCode)
		}
	}
	for _, p := range admin {
		if code, _ := e.user.call(http.MethodGet, p, nil, nil); code != http.StatusForbidden {
			t.Errorf("user: GET %s: %d", p, code)
		}
	}
	if code, _ := e.user.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", e.admin.id), server.DeactivateRequest{}, nil); code != http.StatusForbidden {
		t.Errorf("user deactivates the admin: %d", code)
	}
	var page server.MessagePage
	e.user.call(http.MethodGet, "/api/v1/messages", nil, &page)
	if len(page.Messages) != 1 || page.Messages[0].Flags != nil {
		t.Errorf("user sees flags: %+v", page.Messages)
	}
	for _, p := range admin {
		if code, _ := e.admin.call(http.MethodGet, p, nil, nil); code != http.StatusOK {
			t.Errorf("admin: GET %s: %d", p, code)
		}
	}
	e.admin.call(http.MethodGet, "/api/v1/messages", nil, &page)
	if len(page.Messages) != 1 || page.Messages[0].Flags == nil || !page.Messages[0].Flags.ParseFailed {
		t.Errorf("admin flags: %+v", page.Messages)
	}
	// Client page posting needs only the client's API key.
	if code, _ := e.post("a", "0100002", "QDstill posting", e.now); code != http.StatusOK {
		t.Errorf("client post: %d", code)
	}
	t.Logf("A29: %d endpoints refuse requests without a session (401), %d admin endpoints refuse users (403), users see no flags, admins do", len(user)+len(admin)+4, len(admin))
}

// Cross-site browser requests can't change anything (CSRF).
func TestCrossSiteRequestsRefused(t *testing.T) {
	e := setup(t)
	req, _ := http.NewRequest(http.MethodPost, e.url+"/api/v1/invitations", strings.NewReader(`{"email":"x@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(e.user.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site invite: %d", resp.StatusCode)
	}
	if _, sent := e.dir.invitationFor("x@example.com"); sent {
		t.Error("cross-site request sent an invitation")
	}
}

// Invitation rules beyond the checks: one account and one open invitation
// per address, compared without case or +tag (owner, 2026-10-13), and
// cancelling.
func TestInvitationRules(t *testing.T) {
	e := setup(t)
	for _, tt := range []struct {
		email, code string
	}{
		{"User+tag@Example.com", "already_member"}, // e.user's address
		{"not an address", "invalid_email"},
		{"Someone <s@example.com>", "invalid_email"},
		{"s@sub.example.com", "domain_not_allowed"},
	} {
		if code, p := e.user.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: tt.email}, nil); p.Code != tt.code {
			t.Errorf("%q: %d %+v, want %s", tt.email, code, p, tt.code)
		}
	}
	var inv server.Invitation
	if code, _ := e.user.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "n@example.com"}, &inv); code != http.StatusCreated {
		t.Fatal("invite n")
	}
	if _, p := e.admin.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "N+x@example.com"}, nil); p.Code != "already_invited" {
		t.Errorf("second invitation: %+v", p)
	}
	id, _ := e.dir.invitationFor("n@example.com")
	if code, _ := e.admin.call(http.MethodDelete, fmt.Sprintf("/api/v1/invitations/%d", inv.ID), nil, nil); code != http.StatusNoContent {
		t.Errorf("admin cancels: %d", code)
	}
	if _, err := e.dir.enrol(id, ""); err == nil {
		t.Error("cancelled invitation still works")
	}
	var mine []server.Invitation
	e.user.call(http.MethodGet, "/api/v1/invitations", nil, &mine)
	if len(mine) != 1 || mine[0].Status != "cancelled" {
		t.Errorf("my invitations: %+v", mine)
	}
}

// The first admin (R44), at the API level; A23 runs against Authentik.
func TestFirstAdminSetUp(t *testing.T) {
	e := setup(t)
	e.acc.InitialAdmin = "owner@example.org"
	if ok, _ := e.acc.FirstAdminAvailable(e.ctx); ok {
		t.Fatal("set-up offered while an admin exists")
	}
	if _, err := e.acc.FirstAdminEnrolment(e.ctx); err == nil {
		t.Fatal("set-up link while an admin exists")
	}
	// With no admin: the set-up link enrols owner@example.org, outside the
	// allowed domains, as admin.
	if _, err := e.st.Pool.Exec(e.ctx, "UPDATE users SET role = 'user'"); err != nil {
		t.Fatal(err)
	}
	link, err := e.acc.FirstAdminEnrolment(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.dir.enrol(link[strings.LastIndex(link, "=")+1:], "Owner")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := e.signIn(c)
	if err != nil {
		t.Fatal(err)
	}
	if me := e.me(owner); me.Role != "admin" || me.InvitedBy != nil {
		t.Errorf("owner: %+v", me)
	}
	if _, err := e.acc.FirstAdminEnrolment(e.ctx); err == nil {
		t.Error("set-up still offered after the first admin enrolled")
	}
}

// The app checks an invitation's expiry itself at first sign-in, not only
// Authentik at the start of enrolment (R43a).
func TestInvitationExpiredAtSignIn(t *testing.T) {
	e := setup(t)
	if code, _ := e.user.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "slow@example.com"}, nil); code != http.StatusCreated {
		t.Fatal("invite")
	}
	id, _ := e.dir.invitationFor("slow@example.com")
	c, err := e.dir.enrol(id, "") // enrolment started in time…
	if err != nil {
		t.Fatal(err)
	}
	e.acc.Now = func() time.Time { return time.Now().Add(accounts.InvitationLifetime + time.Minute) } // …finished too late
	if _, err := e.signIn(c); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("sign-in with an expired invitation: %v", err)
	}
	if e.dir.signInAllowed(c.Subject) {
		t.Error("the Authentik user from the expired invitation is still there")
	}
}

// At most one pending invitation per address, in the database (ADR 0004).
func TestOnePendingInvitationInDatabase(t *testing.T) {
	e := setup(t)
	if code, _ := e.user.call(http.MethodPost, "/api/v1/invitations", server.InviteRequest{Email: "once@example.com"}, nil); code != http.StatusCreated {
		t.Fatal("invite")
	}
	_, err := e.st.Pool.Exec(e.ctx, `INSERT INTO invitations (email, email_key, expires_at) VALUES ('Once@example.com', 'once@example.com', now() + interval '1 day')`)
	if err == nil || !strings.Contains(err.Error(), "23505") {
		t.Fatalf("second pending invitation inserted directly: %v", err)
	}
	t.Logf("second pending invitation refused by the database: %v", err)
}

// First-admin set-up changes state, so it's a same-site POST, never a GET.
func TestFirstAdminSetUpIsPost(t *testing.T) {
	e := setup(t)
	e.acc.InitialAdmin = "owner@example.org"
	if _, err := e.st.Pool.Exec(e.ctx, "UPDATE users SET role = 'user'"); err != nil {
		t.Fatal(err)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method string, headers map[string]string) *http.Response {
		req, _ := http.NewRequest(method, e.url+"/auth/first-admin", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := do(http.MethodGet, nil); r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", r.StatusCode)
	}
	if r := do(http.MethodPost, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); r.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST: %d", r.StatusCode)
	}
	if n := len(e.dir.invites); n != 0 {
		t.Errorf("%d set-up invitations made by refused requests", n)
	}
	var next server.Next
	if code, _ := e.as.call(http.MethodPost, "/auth/first-admin", nil, &next); code != http.StatusOK || !strings.HasPrefix(next.Redirect, "http://authentik.test/if/flow/enrol/?itoken=") {
		t.Errorf("same-site POST: %d %s", code, next.Redirect)
	}
}
