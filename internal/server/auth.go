package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

const (
	sessionCookie = "pa_session"
	loginCookie   = "pa_login"
	// logoutCookie carries a sign-out's one-time handle to endSession.
	logoutCookie = "pa_logout"
)

// OIDC signs users in with Authentik (R40). The provider is discovered on
// first use, so the API starts (and serves clients) while Authentik is
// still starting.
type OIDC struct {
	Issuer       string // e.g. https://auth.example.org/application/o/pagerarchive/
	ClientID     string
	ClientSecret string
	RedirectURL  string       // APP_URL/auth/callback
	HTTP         *http.Client // reaches Authentik (see authentik.Transport)

	mu         sync.Mutex
	config     *oauth2.Config
	verifier   *oidc.IDTokenVerifier
	endSession string
}

func (o *OIDC) ctx(ctx context.Context) context.Context { return oidc.ClientContext(ctx, o.HTTP) }

func (o *OIDC) setup() (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.config != nil {
		return o.config, o.verifier, nil
	}
	// The provider keeps this context to fetch signing keys later, so it
	// mustn't be a request's.
	p, err := oidc.NewProvider(o.ctx(context.Background()), o.Issuer)
	if err != nil {
		return nil, nil, err
	}
	var extra struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = p.Claims(&extra)
	o.endSession = extra.EndSession
	o.verifier = p.Verifier(&oidc.Config{ClientID: o.ClientID})
	o.config = &oauth2.Config{
		ClientID: o.ClientID, ClientSecret: o.ClientSecret, RedirectURL: o.RedirectURL,
		Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "email", "profile", "pagerarchive"},
	}
	return o.config, o.verifier, nil
}

// reachable reports whether Authentik answers, within authentikWait, with
// the provider's discovery document: what signing out goes by to tell the
// person Authentik's sign-out couldn't be reached (R40b).
func (o *OIDC) reachable(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, authentikWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(o.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return err
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discovery answered %d", resp.StatusCode)
	}
	return nil
}

// endSessionURL is the provider's end-session endpoint, a fresh copy.
func (o *OIDC) endSessionURL() (*url.URL, error) {
	if _, _, err := o.setup(); err != nil {
		return nil, err
	}
	o.mu.Lock()
	end := o.endSession
	o.mu.Unlock()
	if end == "" {
		return nil, errors.New("Authentik named no end-session endpoint")
	}
	return url.Parse(end)
}

type userKey struct{}

// currentUser is the signed-in user of a request that passed signedIn.
func currentUser(r *http.Request) db.User {
	u, _ := r.Context().Value(userKey{}).(db.User)
	return u
}

type tokenExpiryKey struct{}

// tokenExpiry is when the PowerShell module's access token of a request
// that passed signedIn expires; ok is false for a browser session.
func tokenExpiry(r *http.Request) (t time.Time, ok bool) {
	t, ok = r.Context().Value(tokenExpiryKey{}).(time.Time)
	return t, ok
}

