package stacktest

// Sign-in beside RadioArchive against the real Authentik (R92b-R92e,
// ADR 0052): the API restarted with RADIOARCHIVE_URL and LOOK_COOKIE_DOMAIN
// set (PA_STACK_PHASE=combined), and a made-up companion app's OIDC clients
// whose back-channel logout URLs are a receiver that keeps what Authentik
// posts (deploy/test/companion.yaml).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// companionCallback is the companion's registered redirect URI. Nothing
// answers there: the test plays the companion's server and takes the code.
const companionCallback = "http://companion.example.test/callback"

// hops records the URLs a browser asks for, while on.
type hops struct {
	mu   sync.Mutex
	on   bool
	urls []*url.URL
	next http.RoundTripper
}

func (h *hops) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	if h.on {
		u := *r.URL
		h.urls = append(h.urls, &u)
	}
	h.mu.Unlock()
	return h.next.RoundTrip(r)
}

// record starts recording b's requests; the returned func stops and
// returns them.
func (b *browser) record() func() []*url.URL {
	h, ok := b.client.Transport.(*hops)
	if !ok {
		next := b.client.Transport
		if next == nil {
			next = http.DefaultTransport
		}
		h = &hops{next: next}
		b.client.Transport = h
	}
	h.mu.Lock()
	h.on, h.urls = true, nil
	h.mu.Unlock()
	return func() []*url.URL {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.on = false
		return h.urls
	}
}

// walkTo follows redirects as walk does, through Authentik's flows with
// answers, until stop says a URL is where to stop (before asking for it),
// or a page doesn't redirect, on any host.
func (b *browser) walkTo(start string, answers answer, stop func(*url.URL) bool) (*url.URL, challenge) {
	b.t.Helper()
	next, _ := url.Parse(start)
	for range 40 {
		if stop(next) {
			return next, challenge{}
		}
		if m := flowPath.FindStringSubmatch(next.Path); m != nil && next.Host == b.s.authentik.Host {
			c := b.executor(m[1], next.RawQuery, nil)
			for c.Component != "xak-flow-redirect" {
				body := answers(c)
				if body == nil {
					c.query = next.RawQuery
					return nil, c
				}
				body["component"] = c.Component
				c = b.executor(m[1], next.RawQuery, body)
			}
			to, err := next.Parse(c.To)
			if err != nil {
				b.t.Fatalf("bad redirect %q", c.To)
			}
			next = to
			continue
		}
		resp, err := b.client.Get(next.String())
		if err != nil {
			b.t.Fatal(err)
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			return next, challenge{}
		}
		if next, err = next.Parse(loc); err != nil {
			b.t.Fatal(err)
		}
	}
	b.t.Fatal("too many redirects")
	return nil, challenge{}
}

// companionTokens names the Authentik session a companion sign-in came from.
type companionTokens struct {
	sub, sid string
}

