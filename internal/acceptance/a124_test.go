package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/longjob"
	"github.com/RIKIKU/PagerArchive/internal/nightly"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/redaction"
)

// a124 is A124's API process: a retroactive redaction worker with its
// start-up check and background loop, and the nightly run beside it. end
// ends both where they are, as a crash, restart or upgrade does (the batch
// in hand is rolled back), and a new one on the same database, with a
// fresh progress, is the API started again.
type a124 struct {
	e      *env
	w      *redaction.Worker
	job    *nightly.Job
	ctx    context.Context
	cancel context.CancelFunc
	ended  []chan struct{}
}

func (a *a124) start(n *profile.Network) {
	t := a.e.t
	t.Helper()
	a.w = &redaction.Worker{Store: a.e.st, Network: n, Reparse: a.e.worker, Log: a.e.redactor.Log, Poll: time.Minute,
		Tracer: a.e.redactor.Tracer, ProgressEvery: 100 * time.Millisecond}
	if err := a.w.CheckRules(a.e.ctx); err != nil {
		t.Fatal(err)
	}
	a.e.srv.Redaction = a.w // between requests: the new process's
	old := a.e.job
	a.job = &nightly.Job{Store: a.e.st, Redaction: a.w, Location: old.Location, At: old.At, Now: old.Now, Log: old.Log, Tracer: old.Tracer, Reports: old.Reports}
	a.ctx, a.cancel = context.WithCancel(a.e.ctx)
	a.ended = nil
	a.goRun(func(ctx context.Context) { a.w.Run(ctx) })
}

func (a *a124) goRun(fn func(context.Context)) {
	done := make(chan struct{})
	a.ended = append(a.ended, done)
	go func() { fn(a.ctx); close(done) }()
}

// nightly is the nightly run, in this process, in the background.
func (a *a124) nightly() {
	a.goRun(func(ctx context.Context) { _, _ = a.job.RunOnce(ctx, time.Now()) })
}

func (a *a124) end() {
	a.cancel()
	for _, d := range a.ended {
		select {
		case <-d:
		case <-time.After(time.Minute):
			a.e.t.Fatal("the process didn't end within a minute")
		}
	}
}

