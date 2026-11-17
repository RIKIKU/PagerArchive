package stacktest

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

// stopAnywhere answers no challenge: a walk stops at the first one.
func stopAnywhere(challenge) map[string]any { return nil }

// signOut signs out as the browser app does (R40b): POST /auth/logout, then
// its redirect, through Authentik's end-session endpoint and its
// invalidation flow. It returns where the browser ended up.
func (b *browser) signOut() *url.URL {
	b.t.Helper()
	end, c := b.walk(b.postForRedirect("/auth/logout"), stopAnywhere)
	if end == nil {
		b.t.Fatalf("signing out stopped in Authentik's flow at %q: %s %s", c.Component, c.errorText(), c.reply())
	}
	return end
}

// checkSignOut is A117 without a browser: signing out ends on the app's
// sign-in page with both sessions ended, so the way back in through
// Authentik asks for the sign-in again. wantEnd is where it should end.
func (b *browser) checkSignOut(what, wantEnd string) {
	b.t.Helper()
	end := b.signOut()
	if got := end.Path + "?" + end.RawQuery; end.Host != b.s.app.Host || got != wantEnd {
		b.t.Errorf("%s: signing out ended at %s, want the app's %s", what, end, wantEnd)
	}
	if code, _ := b.api(http.MethodGet, "/api/v1/me", nil, nil); code != http.StatusUnauthorized {
		b.t.Errorf("%s: the app's session still works after signing out: %d", what, code)
	}
	back, c := b.walk(b.s.app.JoinPath("/auth/login").String(), stopAnywhere)
	if back != nil {
		b.t.Fatalf("%s: back into the app after signing out, Authentik signed straight back in (at %s): its session wasn't ended", what, back)
	}
	if c.Component != "ak-stage-identification" {
		b.t.Fatalf("%s: back into the app after signing out, Authentik showed %q, want its identification step", what, c.Component)
	}
	b.t.Logf("A117: %s: signing out ended at %s, the app's session gone; back in, Authentik asked again (%s)", what, end.Path+"?"+end.RawQuery, c.Component)
	if ok, c := b.signIn(); !ok {
		b.t.Fatalf("%s: can't sign in again after signing out: %s %s", what, c.errorText(), c.raw)
	}
}

// A117 (R40b, ADR 0038): signing out ends Authentik's session too and
// comes back to the app's sign-in page; and a session from before ID tokens
// were kept with sessions still ends both.
func TestA117SignOut(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	o.checkSignOut("signed in", "/signin?signed_out=1")
	o.checkSignOut("signed in again", "/signin?signed_out=1")

	// As if signed in before the upgrade that started keeping ID tokens:
	// this browser's session (only a hash of its token is stored) loses it.
	var hash string
	for _, c := range o.client.Jar.Cookies(s.app) {
		if c.Name == "pa_session" {
			sum := sha256.Sum256([]byte(c.Value))
			hash = hex.EncodeToString(sum[:])
		}
	}
	if hash == "" {
		t.Fatal("no session cookie")
	}
	cmd := exec.Command("docker", "compose", "-f", "compose.yaml", "-f", "deploy/test/compose.yaml",
		"exec", "-T", "db", "psql", "-qtA", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-c",
		"UPDATE sessions SET id_token = NULL WHERE id_token IS NOT NULL AND token_hash = decode('"+hash+"', 'hex') RETURNING 1")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("clearing the session's ID token: %v: %s", err, out)
	}
	end, c := o.walk(o.postForRedirect("/auth/logout"), stopAnywhere)
	if code, _ := o.api(http.MethodGet, "/api/v1/me", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("a session with no ID token still works after signing out: %d", code)
	}
	// Without the hint Authentik doesn't return to the app: it ends its
	// session and stops at its own sign-in.
	if end != nil || c.Component != "ak-stage-identification" {
		t.Fatalf("a session with no ID token: signing out ended at %v, %q, want Authentik's sign-in", end, c.Component)
	}
	back, c := o.walk(o.s.app.JoinPath("/auth/login").String(), stopAnywhere)
	if back != nil || c.Component != "ak-stage-identification" {
		t.Fatalf("a session with no ID token: back into the app, Authentik signed straight back in (%v %q)", back, c.Component)
	}
	t.Log("A117: a session with no ID token: signing out ended both sessions, stopping at Authentik's sign-in; back in, Authentik asked again")
}
