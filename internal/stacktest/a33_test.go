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
// Each attempt is logged, so the run shows what happens.

const wrongAttempts = 5

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

// resetAttempt runs the password-reset flow from the start, requesting a
// new code. With wrong, it enters a wrong code. It reports whether the
// correct code was accepted (the new-password page was reached), and what
// the flow said.
func (s *stack) resetAttempt(t *testing.T, email string, wrong bool) (bool, string) {
	t.Helper()
	return s.resetAttemptAs(t, email, email, wrong)
}

// resetAttemptAs is resetAttempt with the address typed as typed; the code
// still arrives in mailbox.
func (s *stack) resetAttemptAs(t *testing.T, typed, mailbox string, wrong bool) (bool, string) {
	t.Helper()
	r := s.browser(t, mailbox)
	var since time.Time
	c := r.executor("pagerarchive-recovery", "", nil)
	for steps := 0; steps < 8; steps++ {
		var body map[string]any
		switch c.Component {
		case "ak-stage-identification":
			body = map[string]any{"uid_field": typed}
		case "ak-stage-authenticator-validate":
			switch {
			case strings.Contains(c.errorText(), "Empty response"): // the code was sent
				code := s.mailpit.code(t, mailbox, since)
				if wrong {
					code = otherCode(code)
				}
				body = map[string]any{"code": code}
			case since.IsZero():
				since = time.Now()
				var v struct {
					Devices []map[string]any `json:"device_challenges"`
				}
				_ = json.Unmarshal(c.raw, &v)
				if len(v.Devices) == 0 {
					return false, "no code offered: " + c.errorText()
				}
				body = map[string]any{"selected_challenge": v.Devices[0]}
			default:
				return false, "code refused: " + c.errorText()
			}
		case "ak-stage-prompt":
			return true, "code accepted: new-password page"
		case "ak-stage-access-denied":
			return false, "refused: " + c.errorText()
		default:
			return false, c.Component + ": " + c.errorText()
		}
		body["component"] = c.Component
		c = r.executor("pagerarchive-recovery", "", body)
	}
	return false, "flow didn't settle: " + c.Component
}

