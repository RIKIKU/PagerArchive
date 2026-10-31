package stacktest

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A33 (R44a): after 5 wrong attempts for one account, further attempts are
// refused for 15 minutes, even if the flow is restarted or a new code is
// requested. Covers password-reset codes, passwords and the enrolment code.
// Each attempt is logged, so the run shows what happens, and each check
// decides on a real answer from Authentik (outcome_test.go, ADR 0014).

const wrongAttempts = 5

// unique is an address at the allowed domain that no earlier run on this
// stack used, so a check can run again (go test -count) on one stack.
func unique(name string) string {
	return fmt.Sprintf("%s-%d@example.com", name, time.Now().UnixNano()%1e9)
}

// otherCode is a code that isn't code.
func otherCode(code string) string {
	n, _ := strconv.Atoi(code)
	return fmt.Sprintf("%06d", (n+1)%1000000)
}

// throttleWait outlasts the email device's own throttle (1, 2, 4, 8 … s
// after successive failures), so that each attempt is really checked.
func throttleWait(failures int) {
	time.Sleep(time.Duration(1<<(failures-1))*time.Second + 300*time.Millisecond)
}

// The checking stages, and what follows each when the answer is right.
const (
	passwordStage  = "ak-stage-password"               // sign-in: then into the app
	resetCodeStage = "ak-stage-authenticator-validate" // password reset: then the new-password page
	enrolCodeStage = "ak-stage-authenticator-email"    // enrolment: then into the app
)

// resetAttempt runs the password-reset flow from the start, requesting a
// new code, and enters it (with wrong, a wrong one).
func (s *stack) resetAttempt(t *testing.T, email string, wrong bool) result {
	t.Helper()
	return s.resetAttemptAs(t, email, email, wrong)
}

// resetAttemptAs is resetAttempt with the address typed as typed; the code
// still arrives in mailbox. The lock may refuse before a code is asked for.
func (s *stack) resetAttemptAs(t *testing.T, typed, mailbox string, wrong bool) result {
	t.Helper()
	r := s.browser(t, mailbox)
	var since time.Time
	c := r.executor("pagerarchive-recovery", "", nil)
	for steps := 0; steps < 8; steps++ {
		var body map[string]any
		switch {
		case c.Component == "ak-stage-identification":
			body = map[string]any{"uid_field": typed}
		case c.Component == resetCodeStage && strings.Contains(c.errorText(), "Empty response"): // the code was sent
			code := s.mailpit.code(t, mailbox, since)
			if wrong {
				code = otherCode(code)
			}
			return judge(r.executor("pagerarchive-recovery", "", map[string]any{"component": c.Component, "code": code}), resetCodeStage, "ak-stage-prompt")
		case c.Component == resetCodeStage && since.IsZero():
			since = time.Now()
			var v struct {
				Devices []map[string]any `json:"device_challenges"`
			}
			_ = json.Unmarshal(c.raw, &v)
			if len(v.Devices) == 0 {
				return result{noAnswer, "no code offered: " + c.errorText()}
			}
			body = map[string]any{"selected_challenge": v.Devices[0]}
		default: // refused before a code was entered, or no answer
			return judge(c, resetCodeStage, "ak-stage-prompt")
		}
		body["component"] = c.Component
		c = r.executor("pagerarchive-recovery", "", body)
	}
	return result{noAnswer, "the flow didn't reach the code: " + c.Component}
}

// passwordAttempt signs in from a fresh start with password.
func (s *stack) passwordAttempt(t *testing.T, email, password string) result {
	t.Helper()
	b := s.browser(t, email)
	b.pw = password
	return b.signInAttempt()
}

// shiftFailures moves an account's recorded sign-in events and code
// failures back in time, so the 15-minute lock can be checked without
// waiting. Test-only: it edits Authentik's database directly.
func (s *stack) shiftFailures(t *testing.T, username string, by time.Duration) {
	t.Helper()
	sql := fmt.Sprintf(`
UPDATE authentik_events_event SET created = created - interval '%d seconds'
 WHERE action IN ('login_failed', 'login') AND (context->>'username' = :'u' OR "user"->>'username' = :'u');
UPDATE authentik_stages_authenticator_email_emaildevice SET throttling_failure_timestamp = throttling_failure_timestamp - interval '%d seconds'
 WHERE user_id IN (SELECT id FROM authentik_core_user WHERE username = :'u');
`, int(by.Seconds()), int(by.Seconds()))
	cmd := exec.Command("docker", "compose", "-f", "compose.yaml", "-f", "deploy/test/compose.yaml",
		"exec", "-T", "db", "psql", "-q", "-v", "ON_ERROR_STOP=1", "-v", "u="+username, "-U", "postgres", "-d", "authentik")
	cmd.Dir = "../.."
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shift failures: %v: %s", err, out)
	}
}

