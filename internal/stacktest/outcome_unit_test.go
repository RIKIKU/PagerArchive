package stacktest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// These run without the stack: a stand-in for Authentik's flow executor
// gives the replies a busy or broken Authentik could, and each must come
// out of judge and expect as what it is (ADR 0014).

// recorder is a testing.TB that keeps what expect says.
type recorder struct {
	testing.TB
	errors, fatals []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(f string, a ...any) {
	r.errors = append(r.errors, fmt.Sprintf(f, a...))
}
func (r *recorder) Fatalf(f string, a ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(f, a...))
}

// fakeExecutor answers each request to the flow executor with the next of
// its replies (the last one repeats), and counts the requests.
type fakeExecutor struct {
	mu      sync.Mutex
	replies []func(w http.ResponseWriter)
	n       int
}

func (f *fakeExecutor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	reply := f.replies[min(f.n, len(f.replies)-1)]
	f.n++
	f.mu.Unlock()
	reply(w)
}

func (f *fakeExecutor) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func status(code int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}
}

func hang(d time.Duration) func(http.ResponseWriter) {
	return func(http.ResponseWriter) { time.Sleep(d) }
}

const (
	lockedReply   = `{"component": "ak-stage-access-denied", "error_message": "Too many wrong attempts for this account. Wait 15 minutes, then try again."}`
	wrongReply    = `{"component": "ak-stage-authenticator-validate", "response_errors": {"code": [{"string": "Invalid Token. Please ensure the time on your device is accurate and try again."}]}}`
	acceptedReply = `{"component": "ak-stage-prompt"}`
	busyReply     = `{"detail": "A server error occurred."}`
)

// fakeBrowser is a browser whose Authentik is f.
func fakeBrowser(t *testing.T, f *fakeExecutor) *browser {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	b := (&stack{authentik: u, app: u}).browser(t, "fake@example.com")
	b.client.Timeout = 300 * time.Millisecond
	return b
}

// The 6th, right reset code, answered in each way a reply can come: only
// the lock's refusal passes, acceptance is a security failure, and every
// reply that isn't an answer is a test error naming its status and body.
func TestRepliesThatAreNotAnswers(t *testing.T) {
	cases := []struct {
		name   string
		reply  func(http.ResponseWriter)
		errors string // what expect must report as a failure ("" for none)
		fatals string // what it must report as a test error ("" for none)
	}{
		{"refused by the lock", status(200, lockedReply), "", ""},
		{"accepted", status(200, acceptedReply), "SECURITY FAILURE", ""},
		{"refused as wrong", status(200, wrongReply), "FAILED: the right code: refused as wrong", ""},
		{"HTTP 500 with a JSON error", status(500, busyReply), "", `TEST ERROR: the right code: no answer, so nothing was checked: not a flow challenge: HTTP 500 "{\"detail\": \"A server error occurred.\"}"`},
		{"HTTP 200 without a component", status(200, `{"detail": "Not found."}`), "", `not a flow challenge: HTTP 200 "{\"detail\": \"Not found.\"}"`},
		{"HTTP 502 that isn't JSON", status(502, "<html>Bad Gateway</html>"), "", `not a flow challenge: HTTP 502 "<html>Bad Gateway</html>"`},
		{"a step that doesn't answer it", status(200, `{"component": "ak-stage-identification"}`), "", "not an answer to the attempt: ak-stage-identification"},
		{"no reply in time", hang(time.Second), "", "no reply: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeExecutor{replies: []func(http.ResponseWriter){c.reply}}
			b := fakeBrowser(t, f)
			r := judge(b.executor("pagerarchive-recovery", "", map[string]any{"component": resetCodeStage, "code": "123456"}), resetCodeStage, "ak-stage-prompt")
			rec := &recorder{}
			passed := expect(rec, "the right code", r, lockedOut)
			t.Logf("%s: %v (%s): passed %v, failures %q, test errors %q", c.name, r.outcome, r.said, passed, rec.errors, rec.fatals)
			if passed != (c.errors == "" && c.fatals == "") {
				t.Errorf("passed %v", passed)
			}
			for _, w := range []struct {
				got  []string
				want string
				kind string
			}{{rec.errors, c.errors, "failure"}, {rec.fatals, c.fatals, "test error"}} {
				switch {
				case w.want == "" && len(w.got) > 0:
					t.Errorf("reported a %s: %q", w.kind, w.got)
				case w.want != "" && (len(w.got) != 1 || !strings.Contains(w.got[0], w.want)):
					t.Errorf("want a %s containing %q, got %q", w.kind, w.want, w.got)
				}
			}
		})
	}
}

