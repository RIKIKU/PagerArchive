package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

const (
	sessionCookie = "pa_session"
	loginCookie   = "pa_login"
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

type userKey struct{}

// currentUser is the signed-in user of a request that passed signedIn.
func currentUser(r *http.Request) db.User {
	u, _ := r.Context().Value(userKey{}).(db.User)
	return u
}

// signedIn admits only requests with a live session of an active user
// (A29). Deactivation deletes sessions, so it takes effect at once (R43b).
func (s *Server) signedIn(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		var u db.User
		if err == nil {
			u, err = s.Accounts.Session(r.Context(), c.Value)
		}
		if errors.Is(err, accounts.ErrNoSession) || errors.Is(err, http.ErrNoCookie) {
			problem(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if err != nil {
			s.Log.Error("session", "error", err.Error())
			problem(w, http.StatusInternalServerError, "couldn't check the session")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	}
}

func (s *Server) secureCookies() bool { return s.AppURL != nil && s.AppURL.Scheme == "https" }

func (s *Server) setCookie(w http.ResponseWriter, name, value, path string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: int(maxAge.Seconds()),
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

// localPath keeps a post-sign-in redirect on this site.
func localPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.HasPrefix(p, "/auth/") {
		return "/"
	}
	return p
}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Return   string `json:"r"`
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
	b, _ := json.Marshal(st)
	s.setCookie(w, loginCookie, base64.RawURLEncoding.EncodeToString(b), "/auth/", 10*time.Minute)
	http.Redirect(w, r, cfg.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
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
		signInFailed(w, r, "expired")
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := verifier.Verify(ctx, raw)
	if err != nil || idt.Nonce != st.Nonce {
		s.Log.Warn("oidc id token rejected", "error", errString(err))
		signInFailed(w, r, "expired")
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
		s.Log.Error("sign-in", "error", err.Error())
		signInFailed(w, r, "error")
		return
	}
	token, err := s.Accounts.NewSession(r.Context(), u.ID)
	if err != nil {
		s.Log.Error("sign-in", "error", err.Error())
		signInFailed(w, r, "error")
		return
	}
	// The server's 30-day expiry, renewed by use, decides; the cookie just
	// lives as long as browsers allow.
	s.setCookie(w, sessionCookie, token, "/", 400*24*time.Hour)
	s.Log.Info("signed in", "user", u.ID)
	http.Redirect(w, r, st.Return, http.StatusSeeOther)
}

func errString(err error) string {
	if err == nil {
		return "nonce mismatch"
	}
	return err.Error()
}

// SignOut is the reply to a sign-out: where to send the browser next, to
// end the Authentik session too.
type SignOut struct {
	Redirect string `json:"redirect"`
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Accounts.EndSession(r.Context(), c.Value); err != nil {
			s.Log.Error("sign-out", "error", err.Error())
		}
	}
	s.setCookie(w, sessionCookie, "", "/", -1)
	out := SignOut{Redirect: "/signin?signed_out=1"}
	if _, _, err := s.OIDC.setup(); err == nil && s.OIDC.endSession != "" {
		out.Redirect = s.OIDC.endSession
	}
	writeJSON(w, http.StatusOK, out)
}

// AuthStatus tells the sign-in page what it can offer. It's outside
// /api/v1, which needs a session (A29).
type AuthStatus struct {
	FirstAdminSetup bool `json:"first_admin_setup"`
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	ok, err := s.Accounts.FirstAdminAvailable(r.Context())
	if err != nil {
		s.Log.Error("auth status", "error", err.Error())
	}
	writeJSON(w, http.StatusOK, AuthStatus{FirstAdminSetup: ok})
}

// firstAdmin starts the first admin's enrolment (R44).
func (s *Server) firstAdmin(w http.ResponseWriter, r *http.Request) {
	link, err := s.Accounts.FirstAdminEnrolment(r.Context())
	var refusal *accounts.Error
	if errors.As(err, &refusal) {
		signInFailed(w, r, refusal.Code)
		return
	}
	if err != nil {
		s.Log.Error("first admin", "error", err.Error())
		signInFailed(w, r, "error")
		return
	}
	http.Redirect(w, r, link, http.StatusSeeOther)
}
