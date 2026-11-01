package stacktest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// browser is one person's cookie jar, driving the app and Authentik the way
// the browser app does, but through Authentik's flow executor API instead
// of its web components.
type browser struct {
	t      *testing.T
	s      *stack
	email  string
	pw     string
	client *http.Client
	// failures counts the failures Authentik has recorded for the account
	// (the lock's own count), or is nil where there's no database to ask.
	failures func() int
}

func (s *stack) browser(t *testing.T, email string) *browser {
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, s: s, email: email, pw: "correct horse " + strings.ReplaceAll(email, "@", " at "), client: &http.Client{
		Jar:           jar,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if s.db {
		b.failures = func() int { return s.failureCount(t, strings.ToLower(strings.TrimSpace(email))) }
	}
	return b
}

// challenge is one step of an Authentik flow.
type challenge struct {
	Component string `json:"component"`
	To        string `json:"to"`
	Error     string `json:"error_message"`
	Errors    map[string][]struct {
		String string `json:"string"`
	} `json:"response_errors"`
	status int    // the reply's HTTP status
	raw    []byte // the reply's body
	err    error  // why there was no reply (a timeout)
	query  string // the flow's query, when walk stopped inside a flow
}

// isChallenge reports whether the reply was a step of a flow: a 2xx reply
// naming a component. Anything else parses to an empty challenge, so its
// status and body are all there is to go on.
func (c challenge) isChallenge() bool {
	return c.status >= 200 && c.status < 300 && c.Component != ""
}

// reply is the status and body, for messages about a reply that isn't a
// flow challenge.
func (c challenge) reply() string {
	if c.err != nil {
		return "no reply: " + c.err.Error()
	}
	return fmt.Sprintf("HTTP %d %.500q", c.status, c.raw)
}

func (c challenge) errorText() string {
	var parts []string
	if c.Error != "" {
		parts = append(parts, c.Error)
	}
	for k, v := range c.Errors {
		for _, e := range v {
			parts = append(parts, k+": "+e.String)
		}
	}
	return strings.Join(parts, "; ")
}

// answer decides what to send for a challenge; nil stops the walk there.
type answer func(c challenge) map[string]any

var flowPath = regexp.MustCompile(`^/if/flow/([a-z0-9-]+)/$`)

// walk follows redirects from start across the app and Authentik. Pages of
// Authentik's flow interface are run through the executor API with
// answers. It stops at a page of the app, returning its URL, or at a
// challenge that answers returns nil for.
func (b *browser) walk(start string, answers answer) (*url.URL, challenge) {
	b.t.Helper()
	next, _ := url.Parse(start)
	for range 40 {
		if m := flowPath.FindStringSubmatch(next.Path); m != nil && next.Host == b.s.authentik.Host {
			c := b.executor(m[1], next.RawQuery, nil)
			for {
				if c.Component == "xak-flow-redirect" {
					to, err := next.Parse(c.To)
					if err != nil {
						b.t.Fatalf("bad redirect %q", c.To)
					}
					next = to
					break
				}
				body := answers(c)
				if body == nil {
					c.query = next.RawQuery
					return nil, c
				}
				body["component"] = c.Component
				c = b.executor(m[1], next.RawQuery, body)
			}
			continue
		}
		resp, err := b.client.Get(next.String())
		if err != nil {
			b.t.Fatal(err)
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			if next.Host != b.s.app.Host {
				b.t.Fatalf("stopped at %s (%d)", next, resp.StatusCode)
			}
			return next, challenge{}
		}
		if next, err = next.Parse(loc); err != nil {
			b.t.Fatal(err)
		}
	}
	b.t.Fatal("too many redirects")
	return nil, challenge{}
}

// transientTries bounds how often executor sends one request whose replies
// are clearly transient (transient). Each resend is logged (ADR 0014).
const transientTries = 3

// transient reports whether a reply is worth sending the same request
// again for: a 5xx (Authentik's front server answers 502 when it couldn't
// hand the request to a web worker) or no reply at all. Nothing else is
// ever resent: a refusal, an acceptance or a 4xx is the answer.
func (c challenge) transient() bool {
	return c.err != nil || c.status >= 500
}

// executor gets (body nil) or answers the current challenge of a flow. A
// transient reply is retried, at most transientTries times in all; a
// request carrying a password or code only while the account's failures
// show the earlier try wasn't counted, so a retried attempt never counts
// twice (and a counted one is never lost: its reply is returned as it came).
func (b *browser) executor(slug, query string, body map[string]any) challenge {
	b.t.Helper()
	secret := body["password"] != nil || body["code"] != nil
	before := 0
	if secret && b.failures != nil {
		before = b.failures()
	}
	for redirects, tries := 0, 1; ; {
		c, answered := b.exchange(slug, query, body)
		if answered { // the executor redirects to itself after a POST: the POST was answered
			if redirects++; redirects == 5 {
				b.t.Fatal("executor redirect loop")
			}
			body, secret = nil, false
			continue
		}
		if !c.isChallenge() {
			b.t.Logf("executor %s at %s: not a flow challenge: %s", slug, time.Now().UTC().Format("15:04:05.000"), c.reply())
		}
		if !c.transient() || tries == transientTries {
			return c
		}
		if secret {
			if b.failures == nil {
				return c // no way to tell whether Authentik counted it
			}
			if n := b.failures(); n != before {
				b.t.Logf("executor %s: Authentik counted that attempt (the account's failures went from %d to %d), so it isn't sent again", slug, before, n)
				return c
			}
		}
		tries++
		b.t.Logf("executor %s: sending the same request again (try %d of %d)", slug, tries, transientTries)
		time.Sleep(time.Duration(tries-1) * time.Second)
	}
}

// exchange makes one request to the flow executor. answered says the
// executor redirected to itself, as it does after taking a POST.
func (b *browser) exchange(slug, query string, body map[string]any) (c challenge, answered bool) {
	u := b.s.authentik.JoinPath("/api/v3/flows/executor/", slug, "/").String() + "?query=" + url.QueryEscape(query)
	var req *http.Request
	if body != nil {
		data, _ := json.Marshal(body)
		req, _ = http.NewRequest(http.MethodPost, u, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		// Once signed in, Authentik wants its CSRF cookie echoed in a
		// header, as its web pages do (the device code, ADR 0009 §2).
		for _, c := range b.client.Jar.Cookies(b.s.authentik) {
			if c.Name == "authentik_csrf" {
				req.Header.Set("X-authentik-CSRF", c.Value)
			}
		}
	} else {
		req, _ = http.NewRequest(http.MethodGet, u, nil)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return challenge{err: err}, false
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusFound {
		return challenge{}, true
	}
	_ = json.Unmarshal(data, &c) // a body that isn't JSON leaves it empty: not a challenge
	c.status, c.raw = resp.StatusCode, data
	return c, false
}

// enrolAnswers fills in the enrolment flow: name and password, then the
// emailed code (wrongCode first, if set).
func (b *browser) enrolAnswers(wrongCode string, seenWrong *string) answer {
	since := time.Now() // the code is sent after this, when the prompt is answered
	return func(c challenge) map[string]any {
		switch c.Component {
		case "ak-stage-prompt":
			return map[string]any{"name": "Test " + b.email, "password": b.pw, "password_repeat": b.pw}
		case "ak-stage-authenticator-email":
			if len(c.Errors) > 0 && seenWrong != nil && *seenWrong == "" {
				*seenWrong = c.errorText()
			}
			if wrongCode != "" && seenWrong != nil && *seenWrong == "" {
				return map[string]any{"code": wrongCode}
			}
			return map[string]any{"code": b.s.mailpit.code(b.t, b.email, since)}
		}
		return nil
	}
}

// signInAnswers fills in the sign-in flow: email and password.
func (b *browser) signInAnswers() answer {
	return func(c challenge) map[string]any {
		if len(c.Errors) > 0 || c.Error != "" {
			return nil // a wrong password, or refused: stop there
		}
		switch c.Component {
		case "ak-stage-identification": // with the password on the same page, or not
			return map[string]any{"uid_field": b.email, "password": b.pw}
		case "ak-stage-password":
			return map[string]any{"password": b.pw}
		}
		return nil
	}
}

// signIn signs in through the app's /auth/login. It reports whether the
// browser ended up signed in, and the challenge it stopped at if not.
func (b *browser) signIn() (bool, challenge) {
	b.t.Helper()
	end, c := b.walk(b.s.app.JoinPath("/auth/login").String(), b.signInAnswers())
	if end == nil {
		return false, c
	}
	return end.Path != "/signin", challenge{Error: end.RawQuery}
}

// signInAttempt signs in as signIn does, and says what came of it: a check
// that someone can't sign in passes only on a real refusal (outcome_test.go).
func (b *browser) signInAttempt() result {
	b.t.Helper()
	end, c := b.walk(b.s.app.JoinPath("/auth/login").String(), b.signInAnswers())
	switch {
	case end == nil:
		return judge(c, passwordStage, "xak-flow-redirect")
	case end.Path == "/signin":
		return result{appRefused, "the app's sign-in page: " + end.RawQuery}
	}
	return result{accepted, "signed in, at " + end.Path}
}

// api calls the app's API with this browser's session.
func (b *browser) api(method, path string, body, out any) (int, string) {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, b.s.app.JoinPath(path).String(), rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			b.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	var p struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &p)
	return resp.StatusCode, p.Error
}

// mailpit reads the test mail catcher.
type mailpit struct{ url *url.URL }

type mailMessage struct {
	ID      string    `json:"ID"`
	Created time.Time `json:"Created"`
	Subject string    `json:"Subject"`
}

// latest waits for the newest message to address received after since
// whose subject contains subject, and returns its text.
func (m mailpit) latest(t *testing.T, address, subject string, since time.Time) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var list struct {
			Messages []mailMessage `json:"messages"`
		}
		q := url.Values{"query": {fmt.Sprintf(`to:"%s"`, address)}}
		resp, err := http.Get(m.url.JoinPath("/api/v1/search").String() + "?" + q.Encode())
		if err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&list)
			resp.Body.Close()
		}
		for _, msg := range list.Messages {
			if msg.Created.After(since) && strings.Contains(msg.Subject, subject) {
				resp, err := http.Get(m.url.JoinPath("/api/v1/message", msg.ID).String())
				if err != nil {
					t.Fatal(err)
				}
				var full struct {
					Text string `json:"Text"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&full)
				resp.Body.Close()
				return full.Text
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("no %q email to %s", subject, address)
	return ""
}

var codeRe = regexp.MustCompile(`\b[0-9]{6}\b`)

func (m mailpit) code(t *testing.T, address string, since time.Time) string {
	t.Helper()
	c := codeRe.FindString(m.latest(t, address, "code", since))
	if c == "" {
		t.Fatalf("no code in the email to %s", address)
	}
	return c
}

var inviteLinkRe = regexp.MustCompile(`https?://\S+itoken=[0-9a-f-]+`)

// invitationLink is the link in the newest invitation email to address.
func (m mailpit) invitationLink(t *testing.T, address string, since time.Time) string {
	t.Helper()
	link := inviteLinkRe.FindString(m.latest(t, address, "invited", since))
	if link == "" {
		t.Fatalf("no invitation link in the email to %s", address)
	}
	return link
}

// postForRedirect POSTs to an app path, as the sign-in page's button does,
// and returns the absolute URL the answer says to go to.
func (b *browser) postForRedirect(path string) string {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, b.s.app.JoinPath(path).String(), nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin") // as the browser app's fetch
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	var next struct {
		Redirect string `json:"redirect"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&next); err != nil || resp.StatusCode != http.StatusOK {
		b.t.Fatalf("POST %s: %d %v", path, resp.StatusCode, err)
	}
	loc, err := b.s.app.Parse(next.Redirect)
	if err != nil {
		b.t.Fatal(err)
	}
	return loc.String()
}