// A retry is for transient replies only (a 5xx, or none), bounded and
// logged; a password or code is resent only while the account's failures
// show the earlier try wasn't counted; and no retry can turn an acceptance
// into a pass.
func TestOnlyTransientRepliesAreRetried(t *testing.T) {
	type counts []int // what failures returns, call by call (the last repeats)
	cases := []struct {
		name     string
		replies  []func(http.ResponseWriter)
		body     map[string]any
		failures counts // nil: no database to ask
		requests int
		outcome  outcome
	}{
		{"refused by the lock", []func(http.ResponseWriter){status(200, lockedReply)}, nil, nil, 1, lockedOut},
		{"accepted", []func(http.ResponseWriter){status(200, acceptedReply)}, nil, nil, 1, accepted},
		{"refused as wrong", []func(http.ResponseWriter){status(200, wrongReply)}, nil, nil, 1, wrongAnswer},
		{"a 4xx is an answer, not retried", []func(http.ResponseWriter){status(403, `{"detail": "CSRF Failed"}`), status(200, lockedReply)}, nil, nil, 1, noAnswer},
		{"a reply without a component, not retried", []func(http.ResponseWriter){status(200, `{"detail": "Not found."}`), status(200, lockedReply)}, nil, nil, 1, noAnswer},
		{"502, then refused by the lock", []func(http.ResponseWriter){status(502, `{"error": "failed to connect to authentik backend"}`), status(200, lockedReply)}, nil, nil, 2, lockedOut},
		{"no reply, then refused by the lock", []func(http.ResponseWriter){hang(time.Second), status(200, lockedReply)}, nil, nil, 2, lockedOut},
		{"5xx every time: three tries, then no answer", []func(http.ResponseWriter){status(503, busyReply)}, nil, nil, transientTries, noAnswer},
		{"a code after a 502, not counted: resent, and acceptance stays acceptance", []func(http.ResponseWriter){status(502, busyReply), status(200, acceptedReply)},
			map[string]any{"component": resetCodeStage, "code": "123456"}, counts{4}, 2, accepted},
		{"a code after a 502, counted: not resent", []func(http.ResponseWriter){status(502, busyReply), status(200, wrongReply)},
			map[string]any{"component": resetCodeStage, "code": "123456"}, counts{4, 5}, 1, noAnswer},
		{"a password after a 502, with no database to ask: not resent", []func(http.ResponseWriter){status(502, busyReply), status(200, lockedReply)},
			map[string]any{"component": passwordStage, "password": "x"}, nil, 1, noAnswer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeExecutor{replies: c.replies}
			b := fakeBrowser(t, f)
			if c.failures != nil {
				calls := 0
				b.failures = func() int { calls++; return c.failures[min(calls, len(c.failures))-1] }
			}
			body := c.body
			if body == nil {
				body = map[string]any{"component": "ak-stage-identification", "uid_field": "fake@example.com"}
			}
			r := judge(b.executor("pagerarchive-recovery", "", body), resetCodeStage, "ak-stage-prompt")
			t.Logf("%s: %d requests, %v (%s)", c.name, f.requests(), r.outcome, r.said)
			if f.requests() != c.requests || r.outcome != c.outcome {
				t.Errorf("%d requests and %v; want %d and %v", f.requests(), r.outcome, c.requests, c.outcome)
			}
			if r.outcome == accepted {
				rec := &recorder{}
				if expect(rec, "the right code", r, lockedOut) || len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "SECURITY FAILURE") {
					t.Errorf("an acceptance after a retry wasn't a security failure: %q %q", rec.errors, rec.fatals)
				}
			}
		})
	}
}
