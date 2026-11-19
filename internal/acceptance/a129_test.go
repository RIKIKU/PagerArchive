package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// a129Request is one of A129's requests, ended at one of its queries: the
// nth query whose SQL contains match ("" for any but the session's).
type a129Request struct {
	name, method, path, route string
	client, scrape            bool // as client A, or with the metrics token; else as the user
	match                     string
	nth                       int
}

// a129Requests are the paths A129 ends early, each at the query its
// handler's error path is about.
func a129Requests(id, event int64) []a129Request {
	return []a129Request{
		{name: "session", method: "GET", path: "/api/v1/me", route: "GET /api/v1/me", match: "-- name: SessionUser "},
		{name: "release seen", method: "POST", path: "/api/v1/me/release-seen", route: "POST /api/v1/me/release-seen", match: "-- name: SeeRelease "},
		{name: "timeline counts", method: "GET", path: "/api/v1/messages/counts", route: "GET /api/v1/messages/counts", match: "SET LOCAL jit"},
		{name: "timeline counts of a large event", method: "GET", path: fmt.Sprintf("/api/v1/messages/counts?event=%d", event), route: "GET /api/v1/messages/counts",
			match: "-- name: LargeEventByID ", nth: 2},
		{name: "message list", method: "GET", path: "/api/v1/messages", route: "GET /api/v1/messages"},
		{name: "a message", method: "GET", path: fmt.Sprintf("/api/v1/messages/%d", id), route: "GET /api/v1/messages/{id}"},
		{name: "meta", method: "GET", path: "/api/v1/meta", route: "GET /api/v1/meta"},
		{name: "albums", method: "GET", path: "/api/v1/albums", route: "GET /api/v1/albums"},
		{name: "report", method: "GET", path: "/api/v1/reports/options", route: "GET /api/v1/reports/options"},
		{name: "page post", method: "POST", path: "/api/v1/pages", route: "POST /api/v1/pages", client: true, match: "-- name: TouchClient "},
		{name: "scrape", method: "GET", path: "/metrics", route: "GET /metrics", scrape: true, match: "-- name: ListClients "},
	}
}

type a129Key struct{}

// a129Setup is A129's server, with a message and a large event, and the
// server's version a release, so release-seen records.
func a129Setup(t *testing.T) (*env, []a129Request) {
	e := setup(t)
	e.srv.Version = "1.2.3"
	e.mustPost("a", "0100001", row1, e.now.Add(-time.Hour))
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO large_events (first_at, last_at) SELECT min(sent_at), max(sent_at) FROM messages`); err != nil {
		t.Fatal(err)
	}
	var event int64
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT max(id) FROM large_events`).Scan(&event); err != nil {
		t.Fatal(err)
	}
	return e, a129Requests(e.messages()[0].ID, event)
}

// a129Do makes q through the server's handler, with end ending it at its
// query: given the request's cancel and the query's context, it returns
// the context the query runs with. It returns the status and the log lines
// written meanwhile, and fails if the query was never reached.
func (e *env) a129Do(q a129Request, end func(cancel context.CancelFunc, ctx context.Context) context.Context) (int, []map[string]any) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.WithValue(e.ctx, a129Key{}, q.name))
	defer cancel()
	seen, reached := 0, false
	e.queries.mu.Lock()
	e.queries.hook = func(qctx context.Context, sql string) context.Context {
		if qctx.Value(a129Key{}) != q.name || reached {
			return qctx
		}
		if q.match == "" && (strings.Contains(sql, "-- name: SessionUser ") || strings.Contains(sql, "-- name: TouchSession ")) {
			return qctx
		}
		if !strings.Contains(sql, q.match) {
			return qctx
		}
		if seen++; seen < max(q.nth, 1) {
			return qctx
		}
		reached = true
		return end(cancel, qctx)
	}
	e.queries.mu.Unlock()
	defer func() {
		e.queries.mu.Lock()
		e.queries.hook = nil
		e.queries.mu.Unlock()
	}()

	var body io.Reader
	switch {
	case q.client:
		b, _ := json.Marshal(server.PageRequest{ClientID: "a", Capcode: "0100002", Text: "QDAbandoned page post", ReceivedAt: e.now, Baud: 512})
		e.posts = append(e.posts, "QDAbandoned page post")
		body = strings.NewReader(string(b))
	case q.method == http.MethodPost:
		body = strings.NewReader(`{}`)
	}
	req := httptest.NewRequestWithContext(ctx, q.method, "http://pager.example.test"+q.path, body)
	switch {
	case q.client:
		req.Header.Set("Authorization", "Bearer "+e.keys["a"])
	case q.scrape:
		req.Header.Set("Authorization", "Bearer "+metricsToken)
	default:
		req.AddCookie(e.user.cookie)
	}
	if q.method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	before := len(e.logs.String())
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if !reached {
		e.t.Fatalf("%s: the request never reached its query (%q, %d)", q.name, q.match, max(q.nth, 1))
	}
	var lines []map[string]any
	sc := bufio.NewScanner(strings.NewReader(e.logs.String()[before:]))
	for sc.Scan() {
		var l map[string]any
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			lines = append(lines, l)
		}
	}
	return rec.Code, lines
}