// signedIn admits only requests with a live session of an active user
// (A29), or with the PowerShell module's access token for an active admin
// (R60-R62). Deactivation deletes sessions, so it takes effect at once
// (R43b); tokens are checked against the user on every request.
func (s *Server) signedIn(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if raw, ok := bearerToken(r); ok {
			// Both at once would leave it unclear who is acting.
			if err == nil {
				writeJSON(w, http.StatusBadRequest, Problem{Error: "Send a session cookie or a bearer token, not both.", Code: CodeTwoCredentials})
				return
			}
			u, expires, err := s.bearerUser(r.Context(), raw)
			var refusal *errTokenRefused
			if errors.As(err, &refusal) {
				refuseToken(w, refusal)
				return
			}
			if err != nil {
				if s.abandoned(w, r, err) {
					return
				}
				problem(w, http.StatusServiceUnavailable, "couldn't check the sign-in")
				return
			}
			ctx := context.WithValue(context.WithValue(r.Context(), userKey{}, u), tokenExpiryKey{}, expires)
			role := u.Role
			if accounts.IsServiceAccount(u) {
				role = "service_account"
			}
			telemetry.SetUser(ctx, u.ID, role)
			h(w, r.WithContext(ctx))
			return
		}
		var u db.User
		if err == nil {
			u, err = s.Accounts.Session(r.Context(), c.Value)
		}
		if errors.Is(err, accounts.ErrNoSession) || errors.Is(err, http.ErrNoCookie) {
			problem(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if err != nil {
			if s.abandoned(w, r, err) {
				return
			}
			s.Log.ErrorContext(r.Context(), "session", "error", err.Error())
			problem(w, http.StatusInternalServerError, "couldn't check the session")
			return
		}
		telemetry.SetUser(r.Context(), u.ID, u.Role)
		h(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	}
}

func (s *Server) secureCookies() bool { return s.AppURL != nil && s.AppURL.Scheme == "https" }

func (s *Server) setCookie(w http.ResponseWriter, name, value, path string, maxAge time.Duration) {
	// A negative maxAge deletes the cookie (Max-Age=0): as a Duration, -1 is
	// a nanosecond, which would round to no Max-Age at all.
	age := int(maxAge.Seconds())
	if maxAge < 0 {
		age = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: age,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

func randomString() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// localPath keeps the post-sign-in return path on this site (R40a). It
// returns p when p is a plain path on this site, optionally with a query and
// fragment, and "/" for anything else.
//
// Browsers drop tabs and newlines anywhere in a URL and read a backslash as
// a slash, so a path that starts "/" + TAB + "/" or "/\" lands on another
// host. So the whole value is refused if it holds a control character,
// whitespace, a backslash or a non-ASCII byte. Its path, decoded once, is
// checked the same way, must not start with "//", and must not lead to
// /auth/ (the sign-in itself). Only the path is decoded for that check, so a
// query such as "?q=SHED%20FIRE" is kept.
func localPath(p string) string {
	if !plainPath(p) {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return "/"
	}
	raw, _, _ := strings.Cut(p, "?")
	raw, _, _ = strings.Cut(raw, "#")
	dec, err := url.PathUnescape(raw)
	if err != nil || !plainPath(dec) {
		return "/"
	}
	if c := path.Clean(dec); c == "/auth" || strings.HasPrefix(c, "/auth/") {
		return "/"
	}
	return p
}

// plainPath reports whether p starts with exactly one "/" and holds no
// control character, whitespace, backslash or non-ASCII byte.
func plainPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return false
	}
	for i := 0; i < len(p); i++ {
		if b := p[i]; b <= ' ' || b >= 0x7f || b == '\\' {
			return false
		}
	}
	return true
}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Return   string `json:"r"`
	// Silent is the silent sign-in check (R92c): sent with prompt=none.
	Silent bool `json:"q,omitempty"`
}

// signInFailed sends the browser to the app's sign-in page with a reason.
func signInFailed(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/signin?error="+url.QueryEscape(code), http.StatusSeeOther)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := s.OIDC.setup()
	if err != nil {
		s.Log.Error("oidc discovery", "error", err.Error())
		signInFailed(w, r, "directory_unavailable")
		return
	}
	st := loginState{State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(), Return: localPath(r.URL.Query().Get("return"))}
	opts := []oauth2.AuthCodeOption{oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)}
	// R92c: beside RadioArchive, the first sign-in of a browser session
	// first asks Authentik, without a prompt, whether it's already signed
	// in. Marked before it's made, so it's made once whatever comes back.
	_, unmarked := r.Cookie(silentCookie)
	if s.silentCheck() && unmarked != nil && r.URL.Query().Get("checked") == "" {
		st.Silent = true
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "none"))
		s.setCookie(w, silentCookie, "1", "/auth/", 0)
	}
	b, _ := json.Marshal(st)
	s.setCookie(w, loginCookie, base64.RawURLEncoding.EncodeToString(b), "/auth/", 10*time.Minute)
	http.Redirect(w, r, cfg.AuthCodeURL(st.State, opts...), http.StatusFound)
}

