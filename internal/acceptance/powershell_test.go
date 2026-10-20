package acceptance

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// A53 at API level: the PowerShell module's access tokens (R60-R62, ADR
// 0009 §3), from a local issuer standing in for Authentik. The stack tests
// repeat the main cases with real Authentik tokens.

const psClient = "pagerarchive-powershell"

type testIssuer struct {
	url string
	key *rsa.PrivateKey
}

func newIssuer(t *testing.T, url string) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &testIssuer{url: url, key: key}
}

// sign makes an RS256 JWT.
func (i *testIssuer) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(head))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return head + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// accessToken is what Authentik issues the module (ADR 0009 §2, E2).
func (i *testIssuer) accessToken(t *testing.T, subject string, change func(map[string]any)) string {
	t.Helper()
	now := time.Now()
	c := map[string]any{
		"iss": i.url, "aud": psClient, "azp": psClient, "sub": subject,
		"scope": "offline_access profile openid email", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"jti": base64.RawURLEncoding.EncodeToString(sha256.New().Sum([]byte(subject + now.String()))[:12]),
	}
	if change != nil {
		change(c)
	}
	return i.sign(t, c)
}

// usePowerShell makes the server trust iss as the module's issuer.
func (e *env) usePowerShell(iss *testIssuer) {
	e.srv.PowerShell = &server.PowerShell{
		Issuer: iss.url, ClientID: psClient,
		Verifier: oidc.NewVerifier(iss.url, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&iss.key.PublicKey}}, &oidc.Config{ClientID: psClient}),
	}
}

// bearer makes a request with a token and returns the status and problem.
func (e *env) bearer(method, path, token string, cookie *http.Cookie) (int, server.Problem, http.Header) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var p server.Problem
	if resp.StatusCode >= 400 {
		_ = json.NewDecoder(resp.Body).Decode(&p)
	}
	return resp.StatusCode, p, resp.Header
}

