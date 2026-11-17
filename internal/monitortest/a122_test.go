package monitortest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// A122's Grafana part (R23b, R76): a real re-parse in the stack's API, over a
// few thousand made-up pages and held part way by a lock on one of them,
// shows on Requests and load as running, with its progress, pages a minute
// and time left, and in Loki as progress lines with their counts; once it's
// let go and done, the dashboard says Idle.
func TestA122ReparseInGrafana(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	const pages, held = 3000, 1500
	const capcode = "0912346"
	if _, err := s.st.Pool.Exec(ctx, `INSERT INTO messages (capcode, text, sent_at, dedup_key)
		SELECT $2, 'MADE-UP A122 PAGE ' || g, timestamptz '2020-01-01 00:00:00+00' + g * interval '1 minute', sha256(('a122 ' || g)::bytea)
		FROM generate_series(1, $1::int) g`, pages, capcode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.st.Pool.Exec(ctx, `DELETE FROM messages WHERE capcode = $1`, capcode) })
	var heldID int64
	if err := s.st.Pool.QueryRow(ctx, `SELECT id FROM messages WHERE capcode = $1 ORDER BY id OFFSET $2 LIMIT 1`, capcode, held-1).Scan(&heldID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM messages WHERE id = $1 FOR UPDATE`, heldID); err != nil {
		t.Fatal(err)
	}
	if err := s.st.RequestReparse(ctx, db.RequestReparseParams{CapcodePattern: capcode, Reason: "alias added"}); err != nil {
		t.Fatal(err)
	}
	one := func(promql string) (float64, bool) {
		v, ok := s.values(t, promql, "")[""]
		return v, ok
	}
	// Prometheus has it running, stopped at the held page.
	if !waitFor(2*time.Minute, func() bool {
		r, _ := one(`max(pagerarchive_reparse_running{job="pagerarchive"})`)
		d, _ := one(`max(pagerarchive_reparse_run_messages_done{job="pagerarchive"})`)
		n, _ := one(`max(pagerarchive_reparse_run_messages{job="pagerarchive"})`)
		return r == 1 && d == held-1 && n == pages
	}) {
		r, _ := one(`max(pagerarchive_reparse_running{job="pagerarchive"})`)
		d, _ := one(`max(pagerarchive_reparse_run_messages_done{job="pagerarchive"})`)
		n, _ := one(`max(pagerarchive_reparse_run_messages{job="pagerarchive"})`)
		t.Fatalf("Prometheus didn't see the re-parse held at %d of %d within 2 minutes: running %v, %v of %v", held-1, pages, r, d, n)
	}
	t.Logf("A122: Prometheus has the re-parse running, %d of %d done", held-1, pages)

	// The dashboard's row, as Grafana runs its panels.
	wantRunning := map[string]func(float64) bool{
		"Re-parse":             func(v float64) bool { return v == 1 },
		"Re-parse progress":    func(v float64) bool { return v > 49.9 && v < 50 }, // 1,499 of 3,000
		"Re-parsed per minute": func(v float64) bool { return v > 0 },
		"Re-parse time left":   func(v float64) bool { return v > 0 },
	}
	var got map[string][]float64
	ok := waitFor(time.Minute, func() bool {
		got = s.panelLasts(t, "pa-requests")
		for title, good := range wantRunning {
			if v := got[title]; len(v) != 1 || !good(v[0]) {
				return false
			}
		}
		// The graph's last point is a step (15 s) behind the stats'.
		g := got["Re-parse: messages to do and done"]
		return len(g) == 2 && g[0] == pages && g[1] == held-1
	})
	if !ok {
		t.Errorf("while it runs, the dashboard's re-parse panels say %v", pick(got, wantRunning))
	} else {
		t.Logf("A122: while it runs: %v", pick(got, wantRunning))
	}
	if v := got["Re-parse: messages to do and done"]; len(v) != 2 || v[0] != pages || v[1] != held-1 {
		t.Errorf("the graph's last values are %v, want %d to do and %d done", v, pages, held-1)
	}

	// Loki has its progress lines, with their counts (every 30 s).
	q := fmt.Sprintf(`{service_name="pagerarchive"} |= "re-parse progress" | done="%d" | total="%d"`, held-1, pages)
	var lines int
	if !waitFor(90*time.Second, func() bool { lines = s.lokiCount(t, q); return lines > 0 }) {
		t.Errorf("Loki has no progress line for %d of %d (%s)", held-1, pages, q)
	} else {
		t.Logf("A122: Loki has %d progress lines with done=%d total=%d", lines, held-1, pages)
	}
	if n := s.lokiCount(t, `{service_name="pagerarchive"} |= "re-parse started" | reasons="alias_added" | total="3000"`); n == 0 {
		t.Error(`Loki has no "re-parse started" line with its reason and total`)
	}

	// Let go: it finishes, and the row says Idle.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if !waitFor(2*time.Minute, func() bool {
		r, _ := one(`max(pagerarchive_reparse_running{job="pagerarchive"})`)
		d, _ := one(`max(pagerarchive_reparse_run_messages_done{job="pagerarchive"})`)
		return r == 0 && d == pages
	}) {
		t.Fatal("Prometheus didn't see the re-parse finish within 2 minutes of letting it go")
	}
	ok = waitFor(time.Minute, func() bool {
		got = s.panelLasts(t, "pa-requests")
		if v := got["Re-parse"]; len(v) != 1 || v[0] != 0 {
			return false
		}
		// No value: the panels show their "Idle".
		return len(got["Re-parse progress"])+len(got["Re-parsed per minute"])+len(got["Re-parse time left"]) == 0
	})
	if !ok {
		t.Errorf("after it, the dashboard's re-parse panels say %v, want Re-parse 0 and the others no value (Idle)", pick(got, wantRunning))
	} else {
		t.Log("A122: after it: Re-parse 0 (Idle), and progress, per minute and time left no value (Idle)")
	}
	if n := s.lokiCount(t, `{service_name="pagerarchive"} |= "re-parsed messages" | count="3000"`); n == 0 {
		t.Error(`Loki has no "re-parsed messages" line with its count`)
	}
	if n := s.lokiCount(t, `{service_name="pagerarchive"} |= "MADE-UP"`); n > 0 {
		t.Errorf("Loki has %d lines with the made-up pages' text", n)
	}
}

func pick(got map[string][]float64, want map[string]func(float64) bool) map[string][]float64 {
	out := map[string][]float64{}
	for k := range want {
		out[k] = got[k]
	}
	return out
}

// panelLasts runs each of a dashboard's panels' queries as Grafana does and
// returns, by panel title, the last value of each series, in the panel's
// query order. A query with no data gives none.
func (s *stack) panelLasts(t *testing.T, dashboard string) map[string][]float64 {
	t.Helper()
	var d struct {
		Dashboard struct {
			Panels []struct {
				Title   string            `json:"title"`
				Targets []json.RawMessage `json:"targets"`
			} `json:"panels"`
		} `json:"dashboard"`
	}
	s.grafanaGet(t, "/api/dashboards/uid/"+dashboard, &d)
	out := map[string][]float64{}
	now := time.Now()
	for _, p := range d.Dashboard.Panels {
		for _, raw := range p.Targets {
			var q map[string]any
			if err := json.Unmarshal(raw, &q); err != nil {
				t.Fatal(err)
			}
			if ds, _ := q["datasource"].(map[string]any); ds["uid"] != "prometheus" {
				continue
			}
			for _, f := range s.dsQuery(t, q, now.Add(-time.Hour), now) {
				if len(f.Data.Values) < 2 || len(f.Data.Values[1]) == 0 {
					continue
				}
				col := f.Data.Values[1]
				if v, ok := col[len(col)-1].(float64); ok {
					out[p.Title] = append(out[p.Title], v)
				}
			}
		}
	}
	return out
}

// lokiCount is how many lines Loki has in the last hour for a LogQL query.
func (s *stack) lokiCount(t *testing.T, q string) int {
	t.Helper()
	var logs struct {
		Data struct {
			Result []struct {
				Values [][]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	end := time.Now().Add(time.Minute)
	s.grafanaGet(t, fmt.Sprintf("/api/datasources/proxy/uid/loki/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=5000",
		url.QueryEscape(q), end.Add(-time.Hour).UnixNano(), end.UnixNano()), &logs)
	n := 0
	for _, r := range logs.Data.Result {
		n += len(r.Values)
	}
	return n
}