// passwordAttempt signs in from a fresh start with password.
func (s *stack) passwordAttempt(t *testing.T, email, password string) (bool, string) {
	t.Helper()
	b := s.browser(t, email)
	b.pw = password
	ok, c := b.signIn()
	if ok {
		return true, "signed in"
	}
	if c.Component == "" {
		return false, "not signed in: " + c.Error
	}
	return false, c.Component + ": " + c.errorText()
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

// lockLasts15Minutes checks an attempt with the right answer 14 minutes
// after the failures (still refused) and 16 minutes after (accepted).
func (s *stack) lockLasts15Minutes(t *testing.T, username string, attempt func() (bool, string)) {
	t.Helper()
	s.shiftFailures(t, username, 14*time.Minute)
	ok, msg := attempt()
	t.Logf("A33:   14 minutes later, right answer: %s", msg)
	if ok {
		t.Error("accepted 14 minutes after the 5th failure")
	}
	s.shiftFailures(t, username, 2*time.Minute)
	ok, msg = attempt()
	t.Logf("A33:   16 minutes later, right answer: %s", msg)
	if !ok {
		t.Error("still refused 16 minutes after the 5th failure")
	}
}

func TestA33GuessingIsLimited(t *testing.T) {
	s := setup(t, "restarted")
	o := s.owner(t)

	t.Run("password-reset codes", func(t *testing.T) {
		b := s.inviteAndEnrol(t, o, "a33reset@example.com")
		for i := 1; i <= wrongAttempts; i++ {
			_, msg := s.resetAttempt(t, b.email, true)
			t.Logf("A33: reset attempt %d (new flow, new code), wrong code: %s", i, msg)
			throttleWait(i)
		}
		ok, msg := s.resetAttempt(t, b.email, false)
		t.Logf("A33: reset attempt %d (new flow, new code), RIGHT code: %s", wrongAttempts+1, msg)
		if ok {
			t.Error("the right reset code was accepted after 5 wrong ones")
		}
		s.lockLasts15Minutes(t, b.email, func() (bool, string) { return s.resetAttempt(t, b.email, false) })
	})

	t.Run("passwords", func(t *testing.T) {
		c := s.inviteAndEnrol(t, o, "a33password@example.com")
		for i := 1; i <= wrongAttempts; i++ {
			_, msg := s.passwordAttempt(t, c.email, fmt.Sprintf("wrong password %d", i))
			t.Logf("A33: sign-in attempt %d (new flow), wrong password: %s", i, msg)
		}
		ok, msg := s.passwordAttempt(t, c.email, c.pw)
		t.Logf("A33: sign-in attempt %d (new flow), RIGHT password: %s", wrongAttempts+1, msg)
		if ok {
			t.Error("the right password was accepted after 5 wrong ones")
		}
		s.lockLasts15Minutes(t, c.email, func() (bool, string) { return s.passwordAttempt(t, c.email, c.pw) })
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
		if c.Component != "ak-stage-authenticator-email" {
			t.Fatalf("no code page: %s %s", c.Component, c.errorText())
		}
		code := s.mailpit.code(t, email, since)
		for i := 1; i <= wrongAttempts; i++ {
			c = d.executor("pagerarchive-enrolment", query, map[string]any{"component": c.Component, "code": otherCode(code)})
			t.Logf("A33: enrolment code attempt %d, wrong code: %s %s", i, c.Component, c.errorText())
			throttleWait(i)
		}
		c = d.executor("pagerarchive-enrolment", query, map[string]any{"component": "ak-stage-authenticator-email", "code": code})
		t.Logf("A33: enrolment code attempt %d, RIGHT code: %s %s", wrongAttempts+1, c.Component, c.errorText())
		if c.Component == "xak-flow-redirect" {
			t.Error("the right enrolment code was accepted after 5 wrong ones")
		}
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

	u := s.inviteAndEnrol(t, o, "a33sameflow@example.com")
	b := s.browser(t, u.email)
	end, c := b.walk(s.app.JoinPath("/auth/login").String(), func(c challenge) map[string]any {
		switch c.Component {
		case "ak-stage-identification":
			return map[string]any{"uid_field": u.email}
		case "ak-stage-password":
			return nil // the password page: answered below
		}
		return nil
	})
	if end != nil || c.Component != "ak-stage-password" {
		t.Fatalf("no password page: %v %s", end, c.Component)
	}
	// walk stopped inside the sign-in flow; carry on in the same flow.
	slug, query := "pagerarchive-authentication", c.query
	for i := 1; i <= wrongAttempts; i++ {
		c = b.executor(slug, query, map[string]any{"component": "ak-stage-password", "password": fmt.Sprintf("wrong %d", i)})
		t.Logf("A33: same flow, password attempt %d, wrong: %s %s", i, c.Component, c.errorText())
		if c.Component != "ak-stage-password" {
			break // Authentik cancels the flow after 5 wrong passwords
		}
	}
	c = b.executor(slug, query, map[string]any{"component": "ak-stage-password", "password": u.pw})
	t.Logf("A33: same flow, password attempt %d, RIGHT: %s %s", wrongAttempts+1, c.Component, c.errorText())
	if c.Component == "xak-flow-redirect" {
		t.Error("right password accepted in the same flow after 5 wrong ones")
	}
	if ok, _ := s.passwordAttempt(t, u.email, u.pw); ok {
		t.Error("right password accepted in a new flow after 5 wrong ones")
	}
	// Reset codes in one flow: no cancel there, so this is the lock alone.
	r := s.inviteAndEnrol(t, o, "a33sameflowreset@example.com")
	rb := s.browser(t, r.email)
	rc := rb.executor("pagerarchive-recovery", "", nil)
	rc = rb.executor("pagerarchive-recovery", "", map[string]any{"component": rc.Component, "uid_field": r.email})
	var v struct {
		Devices []map[string]any `json:"device_challenges"`
	}
	_ = json.Unmarshal(rc.raw, &v)
	if len(v.Devices) == 0 {
		t.Fatalf("no code offered: %s", rc.Component)
	}
	since := time.Now()
	rc = rb.executor("pagerarchive-recovery", "", map[string]any{"component": rc.Component, "selected_challenge": v.Devices[0]})
	code := s.mailpit.code(t, r.email, since)
	for i := 1; i <= wrongAttempts; i++ {
		rc = rb.executor("pagerarchive-recovery", "", map[string]any{"component": "ak-stage-authenticator-validate", "code": otherCode(code)})
		t.Logf("A33: same flow, reset code attempt %d, wrong: %s %s", i, rc.Component, rc.errorText())
		throttleWait(i)
	}
	rc = rb.executor("pagerarchive-recovery", "", map[string]any{"component": "ak-stage-authenticator-validate", "code": code})
	t.Logf("A33: same flow, reset code attempt %d, RIGHT: %s %s", wrongAttempts+1, rc.Component, rc.errorText())
	if rc.Component != "ak-stage-access-denied" {
		t.Error("right reset code not refused in the same flow after 5 wrong ones")
	}

	if ok, msg := s.passwordAttempt(t, o.email, o.pw); !ok {
		t.Errorf("another account is affected: %s", msg)
	} else {
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
			_, msg := s.passwordAttempt(t, v, fmt.Sprintf("wrong password %d", i+1))
			t.Logf("A33: sign-in attempt %d as %q, wrong password: %s", i+1, v, msg)
		}
		for _, v := range append([]string{b.email}, vs...) {
			ok, msg := s.passwordAttempt(t, v, b.pw)
			t.Logf("A33: then the RIGHT password as %q: %s", v, msg)
			if ok {
				t.Errorf("signed in as %q after 5 wrong passwords", v)
			}
		}
	})

	t.Run("password-reset codes", func(t *testing.T) {
		c := s.inviteAndEnrol(t, o, "a33variantreset@example.com")
		vs := variants(c.email)
		for i, v := range vs {
			_, msg := s.resetAttemptAs(t, v, c.email, true)
			t.Logf("A33: reset attempt %d as %q, wrong code: %s", i+1, v, msg)
			throttleWait(i + 1)
		}
		for _, v := range append([]string{c.email}, vs...) {
			ok, msg := s.resetAttemptAs(t, v, c.email, false)
			t.Logf("A33: then the RIGHT code as %q: %s", v, msg)
			if ok {
				t.Errorf("reset code accepted as %q after 5 wrong ones", v)
			}
		}
	})
}