// companionSignIn signs b in to the made-up companion app's client: in
// Authentik (with no prompt when it's signed in there already), then the
// code exchanged as the companion's server would, which gives the client
// the access token that Authentik's back-channel logout goes by.
func (b *browser) companionSignIn(client string) companionTokens {
	b.t.Helper()
	q := url.Values{"client_id": {client}, "redirect_uri": {companionCallback}, "response_type": {"code"},
		"scope": {"openid"}, "state": {"made-up-state"}}
	end, c := b.walkTo(b.s.authentik.JoinPath("/application/o/authorize/").String()+"?"+q.Encode(), b.signInAnswers(),
		func(u *url.URL) bool { return u.Host == "companion.example.test" })
	if end == nil || end.Host != "companion.example.test" || end.Query().Get("code") == "" {
		b.t.Fatalf("%s: signing in to %s stopped at %v %s %s", b.email, client, end, c.Component, c.errorText())
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {end.Query().Get("code")}, "redirect_uri": {companionCallback}}
	req, _ := http.NewRequest(http.MethodPost, b.s.authentik.JoinPath("/application/o/token/").String(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(client, "test-companion-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || resp.StatusCode != http.StatusOK {
		b.t.Fatalf("%s: the companion's code exchange: %d %v", b.email, resp.StatusCode, err)
	}
	var claims struct {
		Sub string `json:"sub"`
		Sid string `json:"sid"`
	}
	payload(b.t, tok.IDToken, &claims)
	if claims.Sub == "" || claims.Sid == "" {
		b.t.Fatalf("the companion's ID token has sub %q, sid %q", claims.Sub, claims.Sid)
	}
	return companionTokens{sub: claims.Sub, sid: claims.Sid}
}

// payload decodes a JWT's claims, unverified.
func payload(t *testing.T, jwt string, out any) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %.20q", jwt)
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(b, out) != nil {
		t.Fatalf("a JWT's claims: %v", err)
	}
}

// receiver reads the companion's back-channel logout receiver.
type receiver struct{ url string }

func (s *stack) receiver(t *testing.T) receiver {
	t.Helper()
	u := os.Getenv("PA_STACK_LOGOUT_RECEIVER_URL")
	if u == "" {
		t.Fatal("PA_STACK_LOGOUT_RECEIVER_URL is not set (scripts/stack-checks.sh sets it)")
	}
	return receiver{u}
}

type logoutPost struct {
	Path        string  `json:"path"`
	LogoutToken string  `json:"logout_token"`
	At          float64 `json:"at"`
}

func (r receiver) posts(t *testing.T) []logoutPost {
	t.Helper()
	resp, err := http.Get(r.url + "/received")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []logoutPost
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// logoutClaims are what a logout token says.
type logoutClaims struct {
	Iss    string                     `json:"iss"`
	Aud    any                        `json:"aud"`
	Sub    string                     `json:"sub"`
	Sid    string                     `json:"sid"`
	Events map[string]json.RawMessage `json:"events"`
	Nonce  *string                    `json:"nonce"`
}

// await waits up to within for a logout token posted to path for this
// Authentik session (sub and sid), verifies it with the client's keys, and
// returns its claims; ok is false if none came.
func (r receiver) await(t *testing.T, s *stack, path, client string, tok companionTokens, within time.Duration) (logoutClaims, bool) {
	t.Helper()
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		for _, p := range r.posts(t) {
			if p.Path != path {
				continue
			}
			var c logoutClaims
			payload(t, p.LogoutToken, &c)
			if c.Sub != tok.sub || c.Sid != tok.sid {
				continue
			}
			// Signed with the client's key, for the client: what the
			// companion would check (A151 checks PagerArchive's own).
			ctx := context.Background()
			pr, err := oidc.NewProvider(ctx, s.authentik.JoinPath("/application/o/", client, "/").String())
			if err != nil {
				t.Fatalf("%s's discovery: %v", client, err)
			}
			if _, err := pr.Verifier(&oidc.Config{ClientID: client}).Verify(ctx, p.LogoutToken); err != nil {
				t.Errorf("the logout token to %s doesn't verify: %v", path, err)
			}
			if _, ok := c.Events["http://schemas.openid.net/event/backchannel-logout"]; !ok || c.Nonce != nil {
				t.Errorf("the logout token to %s: events %v, nonce %v", path, c.Events, c.Nonce)
			}
			return c, true
		}
	}
	return logoutClaims{}, false
}

func keys(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// composeExec runs a command in a stack service.
func composeExec(t *testing.T, service string, args ...string) string {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose", "-f", "compose.yaml", "-f", "deploy/test/compose.yaml", "exec", "-T", service}, args...)...)
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", service, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// authentikSQL runs a query on Authentik's database.
func authentikSQL(t *testing.T, sql string) string {
	t.Helper()
	return composeExec(t, "db", "psql", "-qtA", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "authentik", "-c", sql)
}

// authentikPK is the Authentik user's ID (the OIDC subject) for a username.
func authentikPK(t *testing.T, username string) string {
	t.Helper()
	return authentikSQL(t, fmt.Sprintf("SELECT id FROM authentik_core_user WHERE username = '%s'", strings.ReplaceAll(username, "'", "''")))
}

// inUsersGroup reports whether the user is in Authentik's pagerarchive-users
// group, as the app's API token sees it.
func (s *stack) inUsersGroup(t *testing.T, username string) bool {
	t.Helper()
	q := url.Values{"username": {username}, "groups_by_name": {"pagerarchive-users"}}
	req, _ := http.NewRequest(http.MethodGet, s.authentik.JoinPath("/api/v3/core/users/").String()+"?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Pagination struct {
			Count int `json:"count"`
		} `json:"pagination"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("users in the group: %d %v", resp.StatusCode, err)
	}
	return out.Pagination.Count == 1
}

// akShell runs Python in Authentik's Django, as its admin pages' actions do:
// through the models, so Authentik's signals run.
func akShell(t *testing.T, py string) string {
	t.Helper()
	return composeExec(t, "authentik-worker", "ak", "shell", "-c", py)
}

// sessionEnds waits up to within for b's app session to stop working, and
// says whether it did.
func (b *browser) sessionEnds(within time.Duration) bool {
	b.t.Helper()
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if code, _ := b.api(http.MethodGet, "/api/v1/me", nil, nil); code == http.StatusUnauthorized {
			return true
		}
	}
	return false
}

func (b *browser) sessionWorks() bool {
	b.t.Helper()
	code, _ := b.api(http.MethodGet, "/api/v1/me", nil, nil)
	return code == http.StatusOK
}

// A149, standalone (R92c): no silent check, sign-in is as before.
func TestA149StandaloneHasNoSilentCheck(t *testing.T) {
	s := setup(t, "restarted")
	b := s.browser(t, "owner@example.org")
	resp, err := b.client.Get(s.app.JoinPath("/auth/login").String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc == nil || loc.Path != "/application/o/authorize/" || loc.Query().Has("prompt") {
		t.Fatalf("standalone sign-in goes to %v, want Authentik with no prompt", loc)
	}
	t.Logf("A149: standalone, sign-in goes straight to %s with no prompt", loc.Path)
}

// A149 (R92c): beside RadioArchive, the first sign-in of a browser session
// asks Authentik with prompt=none, once: signed in there already (here,
// through the companion app), it signs in with no prompt; not signed in,
// it carries on to Authentik's sign-in as before, with no loop.
func TestA149SilentSignIn(t *testing.T) {
	s := setup(t, "combined")
	isCheck := func(u *url.URL) bool {
		return u.Host == s.authentik.Host && u.Path == "/application/o/authorize/" && u.Query().Get("prompt") == "none"
	}
	summary := func(urls []*url.URL) (checks int, list []string) {
		for _, u := range urls {
			if isCheck(u) {
				checks++
			}
			if u.Host == s.app.Host || u.Path == "/application/o/authorize/" {
				q := u.Query()
				for _, k := range []string{"state", "nonce", "code_challenge", "code", "client_id", "redirect_uri", "scope", "response_type", "code_challenge_method"} {
					q.Del(k)
				}
				list = append(list, u.Path+"?"+q.Encode())
			}
		}
		return checks, list
	}

	// Not signed in at Authentik: one check, login_required, then the normal
	// sign-in, which asks.
	o := s.browser(t, "owner@example.org")
	stop := o.record()
	end, c := o.walk(s.app.JoinPath("/auth/login").String(), stopAnywhere)
	urls := stop()
	checks, list := summary(urls)
	if end != nil || c.Component != "ak-stage-identification" || checks != 1 {
		t.Fatalf("signed out: stopped at %v %q after %d checks; hops %v", end, c.Component, checks, list)
	}
	var sawRequired bool
	for _, u := range urls {
		sawRequired = sawRequired || (u.Path == "/auth/callback" && u.Query().Get("error") == "login_required")
	}
	if !sawRequired {
		t.Errorf("signed out: Authentik didn't answer login_required: %v", list)
	}
	t.Logf("A149: not signed in at Authentik: %d check, then Authentik's sign-in (%s); hops %v", checks, c.Component, list)
	// It signs in from there as before, and the session's next sign-in
	// isn't checked again.
	if ok, c := o.signIn(); !ok {
		t.Fatalf("signing in after the check: %s", c.errorText())
	}
	stop = o.record()
	o.walk(s.app.JoinPath("/auth/login").String(), stopAnywhere)
	if checks, list := summary(stop()); checks != 0 {
		t.Errorf("a second sign-in in the browser session was checked again: %v", list)
	}

	// Signed in at Authentik through the companion app: the check signs in
	// to PagerArchive with no prompt.
	x := s.browser(t, "owner@example.org").withPassword(o)
	x.companionSignIn("companion")
	stop = x.record()
	end, c = x.walk(s.app.JoinPath("/auth/login").String()+"?return="+url.QueryEscape("/jobs"), stopAnywhere)
	checks, list = summary(stop())
	if end == nil || end.Path != "/jobs" || checks != 1 {
		t.Fatalf("signed in through the companion: ended at %v %q after %d checks; hops %v", end, c.Component, checks, list)
	}
	if !x.sessionWorks() {
		t.Fatal("signed in by the check, but the session doesn't work")
	}
	t.Logf("A149: signed in at Authentik through the companion: the check signed in with no prompt, back to %s; hops %v", end.Path, list)
}

// A150 (R92d): Authentik sends back-channel logout tokens to the other
// clients of an Authentik session that ends: (a) on a sign-out through the
// end-session endpoint (PagerArchive's), (b) on a user disabled (here by
// PagerArchive's deactivation, which ends the user's Authentik sessions) and
// on a session revoked. Only to clients whose access token from that session
// is unexpired: the short-token client gets none once its token has expired.
func TestA150SignOutReachesTheOtherApp(t *testing.T) {
	s := setup(t, "combined")
	r := s.receiver(t)
	o := s.owner(t)

	// (a) Signing out of PagerArchive.
	a := s.inviteAndEnrol(t, o, "a150a@example.com")
	ta := a.companionSignIn("companion")
	short := a.companionSignIn("companion-short")
	if short.sid != ta.sid {
		t.Fatalf("one browser's two sign-ins have Authentik sessions %q and %q", ta.sid, short.sid)
	}
	time.Sleep(7 * time.Second) // the short client's 5-second access token expires
	start := time.Now()
	a.signOut()
	c, ok := r.await(t, s, "/companion", "companion", ta, 30*time.Second)
	if !ok {
		t.Fatal("(a) signing out of PagerArchive: no logout token reached the companion in 30 s")
	}
	t.Logf("A150 (a): signing out of PagerArchive: a logout token reached the companion %v later: iss %s, aud %v, sub %s, sid %s, events %v",
		time.Since(start).Round(100*time.Millisecond), c.Iss, c.Aud, c.Sub, c.Sid, keys(c.Events))
	if _, ok := r.await(t, s, "/companion-short", "companion-short", short, 10*time.Second); ok {
		t.Error("the short-token client got a logout token after its access token expired")
	} else {
		t.Log("A150 (a): the client whose access token had expired got none: Authentik sends only to clients holding an unexpired access token from the session")
	}

	// (b) Deactivated in PagerArchive: Authentik disables the user and ends
	// their sessions, each with a logout token.
	d := s.inviteAndEnrol(t, o, "a150b@example.com")
	td := d.companionSignIn("companion")
	pk := authentikPK(t, "a150b@example.com")
	start = time.Now()
	if code, msg := o.api(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", s.userID(t, o, "a150b@example.com")), map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", code, msg)
	}
	if _, ok := r.await(t, s, "/companion", "companion", td, 30*time.Second); !ok {
		t.Fatal("(b) deactivated in PagerArchive: no logout token reached the companion in 30 s")
	}
	if n := authentikSQL(t, "SELECT count(*) FROM authentik_core_authenticatedsession WHERE user_id = "+pk); n != "0" {
		t.Errorf("(b) deactivated: %s Authentik sessions left, want 0", n)
	}
	t.Logf("A150 (b): deactivated in PagerArchive: their Authentik sessions ended (0 left) and a logout token reached the companion %v later", time.Since(start).Round(100*time.Millisecond))

	// (b) A session revoked in Authentik (its admin pages' action): to the
	// companion, and to PagerArchive, whose session ends.
	v := s.inviteAndEnrol(t, o, "a150c@example.com")
	tv := v.companionSignIn("companion")
	start = time.Now()
	akShell(t, `from authentik.core.models import AuthenticatedSession; AuthenticatedSession.objects.filter(user__username="a150c@example.com").delete()`)
	if _, ok := r.await(t, s, "/companion", "companion", tv, 30*time.Second); !ok {
		t.Fatal("(b) a session revoked in Authentik: no logout token reached the companion in 30 s")
	}
	if !v.sessionEnds(30 * time.Second) {
		t.Fatal("(b) a session revoked in Authentik: PagerArchive's session still works after 30 s")
	}
	t.Logf("A150 (b): a session revoked in Authentik: a logout token reached the companion, and PagerArchive's session ended, %v later", time.Since(start).Round(100*time.Millisecond))
}

// A151 (R92d): a sign-out in the other app, or the user disabled there,
// reaches PagerArchive by back-channel logout: the sessions from that
// Authentik session end, and the same user's other browsers keep theirs.
func TestA151BackChannelIntoPagerArchive(t *testing.T) {
	s := setup(t, "combined")
	o := s.owner(t)
	p := s.inviteAndEnrol(t, o, "a151@example.com")
	other := s.browser(t, p.email).withPassword(p) // the same person's other browser
	if ok, c := other.signIn(); !ok {
		t.Fatalf("signing in on another browser: %s", c.errorText())
	}
	p.companionSignIn("companion")

	// Signing out of the companion (its RP-initiated logout).
	start := time.Now()
	end, _ := p.walkTo(s.authentik.JoinPath("/application/o/companion/end-session/").String(), stopAnywhere,
		func(u *url.URL) bool { return u.Host == "companion.example.test" })
	if !p.sessionEnds(30 * time.Second) {
		t.Fatalf("signed out of the companion (ended at %v): PagerArchive's session still works after 30 s", end)
	}
	if !other.sessionWorks() {
		t.Error("the other browser's session ended too")
	}
	t.Logf("A151: signed out of the companion: PagerArchive's session from that Authentik session ended %v later; the other browser's kept",
		time.Since(start).Round(100*time.Millisecond))

	// Disabled in the other app (it disables the user in the shared
	// Authentik, as PagerArchive does; here with PagerArchive's API token).
	pk := authentikPK(t, p.email)
	setActive := func(active bool) {
		t.Helper()
		body := strings.NewReader(fmt.Sprintf(`{"is_active":%v}`, active))
		req, _ := http.NewRequest(http.MethodPatch, s.authentik.JoinPath("/api/v3/core/users/", pk, "/").String(), body)
		req.Header.Set("Authorization", "Bearer "+s.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("set active %v: %v %v", active, err, resp)
		}
		resp.Body.Close()
	}
	start = time.Now()
	setActive(false)
	defer setActive(true)
	if !other.sessionEnds(30 * time.Second) {
		t.Fatal("disabled in Authentik by the other app: PagerArchive's session still works after 30 s")
	}
	t.Logf("A151: disabled in Authentik by the other app: PagerArchive's session ended %v later", time.Since(start).Round(100*time.Millisecond))
}

// A152 (R92e): enrolment puts the user in Authentik's pagerarchive-users
// group and sets email_proven to the address the code proved; a user who
// isn't in the group (one from before) is added when the API starts.
func TestA152GroupAndProvenAddress(t *testing.T) {
	s := setup(t, "combined")
	o := s.owner(t)
	s.inviteAndEnrol(t, o, "a152@example.com")
	pk := authentikPK(t, "a152@example.com")
	proven := authentikSQL(t, "SELECT attributes->>'email_proven' FROM authentik_core_user WHERE id = "+pk)
	if in := s.inUsersGroup(t, "a152@example.com"); !in || proven != "a152@example.com" {
		t.Fatalf("after enrolment: in pagerarchive-users %v, email_proven %q", in, proven)
	}
	t.Logf("A152: enrolled: in pagerarchive-users, email_proven %q", proven)

	// The owner as if enrolled before: out of the group; the API's start
	// puts them back.
	owner := authentikPK(t, "owner@example.org")
	akShell(t, `from authentik.core.models import Group, User; Group.objects.get(name="pagerarchive-users").users.remove(User.objects.get(pk=`+owner+`))`)
	if s.inUsersGroup(t, "owner@example.org") {
		t.Fatal("the owner is still in the group")
	}
	cmd := exec.Command("docker", "compose", "-f", "compose.yaml", "-f", "deploy/test/compose.yaml", "restart", "api")
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restart the API: %v: %s", err, out)
	}
	start := time.Now()
	for !s.inUsersGroup(t, "owner@example.org") {
		if time.Since(start) > 90*time.Second {
			t.Fatal("the API's start didn't add the owner to pagerarchive-users in 90 s")
		}
		time.Sleep(time.Second)
	}
	for end := time.Now().Add(60 * time.Second); ; time.Sleep(time.Second) {
		if resp, err := http.Get(s.app.JoinPath("/healthz").String()); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(end) {
			t.Fatal("the API didn't come back")
		}
	}
	t.Logf("A152: an account not in the group was added by the API's start, %v after the restart", time.Since(start).Round(time.Second))
}

// A148 (R92b) against the real sign-in: signing in copies the account's
// look into the shared look cookie, on LOOK_COOKIE_DOMAIN.
func TestA148SignInCopiesTheLook(t *testing.T) {
	s := setup(t, "combined")
	o := s.owner(t)
	if code, msg := o.api(http.MethodPatch, "/api/v1/me", map[string]string{"look": "console"}, nil); code != http.StatusOK {
		t.Fatalf("choose Console: %d %s", code, msg)
	}
	defer o.api(http.MethodPatch, "/api/v1/me", map[string]string{"look": "harbour"}, nil)
	b := s.browser(t, "owner@example.org").withPassword(o)
	var set []string
	b.client.Transport = setCookies{next: http.DefaultTransport, seen: &set}
	if ok, c := b.signIn(); !ok {
		t.Fatalf("sign in: %s", c.errorText())
	}
	var look string
	for _, c := range set {
		if strings.HasPrefix(c, "look=") {
			look = c
		}
	}
	domain := os.Getenv("PA_STACK_LOOK_COOKIE_DOMAIN")
	if !strings.HasPrefix(look, "look=console;") || !strings.Contains(look, "Domain="+domain) || !strings.Contains(look, "Path=/") ||
		!strings.Contains(look, "SameSite=Lax") || strings.Contains(look, "HttpOnly") {
		t.Fatalf("signing in set the look cookie %q, want console on %s", look, domain)
	}
	t.Logf("A148: signing in set %s", look)
}

// setCookies records the Set-Cookie headers of the answers a browser gets.
type setCookies struct {
	next http.RoundTripper
	seen *[]string
}

func (s setCookies) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := s.next.RoundTrip(r)
	if err == nil {
		*s.seen = append(*s.seen, resp.Header.Values("Set-Cookie")...)
	}
	return resp, err
}
