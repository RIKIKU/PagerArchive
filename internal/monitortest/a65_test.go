// Package monitortest checks the example monitoring stack (deploy/monitoring)
// against a running PagerArchive (A65). scripts/monitoring-checks.sh starts
// both and runs it; without PA_MON_GRAFANA_URL it skips.
package monitortest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/store"
)

// stack is the running stacks, from the environment the script sets.
type stack struct {
	api, grafana, grafanaPassword string
	st                            *store.Store
	stopAPI, startAPI             string
}

func newStack(t *testing.T) *stack {
	t.Helper()
	s := &stack{
		api: os.Getenv("PA_MON_API_URL"), grafana: os.Getenv("PA_MON_GRAFANA_URL"), grafanaPassword: os.Getenv("PA_MON_GRAFANA_PASSWORD"),
		stopAPI: os.Getenv("PA_MON_STOP_API"), startAPI: os.Getenv("PA_MON_START_API"),
	}
	if s.grafana == "" {
		if os.Getenv("PA_MON_REQUIRE") == "1" {
			t.Fatal("PA_MON_GRAFANA_URL is not set")
		}
		t.Skip("PA_MON_GRAFANA_URL is not set: run scripts/monitoring-checks.sh")
	}
	st, err := store.Open(context.Background(), os.Getenv("PA_MON_DB_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	s.st = st
	return s
}

// notification is what Grafana's webhook contact point posts.
type notification struct {
	Status string `json:"status"`
	Alerts []struct {
		Status string            `json:"status"`
		Labels map[string]string `json:"labels"`
	} `json:"alerts"`
	at time.Time
}

// webhook is the test contact point's receiver.
type webhook struct {
	mu  sync.Mutex
	got []notification
}

func (w *webhook) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	var n notification
	if err := json.NewDecoder(r.Body).Decode(&n); err == nil {
		n.at = time.Now()
		w.mu.Lock()
		w.got = append(w.got, n)
		w.mu.Unlock()
	}
	rw.WriteHeader(http.StatusOK)
}

// find is the first alert since since with this name, status and, if
// client isn't "", client label, and when it came.
func (w *webhook) find(since time.Time, name, status, client string) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, n := range w.got {
		if n.at.Before(since) {
			continue
		}
		for _, a := range n.Alerts {
			if a.Labels["alertname"] == name && a.Status == status && (client == "" || a.Labels["client"] == client) {
				return n.at, true
			}
		}
	}
	return time.Time{}, false
}

// firingFor lists the clients a "Client not receiving" alert fired for.
func (w *webhook) firingFor(since time.Time) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, n := range w.got {
		if n.at.Before(since) {
			continue
		}
		for _, a := range n.Alerts {
			if a.Labels["alertname"] == "Client not receiving" && a.Status == "firing" {
				out = append(out, a.Labels["client"])
			}
		}
	}
	return out
}

func waitFor(d time.Duration, cond func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return cond()
}

// post sends a page as client id with key.
func (s *stack) post(t *testing.T, id, key, text string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"client_id": id, "capcode": "0100001", "text": text, "received_at": time.Now().UTC(), "baud": 1200})
	req, _ := http.NewRequest(http.MethodPost, s.api+"/api/v1/pages", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post as %s: %d", id, resp.StatusCode)
	}
}

// query asks Prometheus, through Grafana as the admin, for an instant
// vector, and returns how many series it has.
func (s *stack) query(t *testing.T, promql string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.grafana+"/api/datasources/proxy/uid/prometheus/api/v1/query?query="+url.QueryEscape(promql), nil)
	req.SetBasicAuth("admin", s.grafanaPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Result []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return len(out.Data.Result)
}

func (s *stack) sh(t *testing.T, cmd string) {
	t.Helper()
	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
}