// failureCount is what the lock counts for an account (the blueprint's
// pagerarchive-not-locked): its login_failed events, plus the failure
// count on its email device. The executor reads it before sending a
// password or code again (ADR 0014).
func (s *stack) failureCount(t *testing.T, username string) int {
	t.Helper()
	cmd := exec.Command("docker", "compose", "-f", "compose.yaml", "-f", "deploy/test/compose.yaml",
		"exec", "-T", "db", "psql", "-qtA", "-v", "ON_ERROR_STOP=1", "-v", "u="+username, "-U", "postgres", "-d", "authentik")
	cmd.Dir = "../.."
	cmd.Stdin = strings.NewReader(`
SELECT (SELECT count(*) FROM authentik_events_event WHERE action = 'login_failed' AND context->>'username' = :'u')
     + (SELECT coalesce(sum(d.throttling_failure_count), 0) FROM authentik_stages_authenticator_email_emaildevice d
         JOIN authentik_core_user u ON u.id = d.user_id WHERE u.username = :'u');
`)
	out, err := cmd.CombinedOutput()
	n, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || convErr != nil {
		t.Fatalf("count failures: %v: %s", err, out)
	}
	return n
}

// lockLasts15Minutes checks an attempt with the right answer 14 minutes
// after the failures (refused by the lock) and 16 minutes after (accepted).
func (s *stack) lockLasts15Minutes(t *testing.T, username string, attempt func() result) {
	t.Helper()
	s.shiftFailures(t, username, 14*time.Minute)
	r := attempt()
	t.Logf("A33:   14 minutes later, right answer: %v: %s", r.outcome, r.said)
	expect(t, "the right answer 14 minutes after the 5th failure", r, lockedOut)
	s.shiftFailures(t, username, 2*time.Minute)
	r = attempt()
	t.Logf("A33:   16 minutes later, right answer: %v: %s", r.outcome, r.said)
	expect(t, "the right answer 16 minutes after the 5th failure", r, accepted)
}

