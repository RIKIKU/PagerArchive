// Package stacktest runs the account checks (A23-A28) against the real
// compose stack: the API, Authentik and a Mailpit mail catcher, as
// scripts/stack-checks.sh starts them. Skipped unless PA_STACK_APP_URL is
// set.
//
// A23 has two phases around a restart of the API with a changed
// INITIAL_ADMIN_EMAIL: PA_STACK_PHASE=first, then PA_STACK_PHASE=restarted.
package stacktest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type stack struct {
	app, authentik *url.URL
	mailpit        mailpit
	token          string // the app's Authentik API token
}

func setup(t *testing.T, phase string) *stack {
	t.Helper()
	if os.Getenv("PA_STACK_APP_URL") == "" {
		t.Skip("PA_STACK_APP_URL is not set (run scripts/stack-checks.sh)")
	}
	if os.Getenv("PA_STACK_PHASE") != phase {
		t.Skipf("phase %s only", phase)
	}
	parse := func(name string) *url.URL {
		u, err := url.Parse(os.Getenv(name))
		if err != nil || u.Host == "" {
			t.Fatalf("%s is not a URL", name)
		}
		return u
	}
	return &stack{app: parse("PA_STACK_APP_URL"), authentik: parse("PA_STACK_AUTHENTIK_URL"),
		mailpit: mailpit{parse("PA_STACK_MAILPIT_URL")}, token: os.Getenv("PA_STACK_AUTHENTIK_TOKEN")}
}

