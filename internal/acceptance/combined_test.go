package acceptance

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// Sign-in beside RadioArchive (R92b-R92d, ADR 0052) against a made-up
// Authentik: OIDC discovery, its signing key, and a token endpoint that
// signs ID tokens for whoever the test says is signed in. The real
// Authentik's side is in internal/stacktest (A148-A152).

// oidcAuthentik is a made-up Authentik's browser provider.
type oidcAuthentik struct {
	srv *httptest.Server
	key *rsa.PrivateKey
	mu  sync.Mutex
	// sub and sid are the user and Authentik session the next ID token is for.
	sub, sid string
	nonce    string // the nonce the next ID token carries
}

func newOIDCAuthentik(t *testing.T) *oidcAuthentik {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &oidcAuthentik{key: k}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := f.srv.URL + "/application/o/"
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/application/o/pagerarchive/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": f.issuer(), "authorization_endpoint": base + "authorize/", "token_endpoint": base + "token/",
				"jwks_uri": base + "pagerarchive/jwks/", "end_session_endpoint": base + "pagerarchive/end-session/",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/application/o/pagerarchive/jwks/":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "kid": "made-up-key", "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
			}}})
		case "/application/o/token/":
			f.mu.Lock()
			claims := map[string]any{"iss": f.issuer(), "aud": "pagerarchive", "sub": f.sub, "sid": f.sid, "nonce": f.nonce,
				"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "made-up-access-token", "token_type": "Bearer", "expires_in": 300,
				"id_token": f.sign(t, "JWT", claims)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *oidcAuthentik) issuer() string { return f.srv.URL + "/application/o/pagerarchive/" }

