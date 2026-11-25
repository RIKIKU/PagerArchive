package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// R92b: the look cookie is read as ArchiveUx's look.js reads it, and the
// first paint's look picked by its rule (look.test.mjs's cases).
func TestR92bLookCookieRule(t *testing.T) {
	read := func(header string) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Cookie", header)
		}
		return lookFromCookies(r)
	}
	for header, want := range map[string]string{
		"session=abc; look=console; other=1": "console",
		"look=harbour":                       "harbour",
		`look="console"`:                     "console",
		"  look = console ":                  "console",
		"mylook=console":                     "", // another cookie whose name ends the same
		"look=neon":                          "",
		"look=":                              "",
		"look=Console":                       "",
		"look=neon; look=console":            "", // the first one counts
		"":                                   "",
	} {
		if got := read(header); got != want {
			t.Errorf("Cookie %q: %q, want %q", header, got, want)
		}
	}
	for _, c := range []struct{ cookie, own, want string }{
		{"console", "harbour", "console"}, // the cookie wins
		{"", "console", "console"},        // absent or unknown: the account's
		{"", "", "harbour"},               // nothing: Harbour
		{"", "neon", "harbour"},
	} {
		if got := pickLook(c.cookie, c.own); got != c.want {
			t.Errorf("pickLook(%q, %q) = %q, want %q", c.cookie, c.own, got, c.want)
		}
	}
}

// R92b: the cookie as written: the look's name on the parent domain, every
// path, a year, Lax and Secure (on HTTPS); nothing standalone or for a look
// that isn't one.
func TestR92bLookCookieAttributes(t *testing.T) {
	app, _ := url.Parse("https://pager.example.org")
	s := &Server{AppURL: app, LookCookieDomain: "example.org"}
	w := httptest.NewRecorder()
	s.setLookCookie(w, "console")
	if got, want := w.Header().Get("Set-Cookie"), "look=console; Path=/; Domain=example.org; Max-Age=31536000; Secure; SameSite=Lax"; got != want {
		t.Errorf("Set-Cookie %q, want %q", got, want)
	}
	for _, c := range []struct {
		domain, look string
	}{{"", "console"}, {"example.org", "neon"}, {"example.org", ""}} {
		s.LookCookieDomain = c.domain
		w := httptest.NewRecorder()
		s.setLookCookie(w, c.look)
		if got := w.Header().Get("Set-Cookie"); got != "" {
			t.Errorf("domain %q, look %q: Set-Cookie %q, want none", c.domain, c.look, got)
		}
	}
}

// signer makes JWTs as Authentik does: RS256, with a key ID.
type signer struct {
	key *rsa.PrivateKey
	kid string
}

func newSigner(t *testing.T) signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return signer{key: k, kid: "made-up-key"}
}

func (s signer) sign(t *testing.T, typ string, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": "RS256", "typ": typ, "kid": s.kid})
	body := head + "." + enc(claims)
	sum := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

const (
	testIssuer = "https://auth.example.org/application/o/pagerarchive/"
	testClient = "pagerarchive"
)

// logoutClaims are a logout token's claims as Authentik sends them.
func logoutClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": testIssuer, "aud": testClient, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"jti": "made-up-jti", "sub": "42", "sid": "made-up-sid",
		"events": map[string]any{logoutEvent: map[string]any{}},
	}
}

// R92d (A151): a logout token is accepted only as OpenID Connect
// Back-Channel Logout 1.0 says: signed by the provider, for this client,
// with the logout event, a sub or sid, and no nonce.
func TestA151LogoutTokenValidation(t *testing.T) {
	ak, other := newSigner(t), newSigner(t)
	now := time.Now()
	verifier := oidc.NewVerifier(testIssuer, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&ak.key.PublicKey}}, &oidc.Config{ClientID: testClient})
	ctx := context.Background()

	good := ak.sign(t, "logout+jwt", logoutClaims(now))
	tok, err := VerifyLogoutToken(ctx, verifier, good)
	if err != nil || tok.Subject != "42" || tok.SessionID != "made-up-sid" {
		t.Fatalf("a good logout token: %+v, %v", tok, err)
	}
	// sub alone, or sid alone, is enough.
	for _, drop := range []string{"sid", "sub"} {
		c := logoutClaims(now)
		delete(c, drop)
		if _, err := VerifyLogoutToken(ctx, verifier, ak.sign(t, "logout+jwt", c)); err != nil {
			t.Errorf("without %s: %v", drop, err)
		}
	}

	refused := map[string]string{
		"bad signature":    other.sign(t, "logout+jwt", logoutClaims(now)),
		"tampered":         good[:strings.LastIndex(good, ".")] + "." + base64.RawURLEncoding.EncodeToString([]byte("not a signature")),
		"not a JWT":        "not-a-token",
		"no events":        ak.sign(t, "logout+jwt", with(logoutClaims(now), "events", nil)),
		"other event":      ak.sign(t, "logout+jwt", with(logoutClaims(now), "events", map[string]any{"http://schemas.openid.net/event/other": map[string]any{}})),
		"event not {}":     ak.sign(t, "logout+jwt", with(logoutClaims(now), "events", map[string]any{logoutEvent: "yes"})),
		"events not {}":    ak.sign(t, "logout+jwt", with(logoutClaims(now), "events", []string{logoutEvent})),
		"nonce present":    ak.sign(t, "logout+jwt", with(logoutClaims(now), "nonce", "made-up-nonce")),
		"wrong aud":        ak.sign(t, "logout+jwt", with(logoutClaims(now), "aud", "radioarchive")),
		"wrong iss":        ak.sign(t, "logout+jwt", with(logoutClaims(now), "iss", "https://auth.example.org/application/o/radioarchive/")),
		"expired":          ak.sign(t, "logout+jwt", with(logoutClaims(now), "exp", now.Add(-time.Minute).Unix())),
		"no iat":           ak.sign(t, "logout+jwt", with(logoutClaims(now), "iat", nil)),
		"no sub, no sid":   ak.sign(t, "logout+jwt", with(with(logoutClaims(now), "sub", nil), "sid", nil)),
		"sid not a string": ak.sign(t, "logout+jwt", with(logoutClaims(now), "sid", 7)),
	}
	for what, raw := range refused {
		if tok, err := VerifyLogoutToken(ctx, verifier, raw); err == nil {
			t.Errorf("%s: accepted (%+v)", what, tok)
		} else {
			t.Logf("A151: %s: refused: %v", what, err)
		}
	}
}

// with is claims with key set to v, or taken out when v is nil.
func with(claims map[string]any, key string, v any) map[string]any {
	if v == nil {
		delete(claims, key)
	} else {
		claims[key] = v
	}
	return claims
}