// silentFailed carries a silent sign-in check that found no Authentik
// session, or failed, on to the normal sign-in (R92c), and reports whether
// it did: false for a sign-in that wasn't the check.
func (s *Server) silentFailed(w http.ResponseWriter, r *http.Request, st loginState, why string) bool {
	if !st.Silent {
		return false
	}
	s.Log.Info("silent sign-in check: going on to sign in", "reason", why)
	http.Redirect(w, r, normalSignIn(st.Return), http.StatusSeeOther)
	return true
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	var st loginState
	c, err := r.Cookie(loginCookie)
	if err == nil {
		var b []byte
		if b, err = base64.RawURLEncoding.DecodeString(c.Value); err == nil {
			err = json.Unmarshal(b, &st)
		}
	}
	q := r.URL.Query()
	if err != nil || st.State == "" || q.Get("state") != st.State {
		signInFailed(w, r, "expired")
		return
	}
	s.setCookie(w, loginCookie, "", "/auth/", -1)
	if q.Get("error") != "" {
		// login_required, the usual answer: no Authentik session.
		if s.silentFailed(w, r, st, q.Get("error")) {
			return
		}
		s.Log.Warn("sign-in refused by Authentik", "error", q.Get("error"), "description", q.Get("error_description"))
		signInFailed(w, r, "refused")
		return
	}
	cfg, verifier, err := s.OIDC.setup()
	if err != nil {
		s.Log.Error("oidc discovery", "error", err.Error())
		signInFailed(w, r, "directory_unavailable")
		return
	}
	ctx := s.OIDC.ctx(r.Context())
	tok, err := cfg.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		s.Log.Warn("oidc code exchange failed", "error", err.Error())
		if !s.silentFailed(w, r, st, "code exchange failed") {
			signInFailed(w, r, "expired")
		}
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := verifier.Verify(ctx, raw)
	if err != nil || idt.Nonce != st.Nonce {
		s.Log.Warn("oidc id token rejected", "error", errString(err))
		if !s.silentFailed(w, r, st, "id token rejected") {
			signInFailed(w, r, "expired")
		}
		return
	}
	var claims struct {
		Email      string `json:"email"`
		Name       string `json:"name"`
		Invitation string `json:"pagerarchive_invitation"`
	}
	if err := idt.Claims(&claims); err != nil {
		signInFailed(w, r, "expired")
		return
	}
	u, err := s.Accounts.SignIn(r.Context(), accounts.Claims{Subject: idt.Subject, Email: claims.Email, Name: claims.Name, Invitation: claims.Invitation})
	var refusal *accounts.Error
	if errors.As(err, &refusal) {
		s.Log.Info("sign-in refused", "code", refusal.Code)
		signInFailed(w, r, refusal.Code)
		return
	}
	if err != nil {
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.Error("sign-in", "error", err.Error())
		signInFailed(w, r, "error")
		return
	}
	// The ID token is kept with the session for signing out (R40b), never
	// sent to the browser's pages or logged.
	token, err := s.Accounts.NewSession(r.Context(), u.ID, raw)
	if err != nil {
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.Error("sign-in", "error", err.Error())
		signInFailed(w, r, "error")
		return
	}
	// The server's 30-day expiry, renewed by use, decides; the cookie just
	// lives as long as browsers allow.
	s.setCookie(w, sessionCookie, token, "/", 400*24*time.Hour)
	// R92b: beside RadioArchive, the account's look goes into the shared
	// look cookie, so both apps' next pages are drawn in it.
	s.setLookCookie(w, pickLook("", u.Look))
	s.Log.Info("signed in", "user", u.ID)
	// Checked again here: the cookie may be from before an upgrade.
	http.Redirect(w, r, localPath(st.Return), http.StatusSeeOther)
}

func errString(err error) string {
	if err == nil {
		return "nonce mismatch"
	}
	return err.Error()
}

// signedOutPage is where signing out ends (R40b).
const signedOutPage = "/signin?signed_out=1"

// signedOutUnreachable is where it ends when Authentik couldn't be reached
// to end its session too.
const signedOutUnreachable = signedOutPage + "&authentik=unreachable"

// authentikWait bounds how long signing out waits to hear from Authentik.
const authentikWait = 3 * time.Second

// endSessionPath is the hop from the app to Authentik's end-session
// endpoint, which adds the ID token where the app's pages can't read it.
const endSessionPath = "/auth/end-session"

// Next is where the browser app should go next: after signing out, or to
// start the first admin's enrolment.
type Next struct {
	Redirect string `json:"redirect"`
}

// logout signs one browser out of the app and sends it on to end its
// Authentik session too (R40b, ADR 0038). The app's session ends here
// whatever Authentik does. The session's ID token, which Authentik needs to
// send the browser back to the app's sign-in page, is held for a moment
// under a one-time handle in an HttpOnly cookie, and added to the URL by
// endSession: this answer, which the app's script reads, never has it.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	idToken := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		if idToken, err = s.Accounts.EndSession(r.Context(), c.Value); err != nil {
			if s.abandoned(w, r, err) {
				return
			}
			s.Log.Error("sign-out", "error", err.Error())
		}
	}
	s.setCookie(w, sessionCookie, "", "/", -1)
	out := Next{Redirect: signedOutPage}
	if s.OIDC != nil {
		if err := s.OIDC.reachable(r.Context()); err != nil {
			s.Log.Warn("sign-out: Authentik couldn't be reached, so its session wasn't ended", "error", err.Error())
			out.Redirect = signedOutUnreachable
		} else {
			handle := randomString()
			s.signOuts.put(handle, idToken, s.Now())
			s.setCookie(w, logoutCookie, handle, endSessionPath, signOutHold)
			out.Redirect = endSessionPath
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// endSession sends a browser that has just signed out to Authentik's
// end-session endpoint (OIDC RP-initiated logout), whose invalidation flow
// ends the browser's Authentik session and returns it to the app's
// sign-in page, the post_logout_redirect_uri registered on the provider.
// That return needs the id_token_hint; a session from before ID tokens were
// kept has none, so it's sent without either, which still ends Authentik's
// session and stops on Authentik's sign-in page. With no sign-out to finish
// (no handle, or one used or expired) it goes to the sign-in page.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var (
		idToken string
		ok      bool
	)
	if c, err := r.Cookie(logoutCookie); err == nil {
		idToken, ok = s.signOuts.take(c.Value, s.Now())
		s.setCookie(w, logoutCookie, "", endSessionPath, -1)
	}
	if !ok || s.OIDC == nil {
		http.Redirect(w, r, signedOutPage, http.StatusSeeOther)
		return
	}
	end, err := s.OIDC.endSessionURL()
	if err != nil {
		s.Log.Warn("sign-out: Authentik couldn't be reached, so its session wasn't ended", "error", err.Error())
		http.Redirect(w, r, signedOutUnreachable, http.StatusSeeOther)
		return
	}
	if idToken != "" && s.AppURL != nil {
		q := end.Query()
		q.Set("id_token_hint", idToken)
		q.Set("post_logout_redirect_uri", s.signedOutURL())
		end.RawQuery = q.Encode()
	}
	http.Redirect(w, r, end.String(), http.StatusSeeOther)
}

