package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// PowerShellScope is what the module asks Authentik for: offline_access
// gives it a refresh token (ADR 0009 §1).
const PowerShellScope = "openid email profile offline_access"

// ServiceAccountScope is what a service account asks for (R62a): no refresh
// token, since its credential gets a new access token whenever it needs one.
const ServiceAccountScope = "openid profile"

// PowerShell checks the PowerShell module's access tokens (R60-R62, ADR 0009
// §3). Like OIDC, it's set up on first use, so the API starts while
// Authentik is still starting.
type PowerShell struct {
	Issuer   string // AUTHENTIK_URL/application/o/pagerarchive-powershell/
	ClientID string // pagerarchive-powershell
	HTTP     *http.Client
	// Keys, when set, are the issuer's signing keys, in place of discovery
	// (tests). The checks are the same either way.
	Keys oidc.KeySet

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	seen     map[string]time.Time // token IDs already logged, until they expire
}

func (p *PowerShell) tokenVerifier() (*oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.verifier != nil {
		return p.verifier, nil
	}
	// go-oidc's checks: the signature (RS256, keys from the issuer's JWKS),
	// the issuer, expiry, and the audience being this client.
	config := &oidc.Config{ClientID: p.ClientID}
	if p.Keys != nil {
		p.verifier = oidc.NewVerifier(p.Issuer, p.Keys, config)
		return p.verifier, nil
	}
	// The provider keeps this context to fetch signing keys later (and again
	// when a token names a key it hasn't seen), so it mustn't be a request's.
	pr, err := oidc.NewProvider(oidc.ClientContext(context.Background(), p.HTTP), p.Issuer)
	if err != nil {
		return nil, err
	}
	p.verifier = pr.Verifier(config)
	return p.verifier, nil
}

// firstUse reports whether a token ID hasn't been seen before, so sign-ins
// are logged once per token, not per request.
func (p *PowerShell) firstUse(id string, exp time.Time, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[string]time.Time{}
	}
	if _, ok := p.seen[id]; ok {
		return false
	}
	if len(p.seen) >= 1000 {
		for k, e := range p.seen {
			if e.Before(now) {
				delete(p.seen, k)
			}
		}
	}
	p.seen[id] = exp
	return true
}

// Codes for refusing a module token. The module shows the message and uses
// the code as the error's ID (guidelines §7).
const (
	CodeInvalidToken    = "invalid_token"
	CodeNotSignedIn     = "not_signed_in"
	CodeDeactivated     = "deactivated"
	CodeAdminsOnly      = "powershell_admins_only"
	CodeTwoCredentials  = "two_credentials"
	msgInvalidToken     = "Your PowerShell sign-in isn't valid or has expired. Run Connect-PagerArchive."
	msgPowerShellAdmins = "PowerShell access is for admins."
)

// errTokenRefused carries a refusal to the response.
type errTokenRefused struct {
	status      int
	code, msg   string
	description string // for the WWW-Authenticate header; never the token
}

func (e *errTokenRefused) Error() string { return e.code }

