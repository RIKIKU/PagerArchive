package acceptance

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// fakeAuthentik answers OIDC discovery for the browser's provider the way
// Authentik does, with its end-session endpoint; down makes it answer 503,
// and slow makes it take longer than sign-out waits.
type fakeAuthentik struct {
	srv        *httptest.Server
	down, slow atomic.Bool
}

func newFakeAuthentik(t *testing.T) *fakeAuthentik {
	f := &fakeAuthentik{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.slow.Load() {
			select {
			case <-time.After(10 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		if f.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/application/o/pagerarchive/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		base := f.srv.URL + "/application/o/"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                base + "pagerarchive/",
			"authorization_endpoint":                base + "authorize/",
			"token_endpoint":                        base + "token/",
			"jwks_uri":                              base + "pagerarchive/jwks/",
			"end_session_endpoint":                  base + "pagerarchive/end-session/",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// signOutClient doesn't follow redirects: each hop is checked.
var signOutClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// signOut posts /auth/logout as the browser app does, with the session
// cookie, and returns the answer's redirect and its cookies.
func (e *env) signOut(session *http.Cookie) (string, []*http.Cookie, string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.url+"/auth/logout", nil)
	if session != nil {
		req.AddCookie(session)
	}
	resp, err := signOutClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var next server.Next
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &next) != nil {
		e.t.Fatalf("POST /auth/logout: %d %s", resp.StatusCode, body)
	}
	return next.Redirect, resp.Cookies(), string(body)
}

// hop gets path with these cookies and returns where it redirects to.
func (e *env) hop(path string, cookies []*http.Cookie) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.url+path, nil)
	for _, c := range cookies {
		if c.MaxAge >= 0 && c.Value != "" {
			req.AddCookie(c)
		}
	}
	resp, err := signOutClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp, resp.Header.Get("Location")
}

func cookieNamed(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// A117 (R40b, ADR 0038): signing out ends the app's session at once and
// sends the browser on to Authentik's end-session endpoint with the
// session's ID token and the sign-in page to come back to, the token never
// in an answer the app's pages read, nor in the log. A session from before
// ID tokens were kept is sent without them. When Authentik can't be
// reached, the session still ends and the sign-in page is told.
func TestA117SignOut(t *testing.T) {
	e := setup(t)
	ak := newFakeAuthentik(t)
	app, _ := neturl.Parse("http://pager.example.test")
	e.srv.AppURL = app
	e.srv.OIDC = &server.OIDC{Issuer: ak.srv.URL + "/application/o/pagerarchive/", ClientID: "pagerarchive", HTTP: ak.srv.Client()}
	endSession := ak.srv.URL + "/application/o/pagerarchive/end-session/"
	const idToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJtYWRlLXVwIn0.made-up-signature"

	signedIn := func(idt string) *http.Cookie {
		t.Helper()
		token, err := e.acc.NewSession(e.ctx, e.user.id, idt)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "pa_session", Value: token}
	}
	sessionGone := func(c *http.Cookie) {
		t.Helper()
		m := &member{e: e, cookie: c}
		if code, _ := m.call(http.MethodGet, "/api/v1/me", nil, nil); code != http.StatusUnauthorized {
			t.Fatalf("the session still works after signing out: %d", code)
		}
	}

	// With an ID token: to the hop, then to Authentik with it.
	session := signedIn(idToken)
	next, cookies, body := e.signOut(session)
	if next != "/auth/end-session" {
		t.Fatalf("signing out goes to %q, want /auth/end-session", next)
	}
	if strings.Contains(body, "eyJ") {
		t.Fatalf("the answer the app's script reads holds the ID token: %s", body)
	}
	sessionGone(session)
	if c := cookieNamed(cookies, "pa_session"); c == nil || c.MaxAge >= 0 {
		t.Errorf("the session cookie wasn't cleared: %+v", c)
	}
	handle := cookieNamed(cookies, "pa_logout")
	if handle == nil || !handle.HttpOnly || handle.Path != "/auth/end-session" || handle.SameSite != http.SameSiteLaxMode || strings.Contains(handle.Value, "eyJ") {
		t.Fatalf("the sign-out's handle cookie: %+v", handle)
	}
	resp, loc := e.hop(next, cookies)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the hop to Authentik: %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	to, err := neturl.Parse(loc)
	if err != nil || to.Scheme+"://"+to.Host+to.Path != endSession {
		t.Fatalf("the hop goes to %q, want Authentik's end-session endpoint", loc)
	}
	if q := to.Query(); q.Get("id_token_hint") != idToken || q.Get("post_logout_redirect_uri") != "http://pager.example.test/signin?signed_out=1" || len(q) != 2 {
		t.Errorf("the end-session request's query: %v", q)
	}
	if c := cookieNamed(resp.Cookies(), "pa_logout"); c == nil || c.MaxAge >= 0 {
		t.Errorf("the handle cookie wasn't cleared: %+v", c)
	}
	t.Logf("A117: signed out: the app's session ended, the answer to the page said %s, and the hop went to Authentik's end-session endpoint with the ID token and %s", strings.TrimSpace(body), to.Query().Get("post_logout_redirect_uri"))

	// The handle works once.
	if _, again := e.hop(next, cookies); again != "/signin?signed_out=1" {
		t.Errorf("the handle used again goes to %q, want the sign-in page", again)
	}
	// With no handle (a link from elsewhere): the sign-in page, not Authentik.
	if _, none := e.hop(next, nil); none != "/signin?signed_out=1" {
		t.Errorf("the hop with no sign-out goes to %q", none)
	}

	// A session from before ID tokens were kept: still to Authentik, which
	// ends its session, without a hint or a return address it would refuse.
	session = signedIn("")
	next, cookies, _ = e.signOut(session)
	sessionGone(session)
	if _, loc = e.hop(next, cookies); loc != endSession {
		t.Errorf("an older session's sign-out goes to %q, want %q", loc, endSession)
	}
	t.Logf("A117: a session with no ID token: ended, and sent to %s with no query", loc)

	// No session at all (it had expired): Authentik's may still be open.
	next, cookies, _ = e.signOut(nil)
	if _, loc = e.hop(next, cookies); loc != endSession {
		t.Errorf("signing out with no session goes to %q, want %q", loc, endSession)
	}

	// Authentik unreachable, answering an error or not at all: the session
	// still ends, within a few seconds, and the sign-in page is told.
	for _, how := range []string{"down", "slow"} {
		ak.down.Store(how == "down")
		ak.slow.Store(how == "slow")
		session = signedIn(idToken)
		start := time.Now()
		next, cookies, _ = e.signOut(session)
		took := time.Since(start)
		sessionGone(session)
		if next != "/signin?signed_out=1&authentik=unreachable" {
			t.Errorf("Authentik %s: signing out goes to %q", how, next)
		}
		if took > 6*time.Second {
			t.Errorf("Authentik %s: signing out took %s", how, took)
		}
		if c := cookieNamed(cookies, "pa_logout"); c != nil && c.MaxAge >= 0 {
			t.Errorf("Authentik %s: a handle was set: %+v", how, c)
		}
		t.Logf("A117: Authentik %s: signed out of the app in %s, sent to %s", how, took.Round(time.Millisecond), next)
	}
	ak.down.Store(false)
	ak.slow.Store(false)

	if logs := e.logs.String(); strings.Contains(logs, idToken) || strings.Contains(logs, "eyJ") {
		t.Error("the API's log holds the ID token")
	}
}
