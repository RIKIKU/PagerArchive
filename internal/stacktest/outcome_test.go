package stacktest

import (
	"slices"
	"strings"
	"testing"
)

// outcome is what Authentik's reply to one attempt at a password or code
// says. A check passes or fails only on a real answer: refused by the lock,
// refused as wrong, accepted, or (signing in) refused by the app after
// Authentik. Anything else is no answer: a test error that says what came
// back, never a pass, and never "not refused" (ADR 0014).
type outcome int

const (
	noAnswer    outcome = iota // an error status, a reply that isn't a flow challenge, no reply, or a step that doesn't answer the attempt
	wrongAnswer                // refused as wrong; the account isn't locked
	lockedOut                  // refused by the lock: the deny stage's too-many-attempts message
	accepted                   // the flow went on past the check: its next stage, or into the app signed in
	appRefused                 // Authentik let the person through and the app's sign-in page refused them
)

func (o outcome) String() string {
	return [...]string{"no answer", "refused as wrong", "refused by the lock", "accepted", "refused by the app"}[o]
}

// result is an attempt's outcome and what Authentik (or the app) said.
type result struct {
	outcome outcome
	said    string
}

// tooManyAttempts is the deny stage's message (the blueprint's
// pagerarchive-too-many-attempts).
const tooManyAttempts = "Too many wrong attempts for this account."

// wrongMessages are what the checking stages say to a wrong answer:
// Authentik's password, authenticator-validate and authenticator-email
// stages.
var wrongMessages = []string{"Invalid password", "Invalid Token", "Code does not match"}

// judge reads the reply to an attempt made at stage; next lists the
// components that mean the attempt was accepted.
func judge(c challenge, stage string, next ...string) result {
	if !c.isChallenge() {
		return result{noAnswer, "not a flow challenge: " + c.reply()}
	}
	text := c.errorText()
	said := strings.TrimSpace(c.Component + " " + text)
	wrong := slices.ContainsFunc(wrongMessages, func(m string) bool { return strings.Contains(text, m) })
	switch {
	case c.Component == "ak-stage-access-denied" && strings.Contains(text, tooManyAttempts):
		return result{lockedOut, said}
	case c.Component == stage && wrong:
		return result{wrongAnswer, said}
	case c.Component == "ak-stage-access-denied" && wrong:
		// The 5th wrong password ends Authentik's sign-in flow
		// (failed_attempts_before_cancel); the attempt was still refused as wrong.
		return result{wrongAnswer, said + " (Authentik ended the flow)"}
	case slices.Contains(next, c.Component):
		return result{accepted, said}
	}
	return result{noAnswer, "not an answer to the attempt: " + said + "; " + c.reply()}
}

// expect decides a check on an attempt's result. One of want passes. Any
// other real answer fails the check; accepted, when the check wants a
// refusal, is a security failure. No answer is a test error, whatever the
// check wanted: the check has learnt nothing.
func expect(t testing.TB, what string, r result, want ...outcome) bool {
	t.Helper()
	refusal := !slices.Contains(want, accepted)
	switch {
	case slices.Contains(want, r.outcome):
		return true
	case r.outcome == noAnswer:
		t.Fatalf("TEST ERROR: %s: no answer, so nothing was checked: %s", what, r.said)
	case r.outcome == accepted && refusal:
		t.Errorf("SECURITY FAILURE: %s: accepted (%s); want %v", what, r.said, want)
	default:
		t.Errorf("FAILED: %s: %v (%s); want %v", what, r.outcome, r.said, want)
	}
	return false
}

// CI path-filter proof (ADR 0014): a Go change with a doc change runs CI. Reverted by the next commit.