// bearerUser is the user a module or service-account access token belongs
// to, checked on every request: nothing is cached, so deactivation,
// deletion, demotion, a service account's revocation, rotation and expiry
// take effect on the next command (R60, R62a, A53). It also returns the
// token's expiry, when a live stream on it must end (ADR 0010 §4).
func (s *Server) bearerUser(ctx context.Context, raw string) (db.User, time.Time, error) {
	client, service := s.tokenClient(raw)
	if client == nil {
		return db.User{}, time.Time{}, &errTokenRefused{http.StatusUnauthorized, CodeInvalidToken, msgInvalidToken, "PowerShell sign-in is off"}
	}
	v, err := client.tokenVerifier()
	if err != nil {
		s.Log.Error("powershell token: discovery", "error", err.Error())
		return db.User{}, time.Time{}, errors.New("directory unavailable")
	}
	tok, err := v.Verify(ctx, raw)
	if err != nil {
		// go-oidc's errors name the check that failed, never the token.
		s.Log.Info("powershell token refused", "reason", err.Error())
		return db.User{}, time.Time{}, &errTokenRefused{http.StatusUnauthorized, CodeInvalidToken, msgInvalidToken, "invalid"}
	}
	// An access token, not the ID token handed out with it: only access
	// tokens carry azp and scope (ADR 0009 §2, E2).
	var c struct {
		Azp   string `json:"azp"`
		Scope string `json:"scope"`
		JTI   string `json:"jti"`
	}
	if err := tok.Claims(&c); err != nil || c.Azp != client.ClientID || c.Scope == "" {
		s.Log.Info("powershell token refused", "reason", "not an access token for this client")
		return db.User{}, time.Time{}, &errTokenRefused{http.StatusUnauthorized, CodeInvalidToken, msgInvalidToken, "not an access token"}
	}
	u, err := s.Store.UserBySubject(ctx, &tok.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		// Also a service account's token from before it was rotated or
		// revoked: its old Authentik user is gone (R62a).
		s.Log.Info("powershell token refused", "reason", CodeNotSignedIn)
		return db.User{}, time.Time{}, &errTokenRefused{http.StatusUnauthorized, CodeNotSignedIn, "No PagerArchive account for this sign-in.", "no account"}
	}
	if err != nil {
		if telemetry.Abandoned(ctx, err) {
			return db.User{}, time.Time{}, err // the caller says so (R73a)
		}
		s.Log.Error("powershell token: user", "error", store.SafeError(err))
		return db.User{}, time.Time{}, err
	}
	// The service accounts' client is for service accounts only, and the
	// module's for people only.
	if service != accounts.IsServiceAccount(u) {
		s.Log.Info("powershell token refused", "reason", "account of the wrong kind for this client", "user", u.ID)
		return db.User{}, time.Time{}, &errTokenRefused{http.StatusUnauthorized, CodeInvalidToken, msgInvalidToken, "wrong client"}
	}
	if u.Status != "active" {
		s.Log.Info("powershell token refused", "reason", CodeDeactivated, "user", u.ID)
		msg := "Your account has been deactivated. Ask an admin if you think this is a mistake."
		if service {
			msg = "This service account has been deactivated by an admin."
		}
		return db.User{}, time.Time{}, &errTokenRefused{http.StatusUnauthorized, CodeDeactivated, msg, "deactivated"}
	}
	if service {
		// Authentik doesn't check an app password's expiry when it's traded
		// for a token, so the app does, on every request (ADR 0048).
		if u.CredentialExpiresAt == nil || !s.Now().Before(*u.CredentialExpiresAt) {
			s.Log.Info("powershell token refused", "reason", CodeServiceExpired, "user", u.ID, "service_account", deref(u.Name))
			return db.User{}, time.Time{}, &errTokenRefused{http.StatusUnauthorized, CodeServiceExpired, msgServiceExpired, "expired"}
		}
	} else if u.Role != accounts.RoleAdmin {
		s.Log.Info("powershell token refused", "reason", CodeAdminsOnly, "user", u.ID)
		return db.User{}, time.Time{}, &errTokenRefused{http.StatusForbidden, CodeAdminsOnly, msgPowerShellAdmins, ""}
	}
	id := c.JTI
	if id == "" {
		id = tok.Subject + " " + tok.IssuedAt.String()
	}
	if client.firstUse(id, tok.Expiry, s.Now()) {
		if service {
			s.Log.Info("signed in as a service account", "user", u.ID, "service_account", deref(u.Name))
		} else {
			s.Log.Info("signed in with PowerShell", "user", u.ID)
		}
	}
	return u, tok.Expiry, nil
}

// tokenClient is the client whose tokens raw claims to be: the service
// accounts' when it names their issuer, and otherwise the module's. The
// issuer is read unchecked only to choose; the chosen client's verifier
// then checks the whole token, the issuer included.
func (s *Server) tokenClient(raw string) (*PowerShell, bool) {
	if s.ServiceAccounts != nil && unverifiedIssuer(raw) == s.ServiceAccounts.Issuer {
		return s.ServiceAccounts, true
	}
	return s.PowerShell, false
}

// unverifiedIssuer is a JWT's iss claim, unchecked, or "".
func unverifiedIssuer(raw string) string {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	return c.Iss
}

// bearerToken is the request's Authorization: Bearer token, if any.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	scheme, tok, _ := strings.Cut(h, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	return strings.TrimSpace(tok), true
}

func refuseToken(w http.ResponseWriter, e *errTokenRefused) {
	if e.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="`+e.description+`"`)
	}
	writeJSON(w, e.status, Problem{Error: e.msg, Code: e.code})
}

// PowerShellSettings tells the module what it needs from the app's URL
// alone (R60): where to sign in, as whom, and what to ask for.
type PowerShellSettings struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`
	Scope    string `json:"scope"`
	// Service accounts' client (R62a), when it's on: where a service account
	// trades its credential for tokens, and what it asks for.
	ServiceIssuer   string `json:"service_issuer,omitempty"`
	ServiceClientID string `json:"service_client_id,omitempty"`
	ServiceScope    string `json:"service_scope,omitempty"`
}

func (s *Server) powershellSettings(w http.ResponseWriter, r *http.Request) {
	if s.PowerShell == nil {
		problem(w, http.StatusNotFound, "PowerShell sign-in is off")
		return
	}
	out := PowerShellSettings{Issuer: s.PowerShell.Issuer, ClientID: s.PowerShell.ClientID, Scope: PowerShellScope}
	if s.ServiceAccounts != nil {
		out.ServiceIssuer, out.ServiceClientID, out.ServiceScope = s.ServiceAccounts.Issuer, s.ServiceAccounts.ClientID, ServiceAccountScope
	}
	writeJSON(w, http.StatusOK, out)
}