// A65. Told when a client stops: with the example stack scraping
// PagerArchive and two clients, moving one client's last message to 31
// minutes ago (the test clock A32 uses: its last-seen time in the
// database) fires "Client not receiving" for that client only within 2
// minutes; it resolves when the client posts again; stopping the API fires
// "API unreachable", which resolves when it's back.
func TestA65ToldWhenAClientStops(t *testing.T) {
	s := newStack(t)
	hook := &webhook{}
	l, err := net.Listen("tcp", os.Getenv("PA_MON_WEBHOOK_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, hook)
	defer l.Close()

	run := time.Now().Format("150405")
	a, b := "mon-a-"+run, "mon-b-"+run
	keys := map[string]string{}
	for _, id := range []string{a, b} {
		key, err := s.st.AddClient(context.Background(), id, "A65 "+id)
		if err != nil {
			t.Fatal(err)
		}
		keys[id] = key
		s.post(t, id, key, "QDA65 "+id+" is receiving")
	}
	// Both scraped as receiving.
	if !waitFor(90*time.Second, func() bool {
		return s.query(t, fmt.Sprintf(`pagerarchive_client_receiving{client=~"%s|%s"} == 1`, a, b)) == 2
	}) {
		t.Fatal("Prometheus never had both clients receiving: is Alloy scraping the API?")
	}
	t.Logf("A65: clients %s and %s scraped as receiving", a, b)

	// Client A's last message moves to 31 minutes ago.
	moved := time.Now()
	if _, err := s.st.Pool.Exec(context.Background(), `UPDATE clients SET last_seen_at = now() - interval '31 minutes' WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	firedAt, fired := time.Time{}, false
	if !waitFor(2*time.Minute, func() bool { firedAt, fired = hook.find(moved, "Client not receiving", "firing", a); return fired }) {
		t.Fatalf("no \"Client not receiving\" alert for %s within 2 minutes; got %d notifications", a, len(hook.got))
	}
	t.Logf("A65: \"Client not receiving\" fired for %s %v after its last message moved to 31 minutes ago", a, firedAt.Sub(moved).Round(time.Second))
	// For that client only (B's went on being heard). Give B's a chance to
	// show up wrongly: one more evaluation.
	time.Sleep(35 * time.Second)
	for _, c := range hook.firingFor(moved) {
		if c != a {
			t.Errorf("\"Client not receiving\" fired for %s too", c)
		}
	}

	// A posts again: the alert resolves.
	posted := time.Now()
	s.post(t, a, keys[a], "QDA65 "+a+" is back")
	resolvedAt, resolved := time.Time{}, false
	if !waitFor(3*time.Minute, func() bool {
		resolvedAt, resolved = hook.find(posted, "Client not receiving", "resolved", a)
		return resolved
	}) {
		t.Errorf("\"Client not receiving\" for %s didn't resolve within 3 minutes of it posting", a)
	} else {
		t.Logf("A65: it resolved %v after %s posted again", resolvedAt.Sub(posted).Round(time.Second), a)
	}

	// The API stops: "API unreachable" fires; it comes back: it resolves.
	stopped := time.Now()
	s.sh(t, s.stopAPI)
	downAt, down := time.Time{}, false
	if !waitFor(3*time.Minute, func() bool { downAt, down = hook.find(stopped, "API unreachable", "firing", ""); return down }) {
		t.Errorf("\"API unreachable\" didn't fire within 3 minutes of the API stopping")
	} else {
		t.Logf("A65: \"API unreachable\" fired %v after the API stopped", downAt.Sub(stopped).Round(time.Second))
	}
	started := time.Now()
	s.sh(t, s.startAPI)
	upAt, up := time.Time{}, false
	if !waitFor(3*time.Minute, func() bool { upAt, up = hook.find(started, "API unreachable", "resolved", ""); return up }) {
		t.Errorf("\"API unreachable\" didn't resolve within 3 minutes of the API starting")
	} else {
		t.Logf("A65: it resolved %v after the API started again", upAt.Sub(started).Round(time.Second))
	}
	if strings.TrimSpace(os.Getenv("PA_MON_KEEP_CLIENTS")) == "" {
		for _, id := range []string{a, b} {
			_, _ = s.st.RevokeClient(context.Background(), id)
		}
	}
}