// signedOutURL is the sign-in page's full address, as the blueprint
// registers it on the provider for its return after signing out: Authentik
// compares the two exactly.
func (s *Server) signedOutURL() string {
	return s.AppURL.JoinPath("/signin").String() + "?signed_out=1"
}

// signOutHold is how long a sign-out's ID token is held for its hop to
// Authentik: the browser makes it at once.
const signOutHold = 2 * time.Minute

// signOuts holds the ID tokens of browsers that have just signed out, each
// under a one-time handle, until endSession takes it. In memory: a restart
// in between sends that browser to Authentik without the token.
type signOuts struct {
	mu sync.Mutex
	m  map[string]heldToken
}

type heldToken struct {
	idToken string
	until   time.Time
}

func (h *signOuts) put(handle, idToken string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string]heldToken{}
	}
	for k, v := range h.m {
		if !now.Before(v.until) {
			delete(h.m, k)
		}
	}
	h.m[handle] = heldToken{idToken: idToken, until: now.Add(signOutHold)}
}

// take returns the token held under handle, once, and ok false when there
// is none or it expired.
func (h *signOuts) take(handle string, now time.Time) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.m[handle]
	delete(h.m, handle)
	if !ok || !now.Before(v.until) {
		return "", false
	}
	return v.idToken, true
}

// AuthStatus tells the sign-in page what it can offer. It's outside
// /api/v1, which needs a session (A29).
type AuthStatus struct {
	FirstAdminSetup bool `json:"first_admin_setup"`
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	ok, err := s.Accounts.FirstAdminAvailable(r.Context())
	if err != nil {
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.Error("auth status", "error", err.Error())
	}
	writeJSON(w, http.StatusOK, AuthStatus{FirstAdminSetup: ok})
}

// firstAdmin starts the first admin's enrolment (R44). It changes state, so it's a
// same-site POST (from the sign-in page's button, with fetch) that answers
// with where to go: a form post couldn't follow a redirect to Authentik's
// host under the page's Content Security Policy (form-action 'self').
func (s *Server) firstAdmin(w http.ResponseWriter, r *http.Request) {
	link, err := s.Accounts.FirstAdminEnrolment(r.Context())
	var refusal *accounts.Error
	switch {
	case errors.As(err, &refusal):
		link = "/signin?error=" + url.QueryEscape(refusal.Code)
	case err != nil:
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.Error("first admin", "error", err.Error())
		link = "/signin?error=error"
	}
	writeJSON(w, http.StatusOK, Next{Redirect: link})
}

// PageLook is the look (R82d) the app's page is sent with: the signed-in
// user's, so its first paint is already theirs (ADR 0028), or "" (Harbour)
// before sign-in, on the sign-in page, and whenever the session can't be
// read. It never refuses anything: the page itself loads without a session,
// and the app then sends the visitor to sign in.
//
// Beside RadioArchive (LookCookieDomain set, R92b), it's ArchiveUx's rule
// instead (look.js, its ADR 0002): the shared look cookie's look, else the
// account's, else Harbour, on every page, the sign-in page too; and shared
// says so, so the app's script follows the cookie as well.
func (s *Server) PageLook(r *http.Request) (look string, shared bool) {
	own := s.accountLook(r)
	if s.LookCookieDomain == "" {
		return own, false
	}
	return pickLook(lookFromCookies(r), own), true
}

// accountLook is the signed-in user's look, or "" before sign-in, on the
// sign-in page, and whenever the session can't be read.
func (s *Server) accountLook(r *http.Request) string {
	if s.Accounts == nil || strings.HasPrefix(r.URL.Path, "/signin") {
		return ""
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	u, err := s.Accounts.Session(r.Context(), c.Value)
	if err != nil {
		return ""
	}
	return u.Look
}