// authentikUser returns whether a user with this username exists in
// Authentik and is active.
func (s *stack) authentikUser(t *testing.T, username string) (exists, active bool) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.authentik.JoinPath("/api/v3/core/users/").String()+"?username="+url.QueryEscape(username), nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Results []struct {
			IsActive bool `json:"is_active"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("authentik users: %d %v", resp.StatusCode, err)
	}
	if len(out.Results) == 0 {
		return false, false
	}
	return true, out.Results[0].IsActive
}

// invite has b invite address and returns the emailed link.
func (b *browser) invite(address string) string {
	b.t.Helper()
	since := time.Now()
	if code, msg := b.api(http.MethodPost, "/api/v1/invitations", map[string]string{"email": address}, nil); code != http.StatusCreated {
		b.t.Fatalf("%s invites %s: %d %s", b.email, address, code, msg)
	}
	return b.s.mailpit.invitationLink(b.t, address, since)
}

// enrol follows an invitation link to a signed-in session.
func (b *browser) enrol(link string) {
	b.t.Helper()
	end, c := b.walk(link, b.enrolAnswers("", nil))
	if end == nil || end.Path == "/signin" {
		b.t.Fatalf("%s didn't enrol: stopped at %v %s %.300s", b.email, end, c.errorText(), c.raw)
	}
}

// inviteAndEnrol: inviter invites address, who enrols and is signed in.
func (s *stack) inviteAndEnrol(t *testing.T, inviter *browser, address string) *browser {
	t.Helper()
	b := s.browser(t, address)
	b.enrol(inviter.invite(address))
	return b
}

type me struct {
	ID        int64   `json:"id"`
	Role      string  `json:"role"`
	InvitedBy *string `json:"invited_by"`
}

func (b *browser) me() me {
	b.t.Helper()
	var m me
	if code, msg := b.api(http.MethodGet, "/api/v1/me", nil, &m); code != http.StatusOK {
		b.t.Fatalf("%s: GET /api/v1/me: %d %s", b.email, code, msg)
	}
	return m
}

// owner signs the first admin in (A23 created them in phase "first").
func (s *stack) owner(t *testing.T) *browser {
	t.Helper()
	o := s.browser(t, "owner@example.org")
	if ok, c := o.signIn(); !ok {
		t.Fatalf("owner can't sign in: %s %s", c.errorText(), c.raw)
	}
	return o
}

// A23, before the restart: the first admin enrols without an invitation,
// and a wrong code is refused.
func TestA23FirstAdmin(t *testing.T) {
	s := setup(t, "first")
	var status struct {
		FirstAdminSetup bool `json:"first_admin_setup"`
	}
	resp, err := http.Get(s.app.JoinPath("/auth/status").String())
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if !status.FirstAdminSetup {
		t.Fatal("fresh install doesn't offer first-admin set-up")
	}
	o := s.browser(t, "owner@example.org")
	var wrong string
	end, c := o.walk(o.postForRedirect("/auth/first-admin"), o.enrolAnswers("000000", &wrong))
	if wrong == "" {
		t.Fatal("the wrong code wasn't refused")
	}
	if end == nil || end.Path == "/signin" {
		t.Fatalf("owner didn't enrol: %v %s", end, c.errorText())
	}
	if m := o.me(); m.Role != "admin" || m.InvitedBy != nil {
		t.Fatalf("owner: %+v", m)
	}
	t.Logf("A23: wrong code refused (%q); with the emailed code, owner@example.org is signed in as admin", wrong)
}

// A23, after the restart with INITIAL_ADMIN_EMAIL=second@example.org.
func TestA23SecondAddressCantEnrol(t *testing.T) {
	s := setup(t, "restarted")
	var status struct {
		FirstAdminSetup bool `json:"first_admin_setup"`
	}
	resp, err := http.Get(s.app.JoinPath("/auth/status").String())
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if status.FirstAdminSetup {
		t.Error("first-admin set-up still offered")
	}
	b := s.browser(t, "second@example.org")
	end, _ := b.walk(b.postForRedirect("/auth/first-admin"), b.enrolAnswers("", nil))
	if end == nil || end.Path != "/signin" || !strings.Contains(end.RawQuery, "admin_exists") {
		t.Errorf("first-admin link: ended at %v", end)
	}
	// Enrolment itself needs an invitation.
	_, c := b.walk(s.authentik.JoinPath("/if/flow/pagerarchive-enrolment/").String(), b.enrolAnswers("", nil))
	if c.Component != "ak-stage-access-denied" {
		t.Errorf("enrolment without an invitation: %s %s", c.Component, c.errorText())
	}
	if exists, _ := s.authentikUser(t, "second@example.org"); exists {
		t.Error("second@example.org exists in Authentik")
	}
	t.Logf("A23: after the restart, second@example.org can't enrol: set-up %v, enrolment %q", end, c.errorText())
}

// A24. Invitations.
func TestA24Invitations(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	a := s.inviteAndEnrol(t, o, "a24a@example.com")
	link := a.invite("a24b@example.com")
	b := s.browser(t, "a24b@example.com")
	b.enrol(link)
	m := b.me()
	if m.Role != "user" || m.InvitedBy == nil || !strings.Contains(*m.InvitedBy, "a24a@example.com") {
		t.Fatalf("B: %+v", m)
	}
	again := s.browser(t, "a24b@example.com")
	if _, c := again.walk(link, again.enrolAnswers("", nil)); c.Component != "ak-stage-access-denied" {
		t.Errorf("reused invitation: %s", c.Component)
	}
	code, msg := a.api(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "c@example.org"}, nil)
	if code != http.StatusUnprocessableEntity {
		t.Errorf("c@example.org: %d %s", code, msg)
	}
	// An invitation created 7 days and 1 minute ago: its Authentik expiry
	// (created + 7 days) is a minute in the past.
	late := a.invite("a24late@example.com")
	s.expireInvitation(t, late, time.Now().Add(-time.Minute))
	lb := s.browser(t, "a24late@example.com")
	_, c := lb.walk(late, lb.enrolAnswers("", nil))
	if c.Component != "ak-stage-access-denied" {
		t.Errorf("expired invitation: %s", c.Component)
	}
	t.Logf("A24: B is %s, invited by %s; reused invitation %q; c@example.org: %d %q; expired invitation %q", m.Role, *m.InvitedBy, "access denied", code, msg, c.errorText())
}

func (s *stack) expireInvitation(t *testing.T, link string, at time.Time) {
	t.Helper()
	id := link[strings.LastIndex(link, "=")+1:]
	body := strings.NewReader(fmt.Sprintf(`{"expires":%q}`, at.UTC().Format(time.RFC3339)))
	req, _ := http.NewRequest(http.MethodPatch, s.authentik.JoinPath("/api/v3/stages/invitation/invitations/", id, "/").String(), body)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("backdate invitation: %v %v", err, resp)
	}
	resp.Body.Close()
}

// stream opens /api/v1/stream as b; the channel closes when it ends.
func (b *browser) stream(ctx context.Context) <-chan struct{} {
	b.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, b.s.app.JoinPath("/api/v1/stream").String(), nil)
	resp, err := b.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		b.t.Fatalf("%s stream: %v %v", b.email, err, resp)
	}
	done := make(chan struct{})
	connected := make(chan struct{})
	go func() {
		defer close(done)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if sc.Text() == ": connected" {
				close(connected)
			}
		}
	}()
	<-connected
	return done
}

// A25 and A26: deactivating an invite tree, then reactivating one user.
func TestA25A26InviteTree(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	a := s.inviteAndEnrol(t, o, "a25a@example.com")
	b := s.inviteAndEnrol(t, a, "a25b@example.com")
	c := s.inviteAndEnrol(t, b, "a25c@example.com")
	eLink := a.invite("a25e@example.com")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bStream, cStream := b.stream(ctx), c.stream(ctx)

	start := time.Now()
	var res struct {
		Deactivated int `json:"deactivated"`
	}
	if code, msg := o.api(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", a.me().ID), map[string]bool{"tree": true}, &res); code != http.StatusOK || res.Deactivated != 3 {
		t.Fatalf("deactivate A's tree: %d %s %+v", code, msg, res)
	}
	for name, ch := range map[string]<-chan struct{}{"B": bStream, "C": cStream} {
		select {
		case <-ch:
		case <-time.After(5*time.Second - time.Since(start)):
			t.Fatalf("%s's live connection is still open after 5 s", name)
		}
	}
	closed := time.Since(start)
	for _, m := range []*browser{a, b, c} {
		for _, p := range []string{"/api/v1/messages", "/api/v1/stream", "/api/v1/me"} {
			if code, _ := m.api(http.MethodGet, p, nil, nil); code != http.StatusUnauthorized {
				t.Errorf("%s GET %s: %d, want 401", m.email, p, code)
			}
		}
		if exists, active := s.authentikUser(t, m.email); !exists || active {
			t.Errorf("%s in Authentik: exists %v, active %v", m.email, exists, active)
		}
	}
	eb := s.browser(t, "a25e@example.com")
	if _, ch := eb.walk(eLink, eb.enrolAnswers("", nil)); ch.Component != "ak-stage-access-denied" {
		t.Errorf("A's invitation to e still works: %s", ch.Component)
	}
	d := s.inviteAndEnrol(t, o, "a25d@example.com")
	code, msg := d.api(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "a25b@example.com"}, nil)
	if code != http.StatusForbidden || !strings.Contains(msg, "deactivated") || !strings.Contains(msg, "only an admin") {
		t.Errorf("D re-invites B: %d %q", code, msg)
	}
	t.Logf("A25: live connections closed %v after the request; A, B, C get 401 and are disabled in Authentik; e's invitation no longer works; D refused: %q", closed.Round(time.Millisecond), msg)

	// A26: reactivate A only. (Before the admin re-invites B, which
	// replaces B's Authentik user.)
	if code, msg := o.api(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/reactivate", s.userID(t, o, "a25a@example.com")), nil, nil); code != http.StatusNoContent {
		t.Fatalf("reactivate A: %d %s", code, msg)
	}
	if ok, ch := s.browser(t, "a25a@example.com").withPassword(a).signIn(); !ok {
		t.Errorf("A can't sign in after reactivation: %s", ch.errorText())
	}
	for _, m := range []*browser{b, c} {
		if ok, _ := s.browser(t, m.email).withPassword(m).signIn(); ok {
			t.Errorf("%s can sign in", m.email)
		}
	}
	t.Log("A26: A signs in again; B and C still can't")

	if code, msg := o.api(http.MethodPost, "/api/v1/invitations", map[string]string{"email": "a25b@example.com"}, nil); code != http.StatusCreated {
		t.Errorf("admin re-invites B: %d %s", code, msg)
	}
	t.Log("A25: the admin's invitation to B is accepted")
}

func (b *browser) withPassword(other *browser) *browser { b.pw = other.pw; return b }

func (s *stack) userID(t *testing.T, admin *browser, email string) int64 {
	t.Helper()
	var users []struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	}
	admin.api(http.MethodGet, "/api/v1/admin/users", nil, &users)
	for _, u := range users {
		if u.Email == email {
			return u.ID
		}
	}
	t.Fatalf("no user %s", email)
	return 0
}

// A27. Deleting your own account.
func TestA27DeletingYourOwnAccount(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	b := s.inviteAndEnrol(t, o, "a27b@example.com")
	c := s.inviteAndEnrol(t, b, "a27c@example.com")
	fLink := b.invite("a27f@example.com")
	if code, msg := b.api(http.MethodDelete, "/api/v1/me", nil, nil); code != http.StatusNoContent {
		t.Fatalf("B deletes their account: %d %s", code, msg)
	}
	if ok, _ := s.browser(t, b.email).withPassword(b).signIn(); ok {
		t.Error("B can still sign in")
	}
	if exists, _ := s.authentikUser(t, b.email); exists {
		t.Error("B still exists in Authentik")
	}
	c2 := s.browser(t, c.email).withPassword(c)
	if ok, ch := c2.signIn(); !ok {
		t.Fatalf("C can't sign in: %s", ch.errorText())
	}
	if m := c2.me(); m.InvitedBy == nil || *m.InvitedBy != "deleted user" {
		t.Errorf("C's inviter: %v", m.InvitedBy)
	}
	fb := s.browser(t, "a27f@example.com")
	if _, ch := fb.walk(fLink, fb.enrolAnswers("", nil)); ch.Component != "ak-stage-access-denied" {
		t.Errorf("B's invitation to f still works: %s", ch.Component)
	}
	t.Log(`A27: B can't sign in and is gone from Authentik; C signs in, credited to "deleted user"; B's invitation to f no longer works`)
}

// A28. The last admin stays.
func TestA28TheLastAdminStays(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	p1 := s.inviteAndEnrol(t, o, "a28p1@example.com")
	p2 := s.inviteAndEnrol(t, p1, "a28p2@example.com")
	p2id, p1id, oid := p2.me().ID, p1.me().ID, o.me().ID
	if code, msg := o.api(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", p2id), map[string]string{"role": "admin"}, nil); code != http.StatusNoContent {
		t.Fatalf("promote P2: %d %s", code, msg)
	}
	// Given a second admin, demoting the first succeeds.
	if code, msg := p2.api(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", oid), map[string]string{"role": "user"}, nil); code != http.StatusNoContent {
		t.Fatalf("demote the owner with a second admin: %d %s", code, msg)
	}
	// Now P2 is the only admin.
	refusals := map[string]struct {
		method, path string
		body         any
	}{
		"demoted":     {http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", p2id), map[string]string{"role": "user"}},
		"deactivated": {http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", p2id), map[string]bool{"tree": false}},
		"deleted":     {http.MethodDelete, "/api/v1/me", nil},
		"in a tree":   {http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", p1id), map[string]bool{"tree": true}},
	}
	for name, r := range refusals {
		if code, msg := p2.api(r.method, r.path, r.body, nil); code != http.StatusConflict {
			t.Errorf("last admin %s: %d %s", name, code, msg)
		} else {
			t.Logf("A28: last admin %s: refused (%s)", name, msg)
		}
	}
	if m := p2.me(); m.Role != "admin" {
		t.Errorf("P2 is %s", m.Role)
	}
	if exists, active := s.authentikUser(t, p1.email); !exists || !active {
		t.Error("the refused tree deactivation changed P1")
	}
	// Leave the owner as an admin for later runs.
	if code, msg := p2.api(http.MethodPut, fmt.Sprintf("/api/v1/admin/users/%d/role", oid), map[string]string{"role": "admin"}, nil); code != http.StatusNoContent {
		t.Fatalf("restore the owner: %d %s", code, msg)
	}
}

// Password reset (owner, 2026-10-13: email and password sign-in; the
// emailed code is used for enrolment and password reset only).
func TestPasswordResetWithEmailedCode(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	u := s.inviteAndEnrol(t, o, "reset@example.com")
	oldPassword := u.pw

	r := s.browser(t, u.email)
	r.pw = "a completely new pass phrase"
	var since time.Time // set when the code is requested, so the enrolment code isn't picked up
	c := r.executor("pagerarchive-recovery", "", nil)
	for steps := 0; c.Component != "xak-flow-redirect"; steps++ {
		if steps > 8 {
			t.Fatalf("reset didn't finish: %s %s", c.Component, c.errorText())
		}
		var body map[string]any
		switch c.Component {
		case "ak-stage-identification":
			body = map[string]any{"uid_field": u.email}
		case "ak-stage-authenticator-validate":
			var v struct {
				Devices []map[string]any `json:"device_challenges"`
			}
			_ = json.Unmarshal(c.raw, &v)
			if strings.Contains(c.errorText(), "Empty response") {
				// The email device was selected and its code sent.
				body = map[string]any{"code": s.mailpit.code(t, u.email, since)}
			} else if len(v.Devices) == 1 && v.Devices[0]["device_class"] == "email" {
				since = time.Now()
				body = map[string]any{"selected_challenge": v.Devices[0]}
			} else {
				t.Fatalf("code stage: %.300s", c.raw)
			}
		case "ak-stage-prompt":
			body = map[string]any{"password": r.pw, "password_repeat": r.pw}
		default:
			t.Fatalf("unexpected %s: %s", c.Component, c.errorText())
		}
		body["component"] = c.Component
		c = r.executor("pagerarchive-recovery", "", body)
	}
	if ok, ch := s.browser(t, u.email).withPassword(r).signIn(); !ok {
		t.Fatalf("new password doesn't work: %s", ch.errorText())
	}
	old := s.browser(t, u.email)
	old.pw = oldPassword
	if ok, _ := old.signIn(); ok {
		t.Error("old password still works")
	}
	t.Log("password reset with the emailed code: the new password works, the old one doesn't")
}

// Authentik's own administrator (akadmin) must set up and use a second
// factor when signing in through the public sign-in flow (ADR 0004).
func TestAuthentikAdminNeedsSecondFactor(t *testing.T) {
	s := setup(t, "restarted")
	email, password := os.Getenv("PA_STACK_AKADMIN_EMAIL"), os.Getenv("PA_STACK_AKADMIN_PASSWORD")
	if email == "" {
		t.Skip("PA_STACK_AKADMIN_EMAIL is not set")
	}
	a := s.browser(t, email)
	a.pw = password
	ok, c := a.signIn()
	if ok || (c.Component != "ak-stage-authenticator-totp" && c.Component != "ak-stage-authenticator-validate") {
		t.Fatalf("akadmin after the password: signed in %v, at %s %s", ok, c.Component, c.errorText())
	}
	t.Logf("akadmin after a correct password: %s (second factor required)", c.Component)
	// An ordinary user isn't asked.
	if ok, msg := s.passwordAttempt(t, "owner@example.org", s.owner(t).pw); !ok {
		t.Errorf("owner: %s", msg)
	}
}
