package monitortest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// panelResult is what a dashboard panel's queries gave, as Grafana runs them
// for the panel: the errors, and how many series or rows had a value.
type panelResult struct {
	title  string
	errors []string
	values int
}

// dashboardPanels runs every query of every panel of a dashboard as Grafana
// does for the panel (time range now-6h to now), as panelValues does for one.
func (s *stack) dashboardPanels(t *testing.T, dashboard string) []panelResult {
	t.Helper()
	var d struct {
		Dashboard struct {
			Title  string `json:"title"`
			Panels []struct {
				Title   string            `json:"title"`
				Type    string            `json:"type"`
				Targets []json.RawMessage `json:"targets"`
			} `json:"panels"`
		} `json:"dashboard"`
	}
	s.grafanaGet(t, "/api/dashboards/uid/"+dashboard, &d)
	var out []panelResult
	for _, p := range d.Dashboard.Panels {
		if len(p.Targets) == 0 {
			continue
		}
		r := panelResult{title: p.Title}
		queries := make([]map[string]any, len(p.Targets))
		for i, raw := range p.Targets {
			if err := json.Unmarshal(raw, &queries[i]); err != nil {
				t.Fatal(err)
			}
			queries[i]["intervalMs"] = 15000
			queries[i]["maxDataPoints"] = 1000
		}
		body, _ := json.Marshal(map[string]any{"queries": queries, "from": "now-6h", "to": "now"})
		req, _ := http.NewRequest(http.MethodPost, s.grafana+"/api/ds/query", bytes.NewReader(body))
		req.SetBasicAuth("admin", s.grafanaPassword)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var res struct {
			Results map[string]struct {
				Error  string `json:"error"`
				Frames []struct {
					Data struct {
						Values [][]any `json:"values"`
					} `json:"data"`
				} `json:"frames"`
			} `json:"results"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			r.errors = append(r.errors, fmt.Sprintf("%d %s", resp.StatusCode, b))
		}
		for ref, q := range res.Results {
			if q.Error != "" {
				r.errors = append(r.errors, ref+": "+q.Error)
			}
			for _, f := range q.Frames {
				if len(f.Data.Values) > 0 && len(f.Data.Values[len(f.Data.Values)-1]) > 0 {
					r.values++
				}
			}
		}
		out = append(out, r)
	}
	return out
}

// signInWrongly makes failed sign-ins in Authentik, as a browser does,
// through its flow executor: the made-up address, then wrong passwords.
func signInWrongly(t *testing.T, authentik, address string, tries int) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	u := strings.TrimRight(authentik, "/") + "/api/v3/flows/executor/pagerarchive-authentication/?query="
	base, _ := url.Parse(authentik)
	step := func(body map[string]any) string {
		t.Helper()
		var req *http.Request
		if body == nil {
			req, _ = http.NewRequest(http.MethodGet, u, nil)
		} else {
			b, _ := json.Marshal(body)
			req, _ = http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			for _, ck := range jar.Cookies(base) {
				if ck.Name == "authentik_csrf" {
					req.Header.Set("X-authentik-CSRF", ck.Value)
				}
			}
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var ch struct {
			Component string `json:"component"`
		}
		b, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(b, &ch); err != nil {
			t.Fatalf("Authentik's flow executor: %d %.200s", resp.StatusCode, b)
		}
		return ch.Component
	}
	comp := step(nil)
	if comp != "ak-stage-identification" {
		t.Fatalf("Authentik's sign-in starts at %q, want the identification stage", comp)
	}
	comp = step(map[string]any{"component": comp, "uid_field": address})
	for i := 0; i < tries; i++ {
		if comp != "ak-stage-password" {
			t.Fatalf("after %d wrong passwords for %s: at %q, want the password stage", i, address, comp)
		}
		comp = step(map[string]any{"component": comp, "password": fmt.Sprintf("wrong password %d", i)})
	}
}

// A115 (R89, R89a, R89b): the Failures and Authentik dashboards load, every
// query without an error, and show the stack's own made-up traffic; the
// stack scrapes Authentik's server and worker without its metrics being
// reachable through Authentik's published port; failed sign-ins fire
// "Failed sign-ins" and name nobody in what the stack keeps; Authentik's
// server stopping fires "Authentik down" for it, which resolves when it's
// back; and "Failed sign-ins" resolves once the 5 minutes have passed.
func TestA115FailuresAndAuthentik(t *testing.T) {
	s := newStack(t)
	ak := os.Getenv("PA_MON_AUTHENTIK_URL")
	if ak == "" {
		t.Fatal("PA_MON_AUTHENTIK_URL is not set: run scripts/monitoring-checks.sh")
	}
	hook := &webhook{}
	l, err := net.Listen("tcp", os.Getenv("PA_MON_WEBHOOK_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, hook)
	defer l.Close()

	// Both parts scraped, over the monitoring-authentik network.
	if !waitFor(2*time.Minute, func() bool { return s.query(t, `up{job="authentik"} == 1`) == 2 }) {
		t.Fatalf("Prometheus never had Authentik's server and worker up: %v", s.values(t, `up{job="authentik"}`, "instance"))
	}
	t.Logf("A115: Authentik scraped: %v", s.values(t, `up{job="authentik"}`, "instance"))
	// Authentik's published port, where the reverse proxy sends the internet,
	// has no metrics (D8): only the network Alloy is on does.
	for _, path := range []string{"/metrics", "/-/metrics/"} {
		resp, err := http.Get(strings.TrimRight(ak, "/") + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || strings.Contains(string(b), "django_http") {
			t.Errorf("Authentik's published port answers %s: %d", path, resp.StatusCode)
		}
	}

	// A page refused: a client key that isn't one.
	body, _ := json.Marshal(map[string]any{"client_id": "a115-nobody", "capcode": "0100001", "text": "QDA115 refused", "received_at": time.Now().UTC(), "baud": 1200})
	req, _ := http.NewRequest(http.MethodPost, s.api+"/api/v1/pages", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer not-a-client-key")
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		t.Logf("A115: a page with a wrong client key: %d", resp.StatusCode)
	}

	// Failed sign-ins, as someone guessing: 12 wrong passwords for made-up
	// addresses.
	run := time.Now().Format("150405")
	guessed := time.Now()
	var addresses []string
	for i := 0; i < 3; i++ {
		a := fmt.Sprintf("guess-%s-%d@example.test", run, i)
		addresses = append(addresses, a)
		signInWrongly(t, ak, a, 4)
	}
	if !waitFor(3*time.Minute, func() bool {
		_, ok := hook.find(guessed, "Failed sign-ins", "firing", "")
		return ok
	}) {
		t.Fatalf("\"Failed sign-ins\" didn't fire within 3 minutes of 12 failed sign-ins; counted: %v",
			s.values(t, `pagerarchive_authentik_events_5m{action="login_failed"}`, "reason"))
	}
	at, _ := hook.find(guessed, "Failed sign-ins", "firing", "")
	t.Logf("A115: \"Failed sign-ins\" fired %s after 12 failed sign-ins; counted by reason: %v", at.Sub(guessed).Round(time.Second),
		s.values(t, `pagerarchive_authentik_events_5m{action="login_failed"}`, "reason"))

	// Nobody named in what Prometheus keeps, for Authentik or the API.
	for _, match := range []string{`{job="authentik"}`, `{__name__=~"pagerarchive_authentik.*|pagerarchive_accounts_locked"}`} {
		var series struct {
			Data []map[string]string `json:"data"`
		}
		s.grafanaGet(t, "/api/datasources/proxy/uid/prometheus/api/v1/series?match[]="+url.QueryEscape(match)+
			fmt.Sprintf("&start=%d", time.Now().Add(-time.Hour).Unix()), &series)
		if len(series.Data) == 0 {
			t.Errorf("no series for %s, so nothing checked", match)
		}
		b, _ := json.Marshal(series.Data)
		for _, banned := range append([]string{"example.test", "akadmin", "binding_target_name", "mapping_name"}, addresses...) {
			if strings.Contains(string(b), banned) {
				t.Errorf("Prometheus' series for %s contain %q", match, banned)
			}
		}
		t.Logf("A115: %d series for %s, naming nobody", len(series.Data), match)
	}

	// The two dashboards: every query without an error, and the panels the
	// made-up traffic feeds have values.
	want := map[string][]string{
		"pa-authentik": {"Up", "Workers", "Signed in, 5 min", "Failed sign-ins, 5 min", "Locked accounts",
			"Sign-ins (each minute, last 5 min)", "Errors recorded (each minute, last 5 min)",
			"Requests per second, by status", "Slowest 10% of requests, by view", "Flows started (per minute)", "Worker tasks (per minute)",
			"Tasks waiting", "Database connections and errors (per minute)"},
		"pa-failures": {"Server errors, 1 h", "Refused pages, 1 h", "Not receiving", "Failed sends, 1 h",
			"Failed deliveries, 1 h", "Locked accounts", "Nightly run", "Backups", "Upgrades", "Authentik down",
			"Failed sign-ins, 5 min", "Authentik errors, 5 min", "Radio stalled", "Radio not decoding", "Pages refused (per minute)",
			"Authentik: failed sign-ins and errors", "Errors and warnings in the logs, by message",
			"The error and warning lines (click one for its trace)"},
	}
	for dash, titles := range want {
		got := map[string]panelResult{}
		// A graph's points are a step apart: give new series a moment.
		waitFor(time.Minute, func() bool {
			for _, r := range s.dashboardPanels(t, dash) {
				got[r.title] = r
			}
			for _, title := range titles {
				if got[title].values == 0 {
					return false
				}
			}
			return true
		})
		for title, r := range got {
			if len(r.errors) > 0 {
				t.Errorf("%s, %q: %s", dash, title, strings.Join(r.errors, "; "))
			}
		}
		for _, title := range titles {
			if r, ok := got[title]; !ok {
				t.Errorf("%s has no panel %q", dash, title)
			} else if r.values == 0 {
				t.Errorf("%s, %q: no data", dash, title)
			}
		}
		t.Logf("A115: %s: %d panels with queries, none failing; %d checked for data", dash, len(got), len(titles))
	}

	// Authentik's server stops: "Authentik down" fires for it; it's back: it
	// resolves.
	stopped := time.Now()
	s.sh(t, os.Getenv("PA_MON_STOP_AUTHENTIK"))
	instanceAlert := func(since time.Time, status string) (time.Time, bool) {
		hook.mu.Lock()
		defer hook.mu.Unlock()
		for _, n := range hook.got {
			if n.at.Before(since) {
				continue
			}
			for _, a := range n.Alerts {
				if a.Labels["alertname"] == "Authentik down" && a.Status == status && a.Labels["instance"] == "server" {
					return n.at, true
				}
			}
		}
		return time.Time{}, false
	}
	if !waitFor(3*time.Minute, func() bool { _, ok := instanceAlert(stopped, "firing"); return ok }) {
		s.sh(t, os.Getenv("PA_MON_START_AUTHENTIK"))
		t.Fatal("\"Authentik down\" didn't fire for the server within 3 minutes of it stopping")
	}
	at, _ = instanceAlert(stopped, "firing")
	t.Logf("A115: \"Authentik down\" fired for the server %s after it stopped", at.Sub(stopped).Round(time.Second))
	for _, a := range hook.alerts(stopped, "firing") {
		if a.name == "Authentik down" {
			continue
		}
		if a.name != "Failed sign-ins" {
			t.Errorf("%q fired while Authentik's server was stopped", a.name)
		}
	}
	started := time.Now()
	s.sh(t, os.Getenv("PA_MON_START_AUTHENTIK"))
	if !waitFor(4*time.Minute, func() bool { _, ok := instanceAlert(started, "resolved"); return ok }) {
		t.Error("\"Authentik down\" didn't resolve within 4 minutes of the server starting")
	} else {
		at, _ = instanceAlert(started, "resolved")
		t.Logf("A115: it resolved %s after the server started", at.Sub(started).Round(time.Second))
	}

	// The failed sign-ins leave the 5 minutes: "Failed sign-ins" resolves.
	if !waitFor(time.Until(guessed.Add(9*time.Minute)), func() bool {
		_, ok := hook.find(guessed, "Failed sign-ins", "resolved", "")
		return ok
	}) {
		t.Error("\"Failed sign-ins\" didn't resolve within 9 minutes of the sign-ins")
	} else {
		at, _ = hook.find(guessed, "Failed sign-ins", "resolved", "")
		t.Logf("A115: \"Failed sign-ins\" resolved %s after the sign-ins", at.Sub(guessed).Round(time.Second))
	}
}
