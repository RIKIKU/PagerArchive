package server

// Sign-in beside RadioArchive (R92b-R92e, ADR 0052): the shared look cookie,
// the silent sign-in check, and OIDC back-channel logout.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// ------------------------------------------------------------ the look cookie

// LookCookie is the name of the look cookie both apps share on their parent
// domain (R92b; ArchiveUx's look.js and its ADR 0002).
const LookCookie = "look"

// lookCookieAge is how long the look cookie lasts: a year, as look.js's.
const lookCookieAge = 365 * 24 * time.Hour

// lookFromCookies is the look in a request's Cookie headers, or "" when the
// cookie is absent, empty or not a known look. It's ArchiveUx's
// lookFromCookies (look.js): the first cookie of that name counts, its value
// is the look's name as it is (double quotes around it are dropped), and
// anything but a known look is none.
func lookFromCookies(r *http.Request) string {
	for _, line := range r.Header.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			name, v, ok := strings.Cut(part, "=")
			if !ok || strings.TrimSpace(name) != LookCookie {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
				v = v[1 : len(v)-1]
			}
			if slices.Contains(Looks, v) {
				return v
			}
			return ""
		}
	}
	return ""
}

// pickLook is ArchiveUx's pickLook (look.js): the cookie's look, else the
// app's own setting, else Harbour.
func pickLook(cookie, own string) string {
	switch {
	case cookie != "":
		return cookie
	case slices.Contains(Looks, own):
		return own
	}
	return Looks[0]
}

// setLookCookie writes the shared look cookie (R92b): the look's name only,
// on the parent domain, for every path, for a year, SameSite=Lax, and Secure
// unless the app is on plain HTTP (a test). Not HttpOnly, as look.js has it,
// so either app's page could write it too. Nothing without LookCookieDomain.
func (s *Server) setLookCookie(w http.ResponseWriter, look string) {
	if s.LookCookieDomain == "" || !slices.Contains(Looks, look) {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: LookCookie, Value: look, Domain: s.LookCookieDomain, Path: "/",
		MaxAge: int(lookCookieAge.Seconds()), SameSite: http.SameSiteLaxMode, Secure: s.secureCookies(),
	})
}

// ------------------------------------------------------- silent sign-in check

// silentCookie marks a browser session in which the silent sign-in check
// (R92c) was made, so it's made once: a cookie with no expiry, which the
// browser forgets when the session ends.
const silentCookie = "pa_silent"

// silentCheck reports whether sign-in starts with the silent check: beside
// RadioArchive only (R92c).
func (s *Server) silentCheck() bool { return s.RadioArchiveURL != "" }

// normalSignIn is the sign-in as it is without the check, for after a silent
// check that found no Authentik session or failed: checked=1 says not to
// make the check again, whatever the browser's cookies.
func normalSignIn(ret string) string { return "/auth/login?checked=1&return=" + url.QueryEscape(ret) }

// ---------------------------------------------------- back-channel logout

// logoutEvent is the events claim's member that makes a JWT a logout token
// (OpenID Connect Back-Channel Logout 1.0, section 2.4).
const logoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// LogoutToken is what a valid logout token names: the user (sub) and the
// Authentik session (sid), at least one of them.
type LogoutToken struct {
	Subject   string
	SessionID string
}

// VerifyLogoutToken checks a logout token as OpenID Connect Back-Channel
// Logout 1.0 (section 2.6) says: signed by the provider's key with an
// algorithm it uses, issued by it (iss) for this client (aud), not expired,
// with an issue time (iat), an events claim holding the back-channel logout
// event as a JSON object, a sub or a sid or both, and no nonce (which keeps
// an ID token from passing as one). verifier is the client's ID token
// verifier, which checks the signature, iss, aud and exp.
func VerifyLogoutToken(ctx context.Context, verifier *oidc.IDTokenVerifier, raw string) (LogoutToken, error) {
	tok, err := verifier.Verify(ctx, raw)
	if err != nil {
		return LogoutToken{}, fmt.Errorf("not a valid token: %w", err)
	}
	var claims map[string]json.RawMessage
	if err := tok.Claims(&claims); err != nil {
		return LogoutToken{}, errors.New("unreadable claims")
	}
	if _, ok := claims["nonce"]; ok {
		return LogoutToken{}, errors.New("it has a nonce: an ID token, not a logout token")
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(claims["events"], &events); err != nil || events == nil {
		return LogoutToken{}, errors.New("no events claim")
	}
	ev, ok := events[logoutEvent]
	if !ok || !bytes.HasPrefix(bytes.TrimSpace(ev), []byte("{")) {
		return LogoutToken{}, errors.New("its events claim has no back-channel logout event")
	}
	if tok.IssuedAt.IsZero() {
		return LogoutToken{}, errors.New("no iat")
	}
	var sid string
	if v, ok := claims["sid"]; ok {
		if json.Unmarshal(v, &sid) != nil {
			return LogoutToken{}, errors.New("sid isn't a string")
		}
	}
	if tok.Subject == "" && sid == "" {
		return LogoutToken{}, errors.New("neither sub nor sid")
	}
	return LogoutToken{Subject: tok.Subject, SessionID: sid}, nil
}

// logoutVerifier is the browser client's verifier, for logout tokens.
func (o *OIDC) logoutVerifier() (*oidc.IDTokenVerifier, error) {
	_, v, err := o.setup()
	return v, err
}

// backchannelLogout is the browser client's back-channel logout endpoint
// (R92d): Authentik posts a logout token here when one of its sessions ends
// (a sign-out in any app, a session revoked, a user disabled), and this
// app's browser sessions from that Authentik session end, with their live
// connections. A token without a sid ends all of that user's browser
// sessions. Answers as the specification says: 200, or 400 with an OAuth
// error, never cached.
func (s *Server) backchannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	refuse := func(why string) {
		s.Log.Warn("back-channel logout refused", "reason", why)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": why})
	}
	if s.OIDC == nil || s.Accounts == nil {
		refuse("sign-in isn't set up")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		refuse("not a form")
		return
	}
	raw := r.PostForm.Get("logout_token")
	if raw == "" {
		refuse("no logout_token")
		return
	}
	verifier, err := s.OIDC.logoutVerifier()
	if err != nil {
		s.Log.Error("back-channel logout: oidc discovery", "error", err.Error())
		problem(w, http.StatusServiceUnavailable, "Authentik couldn't be reached to check the token")
		return
	}
	tok, err := VerifyLogoutToken(s.OIDC.ctx(r.Context()), verifier, raw)
	if err != nil {
		refuse(err.Error())
		return
	}
	if tok.Subject == "" {
		// A sid alone: Authentik always sends sub too. Nothing is ended for
		// a session this app can't tie to a user.
		w.WriteHeader(http.StatusOK)
		return
	}
	n, err := s.Accounts.EndDirectorySession(r.Context(), tok.Subject, tok.SessionID)
	if err != nil {
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.Error("back-channel logout", "error", err.Error())
		problem(w, http.StatusInternalServerError, "couldn't end the sessions")
		return
	}
	s.Log.Info("back-channel logout: Authentik's session ended", "sessions_ended", n, "by_session", tok.SessionID != "")
	w.WriteHeader(http.StatusOK)
}
