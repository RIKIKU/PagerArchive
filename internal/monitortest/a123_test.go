package monitortest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// A123's stack part (R23c, R76): the stack's own API, killed (SIGKILL) part
// way through a re-parse of a few thousand made-up pages, carries it on from
// its last batch when it starts again; a pause written while it was down
// holds across the start, and shows as Paused in Prometheus and on Requests
// and load; carried on, it does only the pages it hadn't, and Loki has its
// "re-parse carried on" line.
func TestA123ReparseKilledAndCarriedOn(t *testing.T) {
	s := newStack(t)
	kill := os.Getenv("PA_MON_KILL_API")
	if kill == "" || s.startAPI == "" {
		t.Skip("PA_MON_KILL_API and PA_MON_START_API are not set: run scripts/monitoring-checks.sh")
	}
	ctx := context.Background()
	const pages, held = 3000, 1500
	const capcode = "0912348"
	if _, err := s.st.Pool.Exec(ctx, `INSERT INTO messages (capcode, text, sent_at, dedup_key)
		SELECT $2, 'MADE-UP A123 PAGE ' || g, timestamptz '2020-01-01 00:00:00+00' + g * interval '1 minute', sha256(('a123 ' || g)::bytea)
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
	one := func(promql string) float64 {
		v, ok := s.values(t, promql, "")[""]
		if !ok {
			return -1
		}
		return v
	}
	metrics := func() (state, done, total float64) {
		return one(`max(pagerarchive_reparse_state{job="pagerarchive"})`), one(`max(pagerarchive_reparse_run_messages_done{job="pagerarchive"})`),
			one(`max(pagerarchive_reparse_run_messages{job="pagerarchive"})`)
	}
	if !waitFor(2*time.Minute, func() bool { st, d, n := metrics(); return st == 1 && d == held-1 && n == pages }) {
		st, d, n := metrics()
		t.Fatalf("Prometheus didn't see the re-parse running, held at %d of %d: state %v, %v of %v", held-1, pages, st, d, n)
	}
	runState := func() (state string, done int64) {
		_ = s.st.Pool.QueryRow(ctx, `SELECT state, done FROM reparse_runs`).Scan(&state, &done)
		return
	}

	// Killed where it is: the batch in hand is rolled back.
	s.sh(t, kill)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if !waitFor(time.Minute, func() bool { st, d := runState(); return st == "running" && d == 1000 }) {
		st, d := runState()
		t.Fatalf("after the kill, the run is %s with %d done, want running with 1,000 (its last batch)", st, d)
	}
	// An admin's pause, as the API writes it, while the API is down.
	if _, err := s.st.Pool.Exec(ctx, `UPDATE reparse_runs SET state = 'paused', changed_at = now(); SELECT pg_notify('pagerarchive_reparse', '')`); err != nil {
		t.Fatal(err)
	}
	t.Log("A123: the API killed with 1,499 of 3,000 done in memory and 1,000 kept; paused while it was down")

	// Started again: still paused, at 1,000 of 3,000.
	s.sh(t, s.startAPI)
	if !waitFor(3*time.Minute, func() bool { st, d, n := metrics(); return st == 2 && d == 1000 && n == pages }) {
		st, d, n := metrics()
		t.Fatalf("after the start, Prometheus has state %v, %v of %v; want 2 (paused), 1000 of %d", st, d, n, pages)
	}
	var got map[string][]float64
	if !waitFor(time.Minute, func() bool {
		got = s.panelLasts(t, "pa-requests")
		p := got["Re-parse progress"]
		return len(got["Re-parse"]) == 1 && got["Re-parse"][0] == 2 && len(p) == 1 && p[0] > 33.3 && p[0] < 33.4 &&
			len(got["Re-parsed per minute"])+len(got["Re-parse time left"]) == 0
	}) {
		t.Errorf("paused, the dashboard's re-parse panels say %v; want Re-parse 2 (Paused), progress 33.3 %%, and no rate or time left", got)
	} else {
		t.Logf("A123: paused after the restart: Re-parse %v (Paused), progress %v %%", got["Re-parse"], got["Re-parse progress"])
	}
	if st, d := runState(); st != "paused" || d != 1000 {
		t.Errorf("paused after the restart, the run is %s with %d done", st, d)
	}

	// Resumed: it does the 2,000 it hadn't, and finishes.
	if _, err := s.st.Pool.Exec(ctx, `UPDATE reparse_runs SET state = 'running', changed_at = now(); SELECT pg_notify('pagerarchive_reparse', '')`); err != nil {
		t.Fatal(err)
	}
	if !waitFor(2*time.Minute, func() bool { st, d, n := metrics(); return st == 0 && d == pages && n == pages }) {
		st, d, n := metrics()
		t.Fatalf("resumed, Prometheus has state %v, %v of %v; want 0 (idle), %d of %d", st, d, n, pages, pages)
	}
	if !waitFor(90*time.Second, func() bool {
		return s.lokiCount(t, `{service_name="pagerarchive"} |= "re-parse carried on" | done="1000" | total="3000"`) > 0 &&
			s.lokiCount(t, `{service_name="pagerarchive"} |= "re-parsed messages" | count="2000"`) > 0
	}) {
		t.Error(`Loki has no "re-parse carried on" line at 1,000 of 3,000, or no finished line with the 2,000 it did after`)
	} else {
		t.Log("A123: resumed from 1,000: Loki has the carried-on line and the finished line with count 2000")
	}
	if n := s.lokiCount(t, `{service_name="pagerarchive"} |= "MADE-UP"`); n > 0 {
		t.Errorf("Loki has %d lines with the made-up pages' text", n)
	}
}
