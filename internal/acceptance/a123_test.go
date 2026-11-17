package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// a123 is A123's re-parse worker, started and ended as the API's is: run
// ends it part way through a batch, as a crash, restart or upgrade does
// (nothing more is written; the batch in hand is rolled back), and a new
// worker on the same database, with a fresh progress, is the API started
// again. scripts/monitoring-checks.sh kills the stack's API itself (A123's
// Grafana part).
type a123 struct {
	e      *env
	cancel context.CancelFunc
	ended  chan struct{}
}

// start is the API starting: a new worker, its start-up profile check with
// hash, and its loop.
func (a *a123) start(hash string) {
	t := a.e.t
	t.Helper()
	old := a.e.worker
	w := &reparse.Worker{Store: a.e.st, Network: old.Network, Agencies: old.Agencies, Streets: old.Streets, Geo: old.Geo,
		Log: old.Log, Poll: time.Minute, Tracer: old.Tracer, Regroup: old.Regroup, ProfileHash: hash, ProgressEvery: 100 * time.Millisecond}
	if err := w.CheckProfiles(a.e.ctx); err != nil {
		t.Fatal(err)
	}
	a.e.worker = w
	a.e.srv.Reparse = &w.Progress // between requests: the new process's
	ctx, cancel := context.WithCancel(a.e.ctx)
	a.cancel, a.ended = cancel, make(chan struct{})
	go func() { w.Run(ctx); close(a.ended) }()
}

// kill ends the worker where it is.
func (a *a123) kill() {
	a.cancel()
	select {
	case <-a.ended:
	case <-time.After(time.Minute):
		a.e.t.Fatal("the worker didn't end within a minute")
	}
}