func TestA33GuessingIsLimited(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)

	t.Run("password-reset codes", func(t *testing.T) {
		b := s.inviteAndEnrol(t, o, "a33reset@example.com")
		for i := 1; i <= wrongAttempts; i++ {
			r := s.resetAttempt(t, b.email, true)
			t.Logf("A33: reset attempt %d (new flow, new code), wrong code: %v: %s", i, r.outcome, r.said)
			expect(t, fmt.Sprintf("wrong reset code %d", i), r, wrongAnswer)
			throttleWait(i)
		}
		r := s.resetAttempt(t, b.email, false)
		t.Logf("A33: reset attempt %d (new flow, new code), RIGHT code: %v: %s", wrongAttempts+1, r.outcome, r.said)
		expect(t, "the right reset code after 5 wrong ones", r, lockedOut)
		s.lockLasts15Minutes(t, b.email, func() result { return s.resetAttempt(t, b.email, false) })
	})

	t.Run("passwords", func(t *testing.T) {
		c := s.inviteAndEnrol(t, o, "a33password@example.com")
		for i := 1; i <= wrongAttempts; i++ {
			r := s.passwordAttempt(t, c.email, fmt.Sprintf("wrong password %d", i))
			t.Logf("A33: sign-in attempt %d (new flow), wrong password: %v: %s", i, r.outcome, r.said)
			expect(t, fmt.Sprintf("wrong password %d", i), r, wrongAnswer)
		}
		r := s.passwordAttempt(t, c.email, c.pw)
		t.Logf("A33: sign-in attempt %d (new flow), RIGHT password: %v: %s", wrongAttempts+1, r.outcome, r.said)
		expect(t, "the right password after 5 wrong ones", r, lockedOut)
		s.lockLasts15Minutes(t, c.email, func() result { return s.passwordAttempt(t, c.email, c.pw) })
	})

	t.Run("enrolment code", func(t *testing.T) {
		email := "a33enrol@example.com"
		link := o.invite(email)
		d := s.browser(t, email)
		query := link[strings.Index(link, "?")+1:]
		since := time.Now()
		c := d.executor("pagerarchive-enrolment", query, nil)
		c = d.executor("pagerarchive-enrolment", query, map[string]any{"component": "ak-stage-prompt",
			"name": "A33 Enrol", "password": d.pw, "password_repeat": d.pw})
		if c.Component != enrolCodeStage {
			t.Fatalf("no code page: %s %s %s", c.Component, c.errorText(), c.reply())
		}
		code := s.mailpit.code(t, email, since)
		for i := 1; i <= wrongAttempts; i++ {
			r := judge(d.executor("pagerarchive-enrolment", query, map[string]any{"component": enrolCodeStage, "code": otherCode(code)}),
				enrolCodeStage, "xak-flow-redirect")
			t.Logf("A33: enrolment code attempt %d, wrong code: %v: %s", i, r.outcome, r.said)
			expect(t, fmt.Sprintf("wrong enrolment code %d", i), r, wrongAnswer)
			throttleWait(i)
		}
		r := judge(d.executor("pagerarchive-enrolment", query, map[string]any{"component": enrolCodeStage, "code": code}),
			enrolCodeStage, "xak-flow-redirect")
		t.Logf("A33: enrolment code attempt %d, RIGHT code: %v: %s", wrongAttempts+1, r.outcome, r.said)
		expect(t, "the right enrolment code after 5 wrong ones", r, lockedOut)
		if exists, active := s.authentikUser(t, email); active {
			t.Errorf("enrolling user: exists %v, active %v", exists, active)
		}
	})
}

// R44a within one flow, without restarting: each attempt is checked, so
// the 6th is refused too. And one account's lock doesn't touch another.
func TestA33WithinOneFlow(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)

	u := s.inviteAndEnrol(t, o, unique("a33sameflow"))
	b := s.browser(t, u.email)
	end, c := b.walk(s.app.JoinPath("/auth/login").String(), func(c challenge) map[string]any {
		if c.Component == "ak-stage-identification" {
			return map[string]any{"uid_field": u.email}
		}
		return nil // the password page (answered below), or anything else
	})
	if end != nil || c.Component != passwordStage {
		t.Fatalf("no password page: %v %s %s", end, c.Component, c.reply())
	}
	// walk stopped inside the sign-in flow; carry on in the same flow.
	slug, query := "pagerarchive-authentication", c.query
	ended := false
	for i := 1; i <= wrongAttempts; i++ {
		r := judge(b.executor(slug, query, map[string]any{"component": passwordStage, "password": fmt.Sprintf("wrong %d", i)}), passwordStage, "xak-flow-redirect")
		t.Logf("A33: same flow, password attempt %d, wrong: %v: %s", i, r.outcome, r.said)
		expect(t, fmt.Sprintf("wrong password %d in one flow", i), r, wrongAnswer)
		ended = strings.HasSuffix(r.said, "(Authentik ended the flow)")
		if ended && i < wrongAttempts {
			t.Fatalf("Authentik ended the sign-in flow after %d wrong passwords, not 5", i)
		}
	}
	if ended {
		// Authentik cancels the flow at the 5th wrong password, so there is
		// no 6th attempt in it; the next one is a new flow, checked below.
		t.Log("A33: same flow, password attempt 6: none, Authentik ended the flow at the 5th")
	} else {
		r := judge(b.executor(slug, query, map[string]any{"component": passwordStage, "password": u.pw}), passwordStage, "xak-flow-redirect")
		t.Logf("A33: same flow, password attempt %d, RIGHT: %v: %s", wrongAttempts+1, r.outcome, r.said)
		expect(t, "the right password in the same flow after 5 wrong ones", r, lockedOut)
	}
	r := s.passwordAttempt(t, u.email, u.pw)
	t.Logf("A33: new flow, RIGHT password: %v: %s", r.outcome, r.said)
	expect(t, "the right password in a new flow after 5 wrong ones", r, lockedOut)

	// Reset codes in one flow: no cancel there, so this is the lock alone.
	rs := s.inviteAndEnrol(t, o, unique("a33sameflowreset"))
	rb := s.browser(t, rs.email)
	rc := rb.executor("pagerarchive-recovery", "", nil)
	rc = rb.executor("pagerarchive-recovery", "", map[string]any{"component": rc.Component, "uid_field": rs.email})
	var v struct {
		Devices []map[string]any `json:"device_challenges"`
	}
	_ = json.Unmarshal(rc.raw, &v)
	if len(v.Devices) == 0 {
		t.Fatalf("no code offered: %s %s", rc.Component, rc.reply())
	}
	since := time.Now()
	rc = rb.executor("pagerarchive-recovery", "", map[string]any{"component": rc.Component, "selected_challenge": v.Devices[0]})
	code := s.mailpit.code(t, rs.email, since)
	for i := 1; i <= wrongAttempts; i++ {
		r := judge(rb.executor("pagerarchive-recovery", "", map[string]any{"component": resetCodeStage, "code": otherCode(code)}), resetCodeStage, "ak-stage-prompt")
		t.Logf("A33: same flow, reset code attempt %d, wrong: %v: %s", i, r.outcome, r.said)
		expect(t, fmt.Sprintf("wrong reset code %d in one flow", i), r, wrongAnswer)
		throttleWait(i)
	}
	r = judge(rb.executor("pagerarchive-recovery", "", map[string]any{"component": resetCodeStage, "code": code}), resetCodeStage, "ak-stage-prompt")
	t.Logf("A33: same flow, reset code attempt %d, RIGHT: %v: %s", wrongAttempts+1, r.outcome, r.said)
	expect(t, "the right reset code in the same flow after 5 wrong ones", r, lockedOut)

	r = s.passwordAttempt(t, o.email, o.pw)
	if expect(t, "another account (the owner) signing in", r, accepted) {
		t.Log("A33: another account (the owner) still signs in")
	}
}

