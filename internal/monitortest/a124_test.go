package monitortest

import (
	"context"
	"os"
	"testing"
	"time"
)

// A124's stack part (R45a, R76): the stack's own API, killed (SIGKILL) part
// way through a retroactive redaction run over a few thousand made-up
// pages, held right after a page's new text is written and before its
// re-parse, leaves nothing half done and carries the run on from its last
// batch when it starts again; a pause written while it was down holds
// across the start, and shows as Paused in Prometheus and on Requests and
// load; carried on, it finishes, and Loki has its "retroactive redaction
// carried on" line.
func TestA124RedactionKilledAndCarriedOn(t *testing.T) {
	s := newStack(t)
	kill := os.Getenv("PA_MON_KILL_API")
	if kill == "" || s.startAPI == "" {
		t.Skip("PA_MON_KILL_API and PA_MON_START_API are not set: run scripts/monitoring-checks.sh")
	}
	ctx := context.Background()
	const pages, held = 3000, 1500
	const capcode = "0912350"
	const term = "0412 345 678" // made up; the example's mobile-number rule matches it
	// As if stored before a rule matching the term existed: the text and the
	// fields parsed from it hold it, on every other page.
	if _, err := s.st.Pool.Exec(ctx, `INSERT INTO messages (capcode, text, description, sent_at, dedup_key)
		SELECT $2, t, t, timestamptz '2020-01-01 00:00:00+00' + g * interval '1 minute', sha256(('a124 ' || g)::bytea)
		FROM generate_series(1, $1::int) g,
		LATERAL (SELECT 'MADE-UP A124 PAGE ' || g || CASE WHEN g % 2 = 0 THEN ' CALL ' || $3::text ELSE '' END AS t) x`,
		pages, capcode, term); err != nil {
		t.Fatal(err)
	}
	var first, heldID int64
	if err := s.st.Pool.QueryRow(ctx, `SELECT min(id) FROM messages WHERE capcode = $1`, capcode).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := s.st.Pool.QueryRow(ctx, `SELECT id FROM messages WHERE capcode = $1 ORDER BY id OFFSET $2 LIMIT 1`, capcode, held-1).Scan(&heldID); err != nil {
		t.Fatal(err)
	}
	// A lock that holds the batch right after the held page's new text is
	// written, before its re-parse.
	if _, err := s.st.Pool.Exec(ctx, `
		CREATE TABLE a124_held (id bigint);
		CREATE FUNCTION a124_hold() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN
			IF NEW.id IN (SELECT id FROM a124_held) THEN PERFORM pg_advisory_xact_lock(124124); END IF;
			RETURN NULL;
		END $$;
		CREATE TRIGGER a124_hold AFTER UPDATE OF text ON messages FOR EACH ROW EXECUTE FUNCTION a124_hold()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.st.Pool.Exec(ctx, `DROP TRIGGER IF EXISTS a124_hold ON messages; DROP FUNCTION IF EXISTS a124_hold(); DROP TABLE IF EXISTS a124_held`)
		_, _ = s.st.Pool.Exec(ctx, `DELETE FROM messages WHERE capcode = $1`, capcode)
	})
	if _, err := s.st.Pool.Exec(ctx, `INSERT INTO a124_held VALUES ($1)`, heldID); err != nil {
		t.Fatal(err)
	}
	lock, err := s.st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			_, _ = lock.Exec(ctx, `SELECT pg_advisory_unlock(124124)`)
			lock.Release()
			released = true
		}
	}
	defer release()
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(124124)`); err != nil {
		t.Fatal(err)
	}
	// A run as the nightly run starts one, with the API's own rules (saved as
	// applied when it started), placed just before the made-up pages, so
	// it checks only those and anything stored after them.
	var placed int64
	if _, err := s.st.Pool.Exec(ctx, `DELETE FROM redaction_runs`); err != nil {
		t.Fatal(err)
	}
	if err := s.st.Pool.QueryRow(ctx, `INSERT INTO redaction_runs (state, reason, rules_hash, total, after_id)
		SELECT 'running', 'rules_changed', value, $1::bigint, $2::bigint FROM settings WHERE key = 'redaction_hash'
		RETURNING id`, pages, first-1).Scan(&placed); err != nil {
		t.Fatalf("a run with the API's rules (saved as applied when it started): %v", err)
	}
	if _, err := s.st.Pool.Exec(ctx, `SELECT pg_notify('pagerarchive_redaction', '')`); err != nil {
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
		return one(`max(pagerarchive_redaction_state{job="pagerarchive"})`), one(`max(pagerarchive_redaction_run_messages_done{job="pagerarchive"})`),
			one(`max(pagerarchive_redaction_run_messages{job="pagerarchive"})`)
	}
	if !waitFor(2*time.Minute, func() bool { st, d, n := metrics(); return st == 1 && d == held-1 && n == pages }) {
		st, d, n := metrics()
		t.Fatalf("Prometheus didn't see retroactive redaction running, held at %d of %d: state %v, %v of %v", held-1, pages, st, d, n)
	}
	runState := func() (state string, done int64) {
		_ = s.st.Pool.QueryRow(ctx, `SELECT state, done FROM redaction_runs`).Scan(&state, &done)
		return
	}
	halfDone := func() int64 {
		var n int64
		_ = s.st.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE capcode = $1 AND strpos(text, $2) = 0 AND strpos(coalesce(description, ''), $2) > 0`,
			capcode, term).Scan(&n)
		return n
	}

	// Killed where it is: the batch in hand is rolled back, the held page's
	// new text with it.
	s.sh(t, kill)
	release()
	if !waitFor(time.Minute, func() bool { st, d := runState(); return st == "running" && d == 1000 }) {
		st, d := runState()
		t.Fatalf("after the kill, the run is %s with %d checked, want running with 1,000 (its last batch)", st, d)
	}
	if n := halfDone(); n != 0 {
		t.Errorf("after the kill, %d pages have new text beside a parsed field that still holds the term", n)
	}
	// An admin's pause, as the API writes it, while the API is down.
	if _, err := s.st.Pool.Exec(ctx, `UPDATE redaction_runs SET state = 'paused', changed_at = now(); SELECT pg_notify('pagerarchive_redaction', '')`); err != nil {
		t.Fatal(err)
	}
	t.Log("A124: the API killed with 1,499 of 3,000 checked in memory and 1,000 kept, none half done; paused while it was down")

	// Started again: still paused, at 1,000 of 3,000.
	s.sh(t, s.startAPI)
	if !waitFor(3*time.Minute, func() bool { st, d, n := metrics(); return st == 2 && d == 1000 && n == pages }) {
		st, d, n := metrics()
		t.Fatalf("after the start, Prometheus has state %v, %v of %v; want 2 (paused), 1000 of %d", st, d, n, pages)
	}
	var got map[string][]float64
	if !waitFor(time.Minute, func() bool {
		got = s.panelLasts(t, "pa-requests")
		p := got["Retroactive redaction progress"]
		return len(got["Retroactive redaction"]) == 1 && got["Retroactive redaction"][0] == 2 && len(p) == 1 && p[0] > 33.3 && p[0] < 33.4 &&
			len(got["Redaction checked per minute"])+len(got["Redaction time left"]) == 0
	}) {
		t.Errorf("paused, the dashboard's redaction panels say %v; want Retroactive redaction 2 (Paused), progress 33.3 %%, and no rate or time left", got)
	} else {
		t.Logf("A124: paused after the restart: Retroactive redaction %v (Paused), progress %v %%", got["Retroactive redaction"], got["Retroactive redaction progress"])
	}
	if st, d := runState(); st != "paused" || d != 1000 {
		t.Errorf("paused after the restart, the run is %s with %d checked", st, d)
	}

	// Resumed: it checks the rest, and finishes.
	if _, err := s.st.Pool.Exec(ctx, `UPDATE redaction_runs SET state = 'running', changed_at = now(); SELECT pg_notify('pagerarchive_redaction', '')`); err != nil {
		t.Fatal(err)
	}
	if !waitFor(2*time.Minute, func() bool { st, d, n := metrics(); return st == 0 && d >= pages && n == d }) {
		st, d, n := metrics()
		t.Fatalf("resumed, Prometheus has state %v, %v of %v; want 0 (idle), all %d checked", st, d, n, pages)
	}
	var left int64
	_ = s.st.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE capcode = $1 AND strpos(text, $2) > 0`, capcode, term).Scan(&left)
	if n := halfDone(); n != 0 || left != 0 {
		t.Errorf("finished: %d pages half done, %d still holding the term", n, left)
	}
	if !waitFor(90*time.Second, func() bool {
		return s.lokiCount(t, `{service_name="pagerarchive"} |= "retroactive redaction carried on" | done="1000" | total="3000"`) > 0 &&
			s.lokiCount(t, `{service_name="pagerarchive"} |= "retroactive redaction finished"`) > 0
	}) {
		t.Error(`Loki has no "retroactive redaction carried on" line at 1,000 of 3,000, or no finished line`)
	} else {
		t.Log("A124: resumed from 1,000: Loki has the carried-on line and the finished line")
	}
	for _, bad := range []string{"MADE-UP", term} {
		if n := s.lokiCount(t, `{service_name="pagerarchive"} |= "`+bad+`"`); n > 0 {
			t.Errorf("Loki has %d lines with %q", n, bad)
		}
	}
}