// a129Metrics scrapes /metrics: each route's abandoned count, and each
// route and status's requests.
func (e *env) a129Metrics() (abandoned map[string]int, requests map[[2]string]int) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.url+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+metricsToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	abandoned, requests = map[string]int{}, map[[2]string]int{}
	ab := regexp.MustCompile(`(?m)^pagerarchive_http_requests_abandoned_total\{route="([^"]+)"\} (\d+)$`)
	for _, m := range ab.FindAllStringSubmatch(string(b), -1) {
		abandoned[m[1]], _ = strconv.Atoi(m[2])
	}
	rq := regexp.MustCompile(`(?m)^pagerarchive_http_requests_total\{route="([^"]+)",status="(\d+)"\} (\d+)$`)
	for _, m := range rq.FindAllStringSubmatch(string(b), -1) {
		requests[[2]string{m[1], m[2]}], _ = strconv.Atoi(m[3])
	}
	return abandoned, requests
}

// A129. Abandoned requests aren't errors (R73a, R72).
func TestA129AbandonedRequests(t *testing.T) {
	e, reqs := a129Setup(t)
	want := map[string]int{}
	for _, q := range reqs {
		code, lines := e.a129Do(q, func(cancel context.CancelFunc, ctx context.Context) context.Context {
			cancel() // the browser went away
			return ctx
		})
		if code != 499 {
			t.Errorf("%s: answered %d, want 499", q.name, code)
		}
		infos := 0
		for _, l := range lines {
			if l["level"] == "ERROR" {
				t.Errorf("%s: an ERROR line: %v", q.name, l)
			}
			if l["msg"] == "request abandoned" {
				infos++
				if l["level"] != "INFO" || l["route"] != q.route {
					t.Errorf("%s: %v, want INFO with route %q", q.name, l, q.route)
				}
				if txt := fmt.Sprint(l); strings.Contains(txt, "?") || !strings.HasSuffix(q.route, " "+q.path) && strings.Contains(txt, q.path) {
					t.Errorf("%s: the log line has the path or query: %v", q.name, l)
				}
			}
		}
		if infos != 1 {
			t.Errorf("%s: %d \"request abandoned\" lines, want 1", q.name, infos)
		}
		want[q.route]++
		t.Logf("A129: %s abandoned: %d, %d INFO line", q.name, code, infos)
	}
	abandoned, requests := e.a129Metrics()
	for route, n := range want {
		if abandoned[route] != n {
			t.Errorf("%s: abandoned_total %d, want %d", route, abandoned[route], n)
		}
		if requests[[2]string{route, "499"}] != n {
			t.Errorf("%s: requests_total with status 499 %d, want %d", route, requests[[2]string{route, "499"}], n)
		}
	}
	if len(abandoned) != len(want) {
		t.Errorf("abandoned_total %v, want %v", abandoned, want)
	}
	for k, n := range requests {
		if strings.HasPrefix(k[1], "5") {
			t.Errorf("requests_total: %d at %s %s", n, k[0], k[1])
		}
	}
	t.Logf("A129: abandoned_total %v", abandoned)
}

// A129: a time limit of the server's own, or a cancellation by its own
// code, isn't an abandoned request: it stays an ERROR.
func TestA129OwnLimitsStayErrors(t *testing.T) {
	e, reqs := a129Setup(t)
	for _, c := range []struct {
		name string
		end  func(context.CancelFunc, context.Context) context.Context
	}{
		{"our own deadline", func(_ context.CancelFunc, ctx context.Context) context.Context {
			ctx, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
			e.t.Cleanup(stop)
			return ctx
		}},
		{"cancelled by our own code", func(_ context.CancelFunc, ctx context.Context) context.Context {
			ctx, stop := context.WithCancel(ctx)
			stop()
			return ctx
		}},
	} {
		for _, q := range reqs {
			code, lines := e.a129Do(q, c.end)
			errs := 0
			for _, l := range lines {
				if l["level"] == "ERROR" {
					errs++
				}
				if l["msg"] == "request abandoned" {
					t.Errorf("%s, %s: logged as abandoned: %v", c.name, q.name, l)
				}
			}
			if errs == 0 || code == 499 {
				t.Errorf("%s, %s: answered %d with %d ERROR lines, want an ERROR and not 499", c.name, q.name, code, errs)
			}
			t.Logf("A129: %s, %s: %d, %d ERROR lines", c.name, q.name, code, errs)
		}
	}
	if abandoned, _ := e.a129Metrics(); len(abandoned) != 0 {
		t.Errorf("abandoned_total %v, want none", abandoned)
	}
}