// R44a can't be dodged by typing the address differently: wrong attempts
// with B's address in other cases or with spaces count against B, and the
// right answer is then refused however the address is typed.
func TestA33AddressVariations(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)
	variants := func(email string) []string {
		mixed := strings.ToUpper(email[:1]) + email[1:strings.Index(email, "@")+1] + strings.ToUpper(email[strings.Index(email, "@")+1:strings.Index(email, "@")+2]) + email[strings.Index(email, "@")+2:]
		return []string{strings.ToUpper(email), mixed, "  " + email + "  ", " " + strings.ToUpper(email), mixed + " "}
	}

	t.Run("passwords", func(t *testing.T) {
		b := s.inviteAndEnrol(t, o, "a33variantpw@example.com")
		vs := variants(b.email)
		for i, v := range vs {
			r := s.passwordAttempt(t, v, fmt.Sprintf("wrong password %d", i+1))
			t.Logf("A33: sign-in attempt %d as %q, wrong password: %v: %s", i+1, v, r.outcome, r.said)
			expect(t, fmt.Sprintf("wrong password %d as %q", i+1, v), r, wrongAnswer)
		}
		for _, v := range append([]string{b.email}, vs...) {
			r := s.passwordAttempt(t, v, b.pw)
			t.Logf("A33: then the RIGHT password as %q: %v: %s", v, r.outcome, r.said)
			expect(t, fmt.Sprintf("the right password as %q after 5 wrong ones", v), r, lockedOut)
		}
	})

	t.Run("password-reset codes", func(t *testing.T) {
		c := s.inviteAndEnrol(t, o, "a33variantreset@example.com")
		vs := variants(c.email)
		for i, v := range vs {
			r := s.resetAttemptAs(t, v, c.email, true)
			t.Logf("A33: reset attempt %d as %q, wrong code: %v: %s", i+1, v, r.outcome, r.said)
			expect(t, fmt.Sprintf("wrong reset code %d as %q", i+1, v), r, wrongAnswer)
			throttleWait(i + 1)
		}
		for _, v := range append([]string{c.email}, vs...) {
			r := s.resetAttemptAs(t, v, c.email, false)
			t.Logf("A33: then the RIGHT code as %q: %v: %s", v, r.outcome, r.said)
			expect(t, fmt.Sprintf("the right reset code as %q after 5 wrong ones", v), r, lockedOut)
		}
	})
}