// A53. The API checks a module token's signature, issuer, audience and
// expiry, that it's an access token, and the person's status and role on
// every request.
func TestA53PowerShellTokens(t *testing.T) {
	e := setup(t)
	iss := newIssuer(t, "https://auth.example.test/application/o/pagerarchive-powershell/")
	e.usePowerShell(iss)
	adminSub := e.admin.claims.Subject

	var tokens []string
	keep := func(s string) string { tokens = append(tokens, s); return s }

	// The admin's token works, as the admin.
	tok := keep(iss.accessToken(t, adminSub, nil))
	if code, p, _ := e.bearer(http.MethodGet, "/api/v1/me", tok, nil); code != http.StatusOK {
		t.Fatalf("admin's token: %d %+v", code, p)
	}
	if code, _, _ := e.bearer(http.MethodGet, "/api/v1/admin/templates", tok, nil); code != http.StatusOK {
		t.Errorf("admin's token, admin route: %d", code)
	}

	// Refused tokens, each with 401 invalid_token.
	other := newIssuer(t, iss.url)
	elsewhere := newIssuer(t, "https://auth.example.test/application/o/pagerarchive/")
	for name, bad := range map[string]string{
		"expired":            iss.accessToken(t, adminSub, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }),
		"another key":        other.accessToken(t, adminSub, nil),
		"another audience":   iss.accessToken(t, adminSub, func(c map[string]any) { c["aud"], c["azp"] = "pagerarchive", "pagerarchive" }),
		"another issuer":     elsewhere.accessToken(t, adminSub, func(c map[string]any) { c["iss"] = elsewhere.url }),
		"an ID token":        iss.accessToken(t, adminSub, func(c map[string]any) { delete(c, "azp"); delete(c, "scope") }),
		"another client":     iss.accessToken(t, adminSub, func(c map[string]any) { c["azp"] = "pagerarchive" }),
		"no signature":       strings.TrimRight(iss.accessToken(t, adminSub, nil), "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"),
		"not a token at all": "not-a-token",
	} {
		keep(bad)
		code, p, h := e.bearer(http.MethodGet, "/api/v1/me", bad, nil)
		if code != http.StatusUnauthorized || p.Code != server.CodeInvalidToken || !strings.HasPrefix(h.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("%s: %d %+v", name, code, p)
		}
		t.Logf("A53: %s: %d %s", name, code, p.Code)
	}

	// A user who isn't an admin: refused, saying PowerShell is for admins (R62).
	code, p, _ := e.bearer(http.MethodGet, "/api/v1/messages", keep(iss.accessToken(t, e.user.claims.Subject, nil)), nil)
	if code != http.StatusForbidden || p.Code != server.CodeAdminsOnly || p.Error != "PowerShell access is for admins." {
		t.Errorf("user's token: %d %+v", code, p)
	}
	t.Logf("A53: user B: %d %q", code, p.Error)

	// Admin C: demoted, refused on the next request; admin again, then
	// deactivated, refused; account deleted, refused.
	c := e.newMember("c@example.com", "admin", e.admin)
	cTok := keep(iss.accessToken(t, c.claims.Subject, nil))
	if code, _, _ := e.bearer(http.MethodGet, "/api/v1/me", cTok, nil); code != http.StatusOK {
		t.Fatalf("C as admin: %d", code)
	}
	setRole := func(role string) {
		if code, p := e.admin.call(http.MethodPut, "/api/v1/admin/users/"+itoa(c.id)+"/role", server.RoleRequest{Role: role}, nil); code >= 300 {
			t.Fatalf("set C's role to %s: %d %+v", role, code, p)
		}
	}
	setRole("user")
	if code, p, _ := e.bearer(http.MethodGet, "/api/v1/me", cTok, nil); code != http.StatusForbidden || p.Code != server.CodeAdminsOnly {
		t.Errorf("C demoted: %d %+v", code, p)
	}
	setRole("admin")
	if code, _, _ := e.bearer(http.MethodGet, "/api/v1/me", cTok, nil); code != http.StatusOK {
		t.Errorf("C admin again: %d", code)
	}
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/users/"+itoa(c.id)+"/deactivate", server.DeactivateRequest{}, nil); code >= 300 {
		t.Fatalf("deactivate C: %d %+v", code, p)
	}
	if code, p, _ := e.bearer(http.MethodGet, "/api/v1/me", cTok, nil); code != http.StatusUnauthorized || p.Code != server.CodeDeactivated {
		t.Errorf("C deactivated: %d %+v", code, p)
	}
	// Deleting an account clears its subject (DeleteUserAccount).
	d := e.newMember("d@example.com", "admin", e.admin)
	dTok := keep(iss.accessToken(t, d.claims.Subject, nil))
	if _, err := e.st.Pool.Exec(e.ctx, "UPDATE users SET subject = NULL, status = 'deleted' WHERE id = $1", d.id); err != nil {
		t.Fatal(err)
	}
	if code, p, _ := e.bearer(http.MethodGet, "/api/v1/me", dTok, nil); code != http.StatusUnauthorized || p.Code != server.CodeNotSignedIn {
		t.Errorf("deleted account: %d %+v", code, p)
	}

	// A token and a session cookie at once: refused.
	if code, p, _ := e.bearer(http.MethodGet, "/api/v1/me", tok, e.admin.cookie); code != http.StatusBadRequest || p.Code != server.CodeTwoCredentials {
		t.Errorf("token and cookie: %d %+v", code, p)
	}
	// A module token can't post pages: that needs a client key (R59).
	if code, _, _ := e.bearer(http.MethodPost, "/api/v1/pages", tok, nil); code != http.StatusUnauthorized {
		t.Errorf("token posts a page: %d", code)
	}

	// Without the module's issuer set up, tokens are refused.
	ps := e.srv.PowerShell
	e.srv.PowerShell = nil
	if code, _, _ := e.bearer(http.MethodGet, "/api/v1/me", tok, nil); code != http.StatusUnauthorized {
		t.Errorf("PowerShell off: %d", code)
	}
	e.srv.PowerShell = ps

	// Tokens are never logged.
	logs := e.logs.String()
	for _, s := range tokens {
		if len(s) > 20 && strings.Contains(logs, s[len(s)-20:]) {
			t.Errorf("a token's signature is in the log")
		}
	}
	if !strings.Contains(logs, `"msg":"signed in with PowerShell"`) {
		t.Error("PowerShell sign-in not logged")
	}
}

// GET /auth/powershell tells the module where to sign in (R60).
func TestPowerShellSettings(t *testing.T) {
	e := setup(t)
	e.usePowerShell(newIssuer(t, "https://auth.example.test/application/o/pagerarchive-powershell/"))
	resp, err := http.Get(e.url + "/auth/powershell")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got server.PowerShellSettings
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %v", resp.StatusCode, err)
	}
	if got.Issuer != "https://auth.example.test/application/o/pagerarchive-powershell/" || got.ClientID != psClient || got.Scope != server.PowerShellScope {
		t.Errorf("settings: %+v", got)
	}
}