// A124. Retroactive redaction: one unit, resumable, pauseable, stoppable
// and restartable (owner, 2026-11-18; R45a, R91; ADR 0042's note 3): over a
// few thousand made-up pages stored before a rule matched them, a run ended
// between a page's new text and its re-parse leaves no page with one and
// not the other, carries on from its last batch when started again, and
// finishes with the term gone from every table; a pause holds across a
// restart and a nightly run; a stop is started again by the next nightly
// run; rules changed at start-up start an unfinished run again in its
// state; and the status, metrics and log follow, with counts only.
func TestA124RedactionControl(t *testing.T) {
	e := setup(t)
	const pages, others, held = 3000, 200, 1500
	const capcode, other = "0912349", "0100002"
	const term = "0412 345 678" // made up; the example's mobile-number rule matches it
	// As if stored before a rule matching the term existed: the text and the
	// fields parsed from it hold it, on every other page.
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO messages (capcode, text, description, sent_at, dedup_key)
		SELECT c, t, t, timestamptz '2020-01-01 00:00:00+00' + g * interval '1 minute', sha256(('a124 ' || g)::bytea)
		FROM generate_series(1, $1::int + $4::int) g,
		LATERAL (SELECT CASE WHEN g <= $1::int THEN $2::text ELSE $3::text END AS c,
		                'MADE-UP A124 PAGE ' || g || CASE WHEN g <= $1::int AND g % 2 = 0 THEN ' CALL ' || $5::text ELSE '' END AS t) x`,
		pages, capcode, other, others, term); err != nil {
		t.Fatal(err)
	}
	// How often each page's re-parse was kept (a rolled-back batch's isn't)
	// and tried at all (a sequence isn't rolled back), and a lock that holds
	// a batch right after one page's new text is written, before its
	// re-parse: where note 3's stop fell.
	if _, err := e.st.Pool.Exec(e.ctx, `
		CREATE TABLE a124_kept (id bigint PRIMARY KEY, n int NOT NULL);
		CREATE SEQUENCE a124_tries;
		CREATE FUNCTION a124_count() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			PERFORM nextval('a124_tries');
			INSERT INTO a124_kept VALUES (NEW.id, 1) ON CONFLICT (id) DO UPDATE SET n = a124_kept.n + 1;
			RETURN NULL;
		END $$;
		CREATE TRIGGER a124_count AFTER UPDATE OF format ON messages FOR EACH ROW EXECUTE FUNCTION a124_count();
		CREATE TABLE a124_held (id bigint);
		CREATE FUNCTION a124_hold() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF NEW.id IN (SELECT id FROM a124_held) THEN PERFORM pg_advisory_xact_lock(124124); END IF;
			RETURN NULL;
		END $$;
		CREATE TRIGGER a124_hold AFTER UPDATE OF text ON messages FOR EACH ROW EXECUTE FUNCTION a124_hold();
		INSERT INTO settings (key, value) VALUES ('redaction_hash', 'rules before the term') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	var ours []int64
	rows, err := e.st.Pool.Query(e.ctx, `SELECT id FROM messages WHERE capcode = $1 ORDER BY id`, capcode)
	if err != nil {
		t.Fatal(err)
	}
	if ours, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		t.Fatal(err)
	}
	heldID := ours[held-1]
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO a124_held VALUES ($1)`, heldID); err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := e.st.Pool.QueryRow(e.ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// halfDone is note 3's state: a page whose text no longer holds the
	// term while a field parsed from it still does.
	halfDone := func() int64 {
		return count(`SELECT count(*) FROM messages WHERE capcode = $1 AND strpos(text, $2) = 0
			AND (strpos(coalesce(description, ''), $2) > 0 OR strpos(coalesce(address, ''), $2) > 0 OR strpos(coalesce(extra::text, ''), $2) > 0)`, capcode, term)
	}
	withTerm := func() int64 { return count(`SELECT count(*) FROM messages WHERE strpos(text, $1) > 0`, term) }
	tries := func() int64 { return count(`SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM a124_tries`) }
	// evens are the pages that hold the term: every other one of ours.
	var evens []int64
	for i, id := range ours {
		if (i+1)%2 == 0 {
			evens = append(evens, id)
		}
	}
	// kept is how many times each of those has been re-parsed and kept:
	// {1: 1500} is each once.
	kept := func() map[int]int {
		rows, err := e.st.Pool.Query(e.ctx, `SELECT coalesce(k.n, 0), count(*) FROM unnest($1::bigint[]) m(id) LEFT JOIN a124_kept k ON k.id = m.id GROUP BY 1`, evens)
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
	// untouched is how many pages the rules don't change were re-parsed:
	// none should be.
	untouched := func() int64 {
		return count(`SELECT count(*) FROM a124_kept WHERE NOT (id = ANY($1::bigint[]))`, evens)
	}
	var lockConn *pgx.Conn
	hold := func() {
		t.Helper()
		c, err := pgx.Connect(e.ctx, e.st.Pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(e.ctx, `SELECT pg_advisory_lock(124124)`); err != nil {
			t.Fatal(err)
		}
		lockConn = c
	}
	release := func() {
		if lockConn != nil {
			_ = lockConn.Close(e.ctx)
			lockConn = nil
		}
	}
	t.Cleanup(release)
	status := func() redaction.Status {
		t.Helper()
		var s redaction.Status
		if code, p := e.admin.call(http.MethodGet, "/api/v1/admin/redaction", nil, &s); code != http.StatusOK {
			t.Fatalf("GET /api/v1/admin/redaction: %d %v", code, p)
		}
		return s
	}
	action := func(what string, wantCode int, wantProblem string) redaction.Status {
		t.Helper()
		var s redaction.Status
		code, p := e.admin.call(http.MethodPost, "/api/v1/admin/redaction/"+what, nil, &s)
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
			if name, v, ok := strings.Cut(sc.Text(), " "); ok && strings.HasPrefix(name, "pagerarchive_redaction_") {
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
	runState := func() (state string, done, after int64) {
		_ = e.st.Pool.QueryRow(e.ctx, `SELECT state, done, after_id FROM redaction_runs`).Scan(&state, &done, &after)
		return
	}
	a := &a124{e: e}
	heldAt := func() {
		waitFor("held at the 1,500th page", func() bool { return a.w.Progress.Snapshot().Done == held-1 })
	}
	finished := func() {
		waitFor("the run finished", func() bool { s, _, _ := runState(); return s == "finished" })
	}
	// putBack is the term stored again on every other page, text and
	// parsed fields, as if from before the rule.
	putBack := func() {
		if _, err := e.st.Pool.Exec(e.ctx, `UPDATE messages m SET text = x.t, description = x.t
			FROM (SELECT id, 'MADE-UP A124 PAGE ' || row_number() OVER (ORDER BY id) || ' CALL ' || $2::text AS t,
			             row_number() OVER (ORDER BY id) AS g FROM messages WHERE capcode = $1) x
			WHERE m.id = x.id AND x.g % 2 = 0`, capcode, term); err != nil {
			t.Fatal(err)
		}
	}
	hash := redaction.RulesHash(e.redactor.Network)

	a.start(e.redactor.Network)
	if s := status(); s.State != longjob.Idle || s.Total != 0 || s.StartedAt != nil || !s.Pending {
		t.Fatalf("before any run, with rules not yet applied: %+v", s)
	}
	if code, _ := e.user.call(http.MethodGet, "/api/v1/admin/redaction", nil, nil); code != http.StatusForbidden {
		t.Errorf("a user's GET: %d, want 403", code)
	}
	if code, _ := e.user.call(http.MethodPost, "/api/v1/admin/redaction/pause", nil, nil); code != http.StatusForbidden {
		t.Errorf("a user's pause: %d, want 403", code)
	}
	action("pause", http.StatusConflict, redaction.CodeNotRunning)
	action("resume", http.StatusConflict, redaction.CodeNotPaused)
	action("stop", http.StatusConflict, redaction.CodeNotRunning)

	// 1. The nightly run starts it; it's ended between a page's new text and
	// its re-parse, and nothing is half done.
	hold()
	a.nightly()
	heldAt()
	if s := status(); s.State != longjob.Running || s.Total != pages+others || s.Done != held-1 || s.Reason != redaction.ReasonRulesChanged ||
		s.StartedAt == nil || !s.Pending {
		t.Errorf("while it runs, the status: %+v", s)
	}
	if m := scrape(); m["pagerarchive_redaction_state"] != 1 || m["pagerarchive_redaction_running"] != 1 ||
		m["pagerarchive_redaction_run_messages_done"] != held-1 || m["pagerarchive_redaction_run_messages"] != pages+others {
		t.Errorf("while it runs, the metrics: %v", m)
	}
	if n := halfDone(); n != 0 {
		t.Errorf("held between a page's new text and its re-parse: %d pages show new text beside old fields", n)
	}
	a.end()
	release()
	time.Sleep(500 * time.Millisecond) // the batch in hand's rollback
	st, done, after := runState()
	if st != "running" || done != 1000 || after != ours[999] {
		t.Fatalf("after it was ended: the run is %s, %d checked, after ID %d; want running, 1000, %d", st, done, after, ours[999])
	}
	if n := halfDone(); n != 0 {
		t.Errorf("ended between a page's new text and its re-parse: %d pages have new text beside old fields", n)
	}
	if n := withTerm(); n != (pages-1000)/2 {
		t.Errorf("after it was ended, %d pages still hold the term; want the %d past its last batch", n, (pages-1000)/2)
	}
	if k := kept(); k[1] != 500 || k[0] != pages/2-500 {
		t.Errorf("after it was ended, the changed pages' re-parses kept: %v, want 500 once (its last batch) and the rest not yet", k)
	}
	inHand := tries() - 500

	// 2. Started again: it carries on from its last batch, without waiting
	// for a night, and finishes.
	a.start(e.redactor.Network)
	finished()
	if hits := e.rowsContaining(term); len(hits) != 0 {
		t.Errorf("finished, the term is still stored: %d rows, first %q", len(hits), hits[0][:min(len(hits[0]), 40)])
	}
	if n := halfDone(); n != 0 {
		t.Errorf("finished: %d pages half done", n)
	}
	if k := kept(); k[1] != pages/2 || len(k) != 1 {
		t.Errorf("carried on: the changed pages' re-parses kept %v, want each of the %d once", k, pages/2)
	}
	if extra := tries() - pages/2; extra != inHand || extra > 1000 {
		t.Errorf("re-parsed %d pages more than once (the batch in hand had %d), want only the batch in hand's", extra, inHand)
	}
	if n := untouched(); n != 0 {
		t.Errorf("%d pages the rules don't change were re-parsed", n)
	}
	if m := scrape(); m["pagerarchive_redaction_state"] != 0 || m["pagerarchive_redaction_run_messages_done"] != pages+others || m["pagerarchive_redaction_run_messages"] != pages+others {
		t.Errorf("finished, the metrics: %v", m)
	}
	if s := status(); s.State != longjob.Idle || s.Done != pages+others || s.Redacted != pages/2 || s.FinishedAt == nil || s.Pending {
		t.Errorf("finished, the status: %+v", s)
	}
	var applied string
	_ = e.st.Pool.QueryRow(e.ctx, `SELECT value FROM settings WHERE key = 'redaction_hash'`).Scan(&applied)
	if applied != hash {
		t.Errorf("finished, the rules aren't saved as applied")
	}
	if res, err := a.job.RunOnce(e.ctx, time.Now()); err != nil || res.Redacted != 0 {
		t.Errorf("the next nightly run, with nothing to do: %+v %v", res, err)
	}
	t.Logf("A124: ended at %d of %d (1,000 kept, none half done), started again, it carried on and finished; %d re-parsed twice, the batch in hand's", held-1, pages+others, inHand)

	// 3. A pause holds across a restart and the nightly run.
	putBack()
	hold()
	if s := action("start", http.StatusOK, ""); s.State != longjob.Running || s.Reason != redaction.ReasonStartedAgain {
		t.Errorf("started again: %+v", s)
	}
	heldAt()
	action("pause", http.StatusOK, "")
	action("pause", http.StatusConflict, redaction.CodeNotRunning)
	release()
	waitFor("paused", func() bool { s, d, _ := runState(); return s == "paused" && d == held })
	if s := status(); s.State != longjob.Paused || s.Done != held || s.PerMinute != 0 || s.ETASeconds != nil || !s.Pending {
		t.Errorf("paused, the status: %+v", s)
	}
	if m := scrape(); m["pagerarchive_redaction_state"] != 2 || m["pagerarchive_redaction_running"] != 0 || m["pagerarchive_redaction_run_messages_done"] != held {
		t.Errorf("paused, the metrics: %v", m)
	}
	a.end()
	before := tries()
	a.start(e.redactor.Network)
	waitFor("paused after the restart", func() bool { return a.w.Progress.Snapshot().State == longjob.Paused })
	if res, err := a.job.RunOnce(e.ctx, time.Now()); err != nil || res.Redacted != 0 {
		t.Errorf("the nightly run while paused: %+v %v", res, err)
	}
	time.Sleep(time.Second)
	if n := tries(); n != before {
		t.Errorf("paused, started again and a nightly run: %d pages re-parsed, want none", n-before)
	}
	if s, d, _ := runState(); s != "paused" || d != held {
		t.Errorf("paused after a restart and a nightly run: %s at %d", s, d)
	}
	if n := halfDone(); n != 0 {
		t.Errorf("paused: %d pages half done", n)
	}
	action("resume", http.StatusOK, "")
	action("resume", http.StatusConflict, redaction.CodeNotPaused)
	finished()
	if n := withTerm(); n != 0 {
		t.Errorf("resumed, %d pages still hold the term", n)
	}
	t.Logf("A124: paused at %d, it stayed paused after a restart and a nightly run, and resumed from there", held)

	// 4. Stopped, the next nightly run starts it again from the beginning.
	putBack()
	hold()
	action("start", http.StatusOK, "")
	heldAt()
	action("stop", http.StatusOK, "")
	release()
	waitFor("stopped", func() bool { s, d, _ := runState(); return s == "stopped" && d == held })
	if s := status(); s.State != longjob.Stopped || s.Done != held || !s.Pending {
		t.Errorf("stopped, the status: %+v", s)
	}
	if m := scrape(); m["pagerarchive_redaction_state"] != 3 {
		t.Errorf("stopped, the metrics: %v", m)
	}
	action("resume", http.StatusConflict, redaction.CodeStopped)
	action("stop", http.StatusConflict, redaction.CodeNotRunning)
	if n := halfDone(); n != 0 {
		t.Errorf("stopped: %d pages half done", n)
	}
	if n := withTerm(); n != (pages-held)/2 {
		t.Errorf("stopped at %d, %d pages hold the term, want the %d after it", held, n, (pages-held)/2)
	}
	if res, err := a.job.RunOnce(e.ctx, time.Now()); err != nil || res.Redacted != (pages-held)/2 {
		t.Errorf("the nightly run after a stop: %+v %v, want the %d pages left redacted", res, err, (pages-held)/2)
	}
	if s, d, _ := runState(); s != "finished" || d != pages+others {
		t.Errorf("after the nightly run, the run is %s at %d", s, d)
	}
	if n := withTerm(); n != 0 {
		t.Errorf("after the nightly run, %d pages still hold the term", n)
	}
	t.Log("A124: stopped at 1,500, the next nightly run started it again from the beginning and finished")

	// 5. Rules changed at start-up start an unfinished run again from the
	// beginning, in its state: a paused one stays paused.
	putBack()
	hold()
	action("start", http.StatusOK, "")
	heldAt()
	action("pause", http.StatusOK, "")
	release()
	waitFor("paused again", func() bool { s, d, _ := runState(); return s == "paused" && d == held })
	a.end()
	changed := *e.redactor.Network
	changed.Redactions = append(slices.Clone(changed.Redactions), profile.Redaction{Name: "a124-made-up", Re: regexp.MustCompile(`\bKEY[0-9]{4}\b`)})
	a.start(&changed)
	if s, d, _ := runState(); s != "paused" || d != 0 {
		t.Errorf("rules changed while paused: the run is %s at %d, want paused at 0", s, d)
	}
	waitFor("paused under the new rules", func() bool { return a.w.Progress.Snapshot().State == longjob.Paused })
	if s := status(); s.State != longjob.Paused || s.Done != 0 || s.Total != pages+others {
		t.Errorf("rules changed while paused, the status: %+v", s)
	}
	action("resume", http.StatusOK, "")
	finished()
	_ = e.st.Pool.QueryRow(e.ctx, `SELECT value FROM settings WHERE key = 'redaction_hash'`).Scan(&applied)
	if applied != redaction.RulesHash(&changed) || withTerm() != 0 || halfDone() != 0 {
		t.Errorf("under the new rules: applied %v, %d pages with the term, %d half done", applied == redaction.RulesHash(&changed), withTerm(), halfDone())
	}
	a.end()

	// The log: each state change, the admin's with their ID; counts and
	// codes only, never the pages' text, the term or the capcode.
	logs := e.logs.String()
	saw := map[string]int{}
	for _, l := range strings.Split(logs, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil {
			continue
		}
		msg, _ := m["msg"].(string)
		switch msg {
		case "retroactive redaction paused", "retroactive redaction resumed", "retroactive redaction stopped", "retroactive redaction started again":
			if m["by"] != float64(e.admin.id) || m["state"] == nil || m["total"] == nil {
				t.Errorf("an admin's line without who, the state or the total: %v", m)
			}
		case "retroactive redaction started":
			if m["reasons"] == nil || m["total"] != float64(pages+others) {
				t.Errorf("a start line: %v", m)
			}
		case "retroactive redaction carried on":
			if m["done"] == nil || m["total"] == nil || m["state"] != "running" {
				t.Errorf("a carried-on line: %v", m)
			}
		case "retroactive redaction progress":
			if m["done"] == nil || m["total"] == nil || m["percent"] == nil {
				t.Errorf("a progress line: %v", m)
			}
		case "retroactive redaction finished":
			if m["count"] == nil || m["redacted"] == nil || m["seconds"] == nil {
				t.Errorf("a finished line: %v", m)
			}
		case "retroactive redaction starts again from the beginning: the rules changed":
			if m["state"] != "paused" || m["total"] != float64(pages+others) {
				t.Errorf("the rules' line: %v", m)
			}
		default:
			continue
		}
		saw[msg]++
	}
	for msg, n := range map[string]int{"retroactive redaction paused": 2, "retroactive redaction resumed": 2, "retroactive redaction stopped": 1,
		"retroactive redaction started again": 3, "retroactive redaction started": 4, "retroactive redaction carried on": 2,
		"retroactive redaction progress": 1, "retroactive redaction finished": 4,
		"retroactive redaction starts again from the beginning: the rules changed": 1} {
		if saw[msg] < n {
			t.Errorf("the log has %d %q lines, want %d (%v)", saw[msg], msg, n, saw)
		}
	}
	for _, bad := range []string{"MADE-UP", term, capcode} {
		if strings.Contains(logs, bad) {
			t.Errorf("the log has %q", bad)
		}
	}
}