func (f *oidcAuthentik) sign(t *testing.T, typ string, claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	body := enc(map[string]string{"alg": "RS256", "typ": typ, "kid": "made-up-key"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// logoutToken is a logout token as Authentik sends it, for sub and sid.
func (f *oidcAuthentik) logoutToken(t *testing.T, sub, sid string) string {
	c := map[string]any{"iss": f.issuer(), "aud": "pagerarchive", "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
		"jti": "made-up", "sub": sub, "events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}}}
	if sid != "" {
		c["sid"] = sid
	}
	return f.sign(t, "logout+jwt", c)
}

// combinedSetup is setup's server signing in with the made-up Authentik.
func combinedSetup(t *testing.T) (*env, *oidcAuthentik) {
	e := setup(t)
	ak := newOIDCAuthentik(t)
	app, _ := neturl.Parse("http://pager.example.test")
	e.srv.AppURL = app
	e.srv.OIDC = &server.OIDC{Issuer: ak.issuer(), ClientID: "pagerarchive", ClientSecret: "made-up",
		RedirectURL: "http://pager.example.test/auth/callback", HTTP: ak.srv.Client()}
	return e, ak
}

// get is one hop: the response, with no redirect followed.
func (e *env) get(path string, cookies ...*http.Cookie) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.url+path, nil)
	for _, c := range cookies {
		if c != nil && c.MaxAge >= 0 && c.Value != "" {
			req.AddCookie(c)
		}
	}
	resp, err := signOutClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// loginState is what the sign-in cookie holds (auth.go's loginState).
type loginState struct {
	State, Nonce string
	Silent       bool
}

func readLoginCookie(t *testing.T, cs []*http.Cookie) (loginState, *http.Cookie) {
	t.Helper()
	c := cookieNamed(cs, "pa_login")
	if c == nil {
		t.Fatal("no sign-in cookie")
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	var st struct {
		S, N string
		Q    bool
	}
	if err != nil || json.Unmarshal(b, &st) != nil {
		t.Fatalf("the sign-in cookie: %q", c.Value)
	}
	return loginState{State: st.S, Nonce: st.N, Silent: st.Q}, c
}

// A149 (R92c): beside RadioArchive, the first sign-in of a browser session
// asks Authentik with prompt=none, once; no Authentik session (or any
// error) carries on to the normal sign-in, the return path kept; standalone
// never asks.
func TestA149SilentSignInCheck(t *testing.T) {
	e, ak := combinedSetup(t)
	authorize := func(resp *http.Response) neturl.Values {
		t.Helper()
		loc, err := neturl.Parse(resp.Header.Get("Location"))
		if err != nil || resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc.String(), ak.srv.URL+"/application/o/authorize/") {
			t.Fatalf("sign-in went to %d %q, want Authentik's authorize endpoint", resp.StatusCode, resp.Header.Get("Location"))
		}
		return loc.Query()
	}

	// Standalone: as before, never prompt=none.
	resp := e.get("/auth/login?return=/jobs")
	if q := authorize(resp); q.Has("prompt") {
		t.Errorf("standalone: prompt=%q", q.Get("prompt"))
	}
	if cookieNamed(resp.Cookies(), "pa_silent") != nil {
		t.Error("standalone: the check was marked")
	}

	e.srv.RadioArchiveURL = "http://radio.example.test"
	// First sign-in of the browser session: prompt=none, and marked for the
	// session (no expiry), before it's made.
	resp = e.get("/auth/login?return=/jobs")
	q := authorize(resp)
	if q.Get("prompt") != "none" {
		t.Fatalf("beside RadioArchive, the first sign-in: prompt=%q, want none", q.Get("prompt"))
	}
	mark := cookieNamed(resp.Cookies(), "pa_silent")
	if mark == nil || mark.MaxAge != 0 || !mark.Expires.IsZero() || !mark.HttpOnly || mark.Path != "/auth/" {
		t.Fatalf("the check's mark: %+v, want a session cookie", mark)
	}
	st, login := readLoginCookie(t, resp.Cookies())
	if !st.Silent {
		t.Error("the sign-in cookie doesn't say it's the check")
	}
	// Authentik answers login_required: on to the normal sign-in, return kept.
	resp = e.get("/auth/callback?error=login_required&state="+neturl.QueryEscape(st.State), login, mark)
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != "/auth/login?checked=1&return=%2Fjobs" {
		t.Fatalf("login_required went to %d %q, want the normal sign-in", resp.StatusCode, loc)
	}
	// That normal sign-in, with the mark or without it (cookies blocked), is
	// as before: no loop.
	for _, cs := range [][]*http.Cookie{{mark}, nil} {
		if q := authorize(e.get("/auth/login?checked=1&return=%2Fjobs", cs...)); q.Has("prompt") {
			t.Errorf("after the check (mark %v): prompt=%q", cs != nil, q.Get("prompt"))
		}
	}
	// A later sign-in in the same browser session isn't checked again.
	if q := authorize(e.get("/auth/login?return=/", mark)); q.Has("prompt") {
		t.Errorf("a second sign-in in the session: prompt=%q", q.Get("prompt"))
	}
	t.Log("A149: standalone: no prompt=none; beside RadioArchive: prompt=none once per browser session, login_required on to /auth/login?checked=1&return=%2Fjobs, then as before")

	// Any other failure of the check (here an ID token for another sign-in:
	// its nonce differs) also goes on.
	resp = e.get("/auth/login?return=/map")
	st, login = readLoginCookie(t, resp.Cookies())
	ak.mu.Lock()
	ak.sub, ak.sid, ak.nonce = "fixture-user@example.com", "made-up-sid", "not-"+st.Nonce
	ak.mu.Unlock()
	resp = e.get("/auth/callback?code=made-up&state="+neturl.QueryEscape(st.State), login)
	if loc := resp.Header.Get("Location"); loc != "/auth/login?checked=1&return=%2Fmap" {
		t.Errorf("a failed check went to %q, want the normal sign-in", loc)
	}
	// Signed in at Authentik: the check signs in, with no prompt, and back
	// to the page asked for.
	resp = e.get("/auth/login?return=/map")
	st, login = readLoginCookie(t, resp.Cookies())
	ak.mu.Lock()
	ak.nonce = st.Nonce
	ak.mu.Unlock()
	resp = e.get("/auth/callback?code=made-up&state="+neturl.QueryEscape(st.State), login)
	session := cookieNamed(resp.Cookies(), "pa_session")
	if resp.Header.Get("Location") != "/map" || session == nil {
		t.Fatalf("a check with an Authentik session: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, _ := (&member{e: e, cookie: session}).call(http.MethodGet, "/api/v1/me", nil, nil); code != http.StatusOK {
		t.Errorf("signed in by the check: GET /api/v1/me %d", code)
	}
	t.Log("A149: a failed check goes on to the normal sign-in; with an Authentik session the check signs in, back to /map")
}

// A148 (R92b): with LOOK_COOKIE_DOMAIN set, the page's first paint is the
// shared look cookie's look, then the account's, then Harbour, on every
// page; the account's look is copied into the cookie at sign-in and a
// change saves to both. Standalone, a stray cookie changes nothing.
func TestA148LookCookie(t *testing.T) {
	e, ak := combinedSetup(t)
	b := e.user
	if code, _ := e.setLook(b, "console"); code != http.StatusOK {
		t.Fatal("B chooses Console")
	}
	stray := &http.Cookie{Name: "look", Value: "harbour"}
	pageWith := func(path string, cs ...*http.Cookie) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cs {
			r.AddCookie(c)
		}
		look, shared := e.srv.PageLook(r)
		if shared {
			return look + "+shared"
		}
		return look
	}
	// Standalone: the account's look, the cookie ignored, and no cookie
	// written by a change.
	if got := pageWith("/", b.cookie, stray); got != "console" {
		t.Errorf("standalone with a stray cookie: %q, want the account's", got)
	}
	if got := pageWith("/signin", stray); got != "" {
		t.Errorf("standalone sign-in page: %q, want Harbour as built", got)
	}
	resp, _ := b.do(http.MethodPatch, e.url+"/api/v1/me", server.MeUpdate{Look: "console"})
	resp.Body.Close()
	if c := cookieNamed(resp.Cookies(), "look"); c != nil {
		t.Errorf("standalone: a change wrote the look cookie: %+v", c)
	}

	e.srv.LookCookieDomain = "example.test"
	for _, c := range []struct {
		path string
		cs   []*http.Cookie
		want string
	}{
		{"/", []*http.Cookie{b.cookie, stray}, "harbour+shared"},                         // the cookie wins
		{"/", []*http.Cookie{b.cookie}, "console+shared"},                                // none: the account's
		{"/", []*http.Cookie{b.cookie, {Name: "look", Value: "neon"}}, "console+shared"}, // not a look: the account's
		{"/signin", []*http.Cookie{{Name: "look", Value: "console"}}, "console+shared"},  // before sign-in too
		{"/signin", nil, "harbour+shared"},                                               // nothing: Harbour
	} {
		if got := pageWith(c.path, c.cs...); got != c.want {
			t.Errorf("%s with %v: %q, want %q", c.path, c.cs, got, c.want)
		}
	}
	// A change while signed in saves to both.
	resp, _ = b.do(http.MethodPatch, e.url+"/api/v1/me", server.MeUpdate{Look: "harbour"})
	resp.Body.Close()
	c := cookieNamed(resp.Cookies(), "look")
	if resp.StatusCode != http.StatusOK || c == nil || c.Value != "harbour" || c.Domain != "example.test" || c.Path != "/" ||
		c.MaxAge != 365*24*3600 || c.SameSite != http.SameSiteLaxMode || c.HttpOnly {
		t.Errorf("a change: %d, look cookie %+v", resp.StatusCode, c)
	}
	if me := e.me(b); me.Look != "harbour" {
		t.Errorf("a change: the account has %q", me.Look)
	}
	t.Logf("A148: a change while signed in: the account has harbour, and Set-Cookie %s", c.String())

	// At sign-in, the account's look goes into the cookie.
	if code, _ := e.setLook(b, "console"); code != http.StatusOK {
		t.Fatal("B chooses Console")
	}
	resp = e.get("/auth/login?return=/")
	st, login := readLoginCookie(t, resp.Cookies())
	ak.mu.Lock()
	ak.sub, ak.sid, ak.nonce = "fixture-user@example.com", "made-up-sid", st.Nonce
	ak.mu.Unlock()
	resp = e.get("/auth/callback?code=made-up&state="+neturl.QueryEscape(st.State), login)
	c = cookieNamed(resp.Cookies(), "look")
	if cookieNamed(resp.Cookies(), "pa_session") == nil || c == nil || c.Value != "console" || c.Domain != "example.test" {
		t.Fatalf("signing in: %d, look cookie %+v", resp.StatusCode, c)
	}
	t.Logf("A148: signing in wrote Set-Cookie %s", c.String())
}

// A151 (R92d): a logout token from Authentik ends this app's browser
// sessions from that Authentik session, and closes the user's live
// connections; a token without a sid ends all of the user's; a bad token
// ends nothing and is refused with 400.
func TestA151BackChannelLogout(t *testing.T) {
	e, ak := combinedSetup(t)
	sub := "fixture-user@example.com"
	signedIn := func(sid string) *member {
		t.Helper()
		idt := ak.sign(t, "JWT", map[string]any{"iss": ak.issuer(), "aud": "pagerarchive", "sub": sub, "sid": sid, "nonce": "n"})
		token, err := e.acc.NewSession(e.ctx, e.user.id, idt)
		if err != nil {
			t.Fatal(err)
		}
		return &member{e: e, id: e.user.id, email: e.user.email, cookie: &http.Cookie{Name: "pa_session", Value: token}}
	}
	works := func(m *member) bool {
		code, _ := m.call(http.MethodGet, "/api/v1/me", nil, nil)
		return code == http.StatusOK
	}
	post := func(token string) (int, string) {
		t.Helper()
		resp, err := http.PostForm(e.url+"/auth/backchannel-logout", neturl.Values{"logout_token": {token}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control %q", resp.Header.Get("Cache-Control"))
		}
		return resp.StatusCode, string(b)
	}

	laptop, laptopTab, phone := signedIn("sid-laptop"), signedIn("sid-laptop"), signedIn("sid-phone")
	// Bad tokens end nothing.
	bad := map[string]string{
		"an ID token":    ak.sign(t, "JWT", map[string]any{"iss": ak.issuer(), "aud": "pagerarchive", "sub": sub, "sid": "sid-laptop", "nonce": "n", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}),
		"another client": newOIDCAuthentik(t).logoutToken(t, sub, "sid-laptop"),
		"none":           "",
	}
	for what, tok := range bad {
		if code, body := post(tok); code != http.StatusBadRequest || !strings.Contains(body, "invalid_request") {
			t.Errorf("%s: %d %s, want 400 invalid_request", what, code, body)
		}
	}
	if !works(laptop) || !works(laptopTab) || !works(phone) {
		t.Fatal("a refused token ended a session")
	}
	// The laptop's Authentik session ended: both of this app's sessions
	// signed in from it end, the phone's stays.
	if code, _ := post(ak.logoutToken(t, sub, "sid-laptop")); code != http.StatusOK {
		t.Fatalf("a good logout token: %d", code)
	}
	if works(laptop) || works(laptopTab) || !works(phone) {
		t.Errorf("after the laptop's logout: laptop %v, its other session %v, phone %v; want only the phone's", works(laptop), works(laptopTab), works(phone))
	}
	// Again (Authentik retries), or for someone unknown: 200, nothing more.
	for _, tok := range []string{ak.logoutToken(t, sub, "sid-laptop"), ak.logoutToken(t, "nobody", "x")} {
		if code, _ := post(tok); code != http.StatusOK {
			t.Errorf("a token with nothing to end: %d, want 200", code)
		}
	}
	// No sid: all of the user's browser sessions.
	if code, _ := post(ak.logoutToken(t, sub, "")); code != http.StatusOK || works(phone) {
		t.Errorf("a token without a sid: %d, the phone's still works %v", code, works(phone))
	}
	if !works(e.admin) {
		t.Error("someone else's session ended")
	}
	if logs := e.logs.String(); strings.Contains(logs, "eyJ") {
		t.Error("a token is in the log")
	}
	t.Log("A151: bad tokens refused with 400 and nothing ended; the laptop's logout token ended both sessions from it and kept the phone's; a token with no sid ended the rest")
}