// A123. Re-parse resumable, pauseable, stoppable and restartable (owner,
// 2026-11-18; R23c): over a few thousand made-up pages, a worker ended part
// way carries on from its last batch when started again, doing no page
// twice beyond the batch in hand and skipping none; a pause holds across a
// restart; a stop leaves the rest as they were until Start again runs
// everything from the beginning; a profile change part way starts it again
// from the beginning; and the status, metrics and log follow.
func TestA123ReparseControl(t *testing.T) {
	e := setup(t)
	const pages, others, held = 3000, 200, 1500
	const capcode, other = "0912347", "0100001"
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO messages (capcode, text, sent_at, dedup_key)
		SELECT CASE WHEN g <= $1::int THEN $2::text ELSE $3::text END, 'MADE-UP A123 PAGE ' || g,
		       timestamptz '2020-01-01 00:00:00+00' + g * interval '1 minute', sha256(('a123 ' || g)::bytea)
		FROM generate_series(1, $1::int + $4::int) g`, pages, capcode, other, others); err != nil {
		t.Fatal(err)
	}
	// How often each page was re-parsed and kept (a rolled-back batch's
	// pages aren't), and how often it was re-parsed at all (a sequence
	// isn't rolled back).
	if _, err := e.st.Pool.Exec(e.ctx, `
		CREATE TABLE a123_kept (id bigint PRIMARY KEY, n int NOT NULL);
		CREATE SEQUENCE a123_tries;
		CREATE FUNCTION a123_count() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			PERFORM nextval('a123_tries');
			INSERT INTO a123_kept VALUES (NEW.id, 1) ON CONFLICT (id) DO UPDATE SET n = a123_kept.n + 1;
			RETURN NULL;
		END $$;
		CREATE TRIGGER a123_count AFTER UPDATE OF format ON messages FOR EACH ROW EXECUTE FUNCTION a123_count();
		INSERT INTO settings (key, value) VALUES ('profile_hash', 'h1') ON CONFLICT (key) DO UPDATE SET value = 'h1'`); err != nil {
		t.Fatal(err)
	}
	ids := func(c string) []int64 {
		rows, err := e.st.Pool.Query(e.ctx, `SELECT id FROM messages WHERE capcode = $1 ORDER BY id`, c)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			out = append(out, id)
		}
		return out
	}
	ours := ids(capcode)
	heldID := ours[held-1]
	// kept is how many times each page has been re-parsed, by its kept count:
	// {1: 3000} is every page once.
	kept := func(c string) map[int]int {
		rows, err := e.st.Pool.Query(e.ctx, `SELECT coalesce(k.n, 0), count(*) FROM messages m LEFT JOIN a123_kept k ON k.id = m.id
			WHERE m.capcode = $1 GROUP BY 1`, c)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int]int{}
		for rows.Next() {
			var n, c int
			_ = rows.Scan(&n, &c)
			out[n] = c
		}
		return out
	}
	keptRange := func(from, to int64) map[int]int {
		out := map[int]int{}
		rows, _ := e.st.Pool.Query(e.ctx, `SELECT coalesce(k.n, 0), count(*) FROM messages m LEFT JOIN a123_kept k ON k.id = m.id
			WHERE m.id BETWEEN $1 AND $2 GROUP BY 1`, from, to)
		defer rows.Close()
		for rows.Next() {
			var n, c int
			_ = rows.Scan(&n, &c)
			out[n] = c
		}
		return out
	}
	tries := func() int64 {
		var n int64
		_ = e.st.Pool.QueryRow(e.ctx, `SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM a123_tries`).Scan(&n)
		return n
	}
	hold := func() func() {
		tx, err := e.st.Pool.Begin(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(e.ctx, `SELECT 1 FROM messages WHERE id = $1 FOR UPDATE`, heldID); err != nil {
			t.Fatal(err)
		}
		return func() { _ = tx.Rollback(e.ctx) }
	}
	status := func() reparse.Status {
		t.Helper()
		var s reparse.Status
		if code, p := e.admin.call(http.MethodGet, "/api/v1/admin/reparse", nil, &s); code != http.StatusOK {
			t.Fatalf("GET /api/v1/admin/reparse: %d %v", code, p)
		}
		return s
	}
	action := func(what string, wantCode int, wantProblem string) reparse.Status {
		t.Helper()
		var s reparse.Status
		code, p := e.admin.call(http.MethodPost, "/api/v1/admin/reparse/"+what, nil, &s)
		if code != wantCode || p.Code != wantProblem {
			t.Fatalf("POST %s: %d %v, want %d %q", what, code, p, wantCode, wantProblem)
		}
		return s
	}
	scrape := func() map[string]float64 {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, e.url+"/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+metricsToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]float64{}
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if name, v, ok := strings.Cut(sc.Text(), " "); ok && strings.HasPrefix(name, "pagerarchive_reparse_") {
				out[name], _ = strconv.ParseFloat(v, 64)
			}
		}
		return out
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("not within 2 minutes: %s (status %+v)", what, status())
	}
	runState := func() (state string, done int64, after int64) {
		_ = e.st.Pool.QueryRow(e.ctx, `SELECT r.state, r.done, coalesce(max(p.after_id), 0) FROM reparse_runs r
			LEFT JOIN reparse_run_patterns p ON p.run_id = r.id GROUP BY r.id`).Scan(&state, &done, &after)
		return
	}
	request := func(pattern, reason string) {
		if err := e.st.RequestReparse(e.ctx, db.RequestReparseParams{CapcodePattern: pattern, Reason: reason}); err != nil {
			t.Fatal(err)
		}
	}
	heldAt := func() {
		waitFor("held at the 1,500th page", func() bool { return e.worker.Progress.Snapshot().Done == held-1 })
	}
	finished := func() {
		waitFor("the run finished", func() bool { s, _, _ := runState(); return s == "finished" })
	}
	a := &a123{e: e}
	if s := status(); s.State != reparse.Idle || s.Total != 0 || s.StartedAt != nil {
		t.Fatalf("before any re-parse: %+v", s)
	}
	if code, _ := e.user.call(http.MethodGet, "/api/v1/admin/reparse", nil, nil); code != http.StatusForbidden {
		t.Errorf("a user's GET: %d, want 403", code)
	}
	if code, _ := e.user.call(http.MethodPost, "/api/v1/admin/reparse/pause", nil, nil); code != http.StatusForbidden {
		t.Errorf("a user's pause: %d, want 403", code)
	}
	action("start", http.StatusConflict, reparse.CodeNone)

	// 1. Ended part way, started again: it carries on from its last batch.
	release := hold()
	request(capcode, "alias added")
	a.start("h1")
	heldAt()
	m := scrape()
	if m["pagerarchive_reparse_state"] != 1 || m["pagerarchive_reparse_running"] != 1 || m["pagerarchive_reparse_run_messages_done"] != held-1 {
		t.Errorf("while it runs, the metrics: %v", m)
	}
	if s := status(); s.State != reparse.Running || s.Total != pages || s.Done != held-1 || s.Patterns != 1 || s.Requests != 1 ||
		strings.Join(s.Reasons, ",") != "alias_added" || s.StartedAt == nil {
		t.Errorf("while it runs, the status: %+v", s)
	}
	a.kill()
	release()
	st, done, after := runState()
	if st != "running" || done != 1000 || after != ours[999] {
		t.Fatalf("after it was ended: the run is %s, %d done, after ID %d; want running, 1000, %d", st, done, after, ours[999])
	}
	if k := kept(capcode); k[1] != 1000 || k[0] != pages-1000 {
		t.Errorf("after it was ended, pages kept: %v, want 1,000 once and 2,000 not yet", k)
	}
	inHand := tries() - 1000
	a.start("h1")
	if m := scrape(); m["pagerarchive_reparse_run_messages_done"] != 0 && m["pagerarchive_reparse_run_messages_done"] < 1000 {
		t.Errorf("started again, the metrics: %v", m)
	}
	finished()
	if k := kept(capcode); k[1] != pages || len(k) != 1 {
		t.Errorf("carried on: pages re-parsed and kept %v, want each of the %d once", k, pages)
	}
	if extra := tries() - pages; extra != inHand || extra > 500 {
		t.Errorf("re-parsed %d pages more than once (the batch in hand had %d), want only the batch in hand's, at most 500", extra, inHand)
	}
	if k := kept(other); k[0] != others {
		t.Errorf("the other capcode's pages: %v, want none re-parsed", k)
	}
	if m := scrape(); m["pagerarchive_reparse_state"] != 0 || m["pagerarchive_reparse_run_messages_done"] != pages || m["pagerarchive_reparse_run_messages"] != pages {
		t.Errorf("finished, the metrics: %v", m)
	}
	if s := status(); s.State != reparse.Idle || s.Done != pages || s.FinishedAt == nil {
		t.Errorf("finished, the status: %+v", s)
	}
	t.Logf("A123: ended at %d of %d (1,000 kept), started again, it finished with every page kept once; %d re-parsed twice, the batch in hand's", held-1, pages, inHand)

	// 2. A pause holds across a restart.
	release = hold()
	request(capcode, "alias template changed")
	waitFor("the second run held", func() bool { return e.worker.Progress.Snapshot().Done == held-1 })
	action("pause", http.StatusOK, "")
	action("pause", http.StatusConflict, reparse.CodeNotRunning)
	release()
	waitFor("paused", func() bool { s, d, _ := runState(); return s == "paused" && d == held })
	if s := status(); s.State != reparse.Paused || s.Done != held || s.PerMinute != 0 || s.ETASeconds != nil {
		t.Errorf("paused, the status: %+v", s)
	}
	if m := scrape(); m["pagerarchive_reparse_state"] != 2 || m["pagerarchive_reparse_running"] != 0 || m["pagerarchive_reparse_run_messages_done"] != held {
		t.Errorf("paused, the metrics: %v", m)
	}
	a.kill()
	before := tries()
	a.start("h1")
	waitFor("paused after the restart", func() bool { return e.worker.Progress.Snapshot().State == reparse.Paused })
	time.Sleep(time.Second)
	if n := tries(); n != before {
		t.Errorf("paused, started again: %d pages re-parsed, want none", n-before)
	}
	if m := scrape(); m["pagerarchive_reparse_state"] != 2 || m["pagerarchive_reparse_run_messages_done"] != held || m["pagerarchive_reparse_run_messages"] != pages {
		t.Errorf("paused after the restart, the metrics: %v", m)
	}
	action("resume", http.StatusOK, "")
	action("resume", http.StatusConflict, reparse.CodeNotPaused)
	finished()
	if k := kept(capcode); k[2] != pages || len(k) != 1 {
		t.Errorf("resumed: pages kept %v, want each twice now", k)
	}
	t.Logf("A123: paused at %d, it stayed paused after a restart, and resumed from there", held)

	// 3. Stopped, the rest stay as they were; Start again runs it all from
	// the beginning, with the requests that waited.
	release = hold()
	request(capcode, "alias ignored changed")
	waitFor("the third run held", func() bool { return e.worker.Progress.Snapshot().Done == held-1 })
	action("stop", http.StatusOK, "")
	release()
	waitFor("stopped", func() bool { s, d, _ := runState(); return s == "stopped" && d == held })
	request(other, "alias added")
	time.Sleep(time.Second)
	if k := keptRange(ours[0], ours[held-1]); k[3] != held {
		t.Errorf("stopped: the first %d pages kept %v, want 3 times each", held, k)
	}
	if k := keptRange(ours[held], ours[pages-1]); k[2] != pages-held {
		t.Errorf("stopped: the rest kept %v, want twice each, as before", k)
	}
	if s := status(); s.State != reparse.Stopped || s.Done != held || s.Total != pages || s.Waiting != 1 {
		t.Errorf("stopped, the status: %+v", s)
	}
	if m := scrape(); m["pagerarchive_reparse_state"] != 3 {
		t.Errorf("stopped, the metrics: %v", m)
	}
	action("resume", http.StatusConflict, reparse.CodeStopped)
	action("stop", http.StatusConflict, reparse.CodeNotRunning)
	s := action("start", http.StatusOK, "")
	if s.State != reparse.Running || s.Total != pages+others || s.Waiting != 0 || s.Requests != 2 || strings.Join(s.Reasons, ",") != "alias_added,alias_ignored_changed" {
		t.Errorf("started again, the status: %+v", s)
	}
	finished()
	if k := keptRange(ours[0], ours[held-1]); k[4] != held {
		t.Errorf("started again: the first %d pages kept %v, want 4 times each", held, k)
	}
	if k := keptRange(ours[held], ours[pages-1]); k[3] != pages-held {
		t.Errorf("started again: the rest kept %v, want 3 times each", k)
	}
	if k := kept(other); k[1] != others {
		t.Errorf("started again: the waiting request's pages %v, want each once", k)
	}
	t.Logf("A123: stopped at %d, the rest as they were; Start again re-parsed all %d from the beginning", held, pages+others)

	// 4. The profiles change part way: it starts again from the beginning,
	// over every message; a paused one stays paused.
	release = hold()
	request(capcode, "alias capcode changed")
	waitFor("the fourth run held", func() bool { return e.worker.Progress.Snapshot().Done == held-1 })
	a.kill()
	release()
	if st, done, _ := runState(); st != "running" || done != 1000 {
		t.Fatalf("ended: %s, %d done", st, done)
	}
	a.start("h2")
	if s := status(); s.Total != pages+others || strings.Join(s.Reasons, ",") != "alias_capcode_changed,profiles_changed" || s.Patterns != 1 {
		t.Errorf("the profiles changed, the status: %+v", s)
	}
	finished()
	if k := keptRange(ours[0], ours[999]); k[6] != 1000 {
		t.Errorf("the profiles changed: the first 1,000 pages kept %v, want 6 times (done before, and again)", k)
	}
	if k := keptRange(ours[1000], ours[held-1]); k[5] != held-1000 {
		t.Errorf("the profiles changed: pages 1,001-1,500 kept %v, want 5 times (once more than before)", k)
	}
	if k := keptRange(ours[held], ours[pages-1]); k[4] != pages-held {
		t.Errorf("the profiles changed: the rest kept %v, want 4 times each", k)
	}
	if k := kept(other); k[2] != others {
		t.Errorf("the profiles changed: the other capcode's pages %v, want each twice", k)
	}
	release = hold()
	request(capcode, "alias added")
	waitFor("the fifth run held", func() bool { return e.worker.Progress.Snapshot().Done == held-1 })
	action("pause", http.StatusOK, "")
	release()
	waitFor("paused", func() bool { s, _, _ := runState(); return s == "paused" })
	a.kill()
	a.start("h3")
	waitFor("paused after the restart", func() bool { return e.worker.Progress.Snapshot().State == reparse.Paused })
	if s := status(); s.State != reparse.Paused || s.Done != 0 || s.Total != pages+others {
		t.Errorf("paused, then the profiles changed: %+v, want paused at 0 of %d", s, pages+others)
	}
	action("resume", http.StatusOK, "")
	finished()
	a.kill()

	// The log: each state change, the admin's with their ID; counts and codes
	// only.
	logs := e.logs.String()
	saw := map[string]int{}
	for _, l := range strings.Split(logs, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil {
			continue
		}
		msg, _ := m["msg"].(string)
		switch msg {
		case "re-parse paused", "re-parse resumed", "re-parse stopped", "re-parse started again":
			if m["by"] != float64(e.admin.id) || m["state"] == nil || m["total"] == nil {
				t.Errorf("an admin's line without who, the state or the total: %v", m)
			}
		case "re-parse carried on":
			if m["done"] == nil || m["total"] == nil || m["state"] != "running" {
				t.Errorf("a carried-on line: %v", m)
			}
		case "re-parse starts again from the beginning: the profiles changed":
			if m["state"] == nil || m["total"] != float64(pages+others) {
				t.Errorf("the profiles' line: %v", m)
			}
		default:
			continue
		}
		saw[msg]++
	}
	for msg, n := range map[string]int{"re-parse paused": 2, "re-parse resumed": 2, "re-parse stopped": 1, "re-parse started again": 1,
		"re-parse carried on": 4, "re-parse starts again from the beginning: the profiles changed": 2} {
		if saw[msg] < n {
			t.Errorf("the log has %d %q lines, want %d (%v)", saw[msg], msg, n, saw)
		}
	}
	for _, bad := range []string{"MADE-UP", capcode} {
		if strings.Contains(logs, bad) {
			t.Errorf("the log has %q", bad)
		}
	}
}
