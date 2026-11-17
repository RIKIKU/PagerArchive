package acceptance

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/telemetry/telemetrytest"
)

// A122. Re-parse progress (R23b): a re-parse over a few thousand made-up
// messages shows on /metrics while it runs, with the run's total counted at
// the start and what's done so far, and when it's finished, done equals the
// total and running is 0 again. It logs a start line, progress lines and the
// finished line, with counts only, on stdout and over OTLP.
func TestA122ReparseProgress(t *testing.T) {
	col := telemetrytest.New()
	defer col.Close()
	otelTestEnv(t, col.URL)
	e := setup(t, traced{tel: startTelemetry(t, io.Discard)})
	e.worker.ProgressEvery = 100 * time.Millisecond
	const pages, held = 3000, 1500
	const capcode = "0912345"
	// The made-up pages: 3,000 on one capcode, and 200 on Station 1's, which
	// the first re-parse doesn't match.
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO messages (capcode, text, sent_at, dedup_key)
		SELECT CASE WHEN g <= $1 THEN $2 ELSE '0100001' END, 'MADE-UP REPARSE PAGE ' || g,
		       timestamptz '2020-01-01 00:00:00+00' + g * interval '1 minute', sha256(int8send(g::bigint))
		FROM generate_series(1, $1 + 200) g`, pages, capcode); err != nil {
		t.Fatal(err)
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
			name, v, ok := strings.Cut(sc.Text(), " ")
			if !ok || !strings.HasPrefix(name, "pagerarchive_reparse_") {
				continue
			}
			out[name], _ = strconv.ParseFloat(v, 64)
		}
		return out
	}
	want := func(when string, got map[string]float64, w map[string]float64) {
		t.Helper()
		for k, v := range w {
			if g, ok := got[k]; !ok || g != v {
				t.Errorf("%s: %s = %v (there: %v), want %v", when, k, g, ok, v)
			}
		}
	}
	idle := scrape()
	want("before any re-parse", idle, map[string]float64{"pagerarchive_reparse_running": 0, "pagerarchive_reparse_run_messages": 0,
		"pagerarchive_reparse_run_messages_done": 0, "pagerarchive_reparse_messages_total": 0})
	if _, ok := idle["pagerarchive_reparse_run_started_timestamp_seconds"]; ok {
		t.Error("before any re-parse: a start time")
	}

	// The 1,500th page is locked, so the run stops there while it's checked.
	var heldID int64
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT id FROM messages WHERE capcode = $1 ORDER BY id OFFSET $2 LIMIT 1`, capcode, held-1).Scan(&heldID); err != nil {
		t.Fatal(err)
	}
	tx, err := e.st.Pool.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err := tx.Exec(e.ctx, `SELECT 1 FROM messages WHERE id = $1 FOR UPDATE`, heldID); err != nil {
		t.Fatal(err)
	}
	// Three requests for the pattern: two of the code's reasons, and one a tool
	// wrote with text in it, which is logged as "other".
	for _, reason := range []string{"alias added", "alias template changed", "SHED FIRE 3 FICTION ST"} {
		if err := e.st.RequestReparse(e.ctx, db.RequestReparseParams{CapcodePattern: capcode, Reason: reason}); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	type result struct {
		n   int
		err error
	}
	res := make(chan result, 1)
	go func() {
		n, err := e.worker.RunOnce(e.ctx)
		res <- result{n, err}
	}()

	var mid map[string]float64
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if mid = scrape(); mid["pagerarchive_reparse_run_messages_done"] == held-1 {
			break
		}
	}
	// A progress line while it's held: done stays at 1,499.
	time.Sleep(300 * time.Millisecond)
	mid = scrape()
	want("while it runs", mid, map[string]float64{"pagerarchive_reparse_running": 1, "pagerarchive_reparse_run_messages": pages,
		"pagerarchive_reparse_run_messages_done": held - 1, "pagerarchive_reparse_messages_total": held - 1})
	if at := mid["pagerarchive_reparse_run_started_timestamp_seconds"]; at < float64(started.Unix()-1) || at > float64(time.Now().Unix()) {
		t.Errorf("while it runs: started at %v, want about %d", at, started.Unix())
	}
	if err := tx.Rollback(e.ctx); err != nil {
		t.Fatal(err)
	}
	var r result
	select {
	case r = <-res:
	case <-time.After(2 * time.Minute):
		t.Fatal("the re-parse didn't finish within 2 minutes")
	}
	if r.err != nil || r.n != pages {
		t.Fatalf("RunOnce = %d, %v; want %d", r.n, r.err, pages)
	}
	after := scrape()
	want("after it", after, map[string]float64{"pagerarchive_reparse_running": 0, "pagerarchive_reparse_run_messages": pages,
		"pagerarchive_reparse_run_messages_done": pages, "pagerarchive_reparse_messages_total": pages})

	// Every message, as a full re-parse after a profile change does.
	var all int64
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM messages`).Scan(&all); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RequestReparse(e.ctx, db.RequestReparseParams{CapcodePattern: "%", Reason: "profiles changed"}); err != nil {
		t.Fatal(err)
	}
	if n, err := e.worker.RunOnce(e.ctx); err != nil || int64(n) != all {
		t.Fatalf("the full re-parse: %d, %v; want %d", n, err, all)
	}
	full := scrape()
	want("after the full re-parse", full, map[string]float64{"pagerarchive_reparse_running": 0, "pagerarchive_reparse_run_messages": float64(all),
		"pagerarchive_reparse_run_messages_done": float64(all), "pagerarchive_reparse_messages_total": float64(pages + all)})

	// The log: started, progress and finished, counts only.
	type line map[string]any
	var startedLines, progress, finished []line
	logs := e.logs.String()
	for _, l := range strings.Split(logs, "\n") {
		var m line
		if json.Unmarshal([]byte(l), &m) != nil {
			continue
		}
		switch m["msg"] {
		case "re-parse started":
			startedLines = append(startedLines, m)
		case "re-parse progress":
			progress = append(progress, m)
		case "re-parsed messages":
			finished = append(finished, m)
		}
	}
	if len(startedLines) != 2 || len(finished) != 2 {
		t.Fatalf("%d start lines and %d finished lines, want 2 of each", len(startedLines), len(finished))
	}
	s := startedLines[0]
	if s["reasons"] != "alias_added,alias_template_changed,other" || s["requests"] != 3.0 || s["patterns"] != 1.0 || s["total"] != float64(pages) {
		t.Errorf("the start line: %v", s)
	}
	if s := startedLines[1]; s["reasons"] != "profiles_changed" || s["total"] != float64(all) {
		t.Errorf("the full re-parse's start line: %v", s)
	}
	if f := finished[0]; f["count"] != float64(pages) || f["requests"] != 3.0 || f["seconds"] == nil || f["per_minute"] == nil {
		t.Errorf("the finished line: %v", f)
	}
	sawHeld := false
	for _, p := range progress {
		done, total := p["done"].(float64), p["total"].(float64)
		if total != pages && total != float64(all) || done > total || p["percent"] != float64(int64(done)*100/int64(total)) {
			t.Errorf("a progress line: %v", p)
		}
		if done > 0 && (p["per_minute"].(float64) <= 0 || p["eta_seconds"] == nil) {
			t.Errorf("a progress line with no rate or time left: %v", p)
		}
		if done == held-1 && total == pages {
			sawHeld = true
		}
	}
	if !sawHeld {
		t.Errorf("no progress line said %d of %d while the run was held (%d progress lines)", held-1, pages, len(progress))
	}
	for _, bad := range []string{"MADE-UP", capcode, "SHED", "FICTION"} {
		if strings.Contains(logs, bad) {
			t.Errorf("the log has %q", bad)
		}
	}

	// Over OTLP too: the same counts, through the allowlist (R70).
	var otlpStart, otlpProgress int
	deadline := time.Now().Add(10 * time.Second)
	for ; time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		otlpStart, otlpProgress = 0, 0
		for _, l := range col.Logs() {
			switch l.Body.GetStringValue() {
			case "re-parse started":
				if l.Attr("reasons") == "alias_added,alias_template_changed,other" && l.Attr("total") == strconv.Itoa(pages) {
					otlpStart++
				}
			case "re-parse progress":
				if l.Attr("done") != "" && l.Attr("total") != "" && l.Attr("percent") != "" && l.Attr("per_minute") != "" {
					otlpProgress++
				}
			}
		}
		if otlpStart > 0 && otlpProgress > 0 {
			break
		}
	}
	if otlpStart == 0 || otlpProgress == 0 {
		t.Errorf("over OTLP: %d start lines with the reasons and total, %d progress lines with their counts", otlpStart, otlpProgress)
	}
	if dump := col.Dump(); strings.Contains(dump, "SHED") || strings.Contains(dump, "MADE-UP") {
		t.Error("the exported telemetry has a page's text or a request's reason text")
	}
	t.Logf("A122: while held, /metrics said running %v, %v of %v done; after, running %v, %v of %v; the full re-parse %v of %v; %d progress lines, e.g. %v",
		mid["pagerarchive_reparse_running"], mid["pagerarchive_reparse_run_messages_done"], mid["pagerarchive_reparse_run_messages"],
		after["pagerarchive_reparse_running"], after["pagerarchive_reparse_run_messages_done"], after["pagerarchive_reparse_run_messages"],
		full["pagerarchive_reparse_run_messages_done"], full["pagerarchive_reparse_run_messages"], len(progress), progress[len(progress)/2])
}
