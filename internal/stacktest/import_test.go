package stacktest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/telemetry/telemetrytest"
)

// A76's file-based checks (ADR 0018), through the command as a deployment runs
// it: `docker compose exec -T api pagerarchive import-messages - < file`, on
// the compose file's API and database, with telemetry going to a collector in
// this test. Run by scripts/a76-checks.sh.

// Details the example network's rules remove, and text that must never be
// reported or logged.
var redactedDetails = []string{"0412 345 678", "0412345678", "AGE: 45", "SEX: M"}

const (
	a76Lines    = 10_000
	a76Pairs    = 20
	a76TooOld   = 5
	a76Refused  = 3
	a76Stored   = a76Lines - a76Pairs - a76TooOld - a76Refused
	refusedMark = "REFUSEDLINETEXT"
)

// a76Line is one line of the file, as JSON.
func a76Line(at time.Time, capcode, text, sourceID string) string {
	rec := map[string]string{"received_at": at.Format(time.RFC3339), "capcode": capcode, "text": text}
	if sourceID != "" {
		rec["source_id"] = sourceID
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// a76File makes A76's made-up file: 10,000 lines from the example network, with
// pages holding details the rules remove, 20 pairs of duplicates 3 seconds
// apart, 5 pages older than the retention period, a page with no source_id, and
// 3 malformed lines. After the malformed lines, the pages are in time order. The
// newest are moments old, so R51a would send them if a client had just heard them.
func a76File(now time.Time) []string {
	start := now.Add(-300 * 24 * time.Hour)
	var lines []string
	n := 0
	at := func() time.Time {
		n++
		return start.Add(time.Duration(n) * 37 * time.Minute)
	}
	// Three that aren't pages at all, holding text a careless error would echo.
	lines = append(lines,
		`{"received_at": "`+start.Format(time.RFC3339)+`", "capcode": "0100001", "text": "`+refusedMark+` 0412 345 678`,
		`{"received_at": "`+refusedMark+`", "capcode": "0100001", "text": "`+refusedMark+`"}`,
		refusedMark+` 0412 345 678 is not JSON`)
	for i := range a76TooOld {
		lines = append(lines, a76Line(now.AddDate(-11, 0, -i), "0100002", fmt.Sprintf("QDTOO OLD %d", i), fmt.Sprintf("old-%d", i)))
	}
	for i := range a76Pairs {
		t := at()
		text := fmt.Sprintf("@@EX%07d GRS3 GRASS FIRE %d @ SAMPLE RD, MADEUPTON #STN2", 2000+i, i)
		lines = append(lines, a76Line(t, "0100002", text, fmt.Sprintf("pair-%d-a", i)), a76Line(t.Add(3*time.Second), "0100002", text, fmt.Sprintf("pair-%d-b", i)))
	}
	lines = append(lines, a76Line(at(), "0100003", "QDA PAGE WITH NO SOURCE ID", ""))
	const recent = 50
	for i := 0; len(lines) < a76Lines-recent; i++ {
		capcode := fmt.Sprintf("010000%d", 1+i%4)
		var text string
		switch i % 5 {
		case 0:
			text = fmt.Sprintf("@@EX%07d HSF1 SMOKE %d CALL 0412 345 678 AGE: 45 SEX: M @ 12 FICTION ST, MADEUPTON #STN1", i, i)
		case 1:
			text = fmt.Sprintf("@@EX%07d GRS3 GRASS FIRE %d @ SAMPLE RD, MADEUPTON GRID 123456 UNITS P1 P2 #STN2", i, i)
		case 2:
			text = fmt.Sprintf("HbRE EX%07d UPDATE %d CONTACT 0412345678 #STN3", i-1, i)
		case 3:
			text = fmt.Sprintf("QDADMIN NOTICE %d", i)
		default:
			text = fmt.Sprintf("!!NO MARKER AND NO FORMAT %d", i)
		}
		lines = append(lines, a76Line(at(), capcode, text, fmt.Sprintf("page-%d", i)))
	}
	for i := range recent {
		lines = append(lines, a76Line(now.Add(time.Duration(i-recent)*time.Second), fmt.Sprintf("010000%d", 1+i%4), fmt.Sprintf("QDRECENT PAGE %d", i), fmt.Sprintf("recent-%d", i)))
	}
	return lines
}

type a76Stack struct {
	t         *testing.T
	ctx       context.Context
	db        *pgx.Conn
	api       string
	userB     string
	admin     string
	collector *telemetrytest.Collector
	hook      *a76Hook
}

// a76Hook is the test integration's webhook.
type a76Hook struct {
	n atomic.Int64
}

func (h *a76Hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	h.n.Add(1)
}

func a76Setup(t *testing.T) *a76Stack {
	t.Helper()
	dbURL := os.Getenv("PA_A76_DB_URL")
	if dbURL == "" {
		t.Skip("PA_A76_DB_URL is not set (run scripts/a76-checks.sh)")
	}
	ctx := context.Background()
	db, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(ctx) })
	s := &a76Stack{t: t, ctx: ctx, db: db, api: "http://127.0.0.1:8080", collector: telemetrytest.New(), hook: &a76Hook{},
		admin: "a76-admin-session", userB: "a76-b-session"}
	serve := func(port string, h http.Handler) {
		l, err := net.Listen("tcp", "0.0.0.0:"+port)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: h}
		go func() { _ = srv.Serve(l) }()
		t.Cleanup(func() { srv.Close() })
	}
	serve(os.Getenv("PA_TEST_COLLECTOR_PORT"), s.collector)
	serve("14322", s.hook)
	for _, sql := range []string{
		`INSERT INTO users (subject, email, email_key, name, role) VALUES ('a76-admin', 'a76-admin@example.test', 'a76-admin@example.test', 'A76 admin', 'admin')`,
		`INSERT INTO users (subject, email, email_key, name, role, invited_by) SELECT 'a76-b', 'b@example.test', 'b@example.test', 'User B', 'user', id FROM users WHERE subject = 'a76-admin'`,
		fmt.Sprintf(`INSERT INTO sessions (token_hash, user_id, expires_at) SELECT sha256(convert_to('%s', 'UTF8')), id, now() + interval '2 days' FROM users WHERE subject = 'a76-admin'`, s.admin),
		fmt.Sprintf(`INSERT INTO sessions (token_hash, user_id, expires_at) SELECT sha256(convert_to('%s', 'UTF8')), id, now() + interval '2 days' FROM users WHERE subject = 'a76-b'`, s.userB),
	} {
		s.exec(sql)
	}
	s.call(s.admin, http.MethodPut, "/api/v1/admin/integration-hosts", map[string]any{"entries": []string{"172.16.0.0/12"}}, nil)
	// Selecting every page in the file.
	s.call(s.userB, http.MethodPost, "/api/v1/integrations", map[string]any{
		"name": "Every page", "kind": "webhook", "content": "full", "capcodes": []string{"0100001", "0100002", "0100003", "0100004"},
		"filter": map[string]any{}, "config": map[string]any{}, "secrets": map[string]any{"url": "http://host.docker.internal:14322/hook"},
	}, nil)
	return s
}

func (s *a76Stack) exec(sql string, args ...any) {
	s.t.Helper()
	if _, err := s.db.Exec(s.ctx, sql, args...); err != nil {
		s.t.Fatal(err)
	}
}

func (s *a76Stack) int(sql string, args ...any) int64 {
	s.t.Helper()
	var n int64
	if err := s.db.QueryRow(s.ctx, sql, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *a76Stack) call(token, method, path string, body, out any) {
	s.t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, s.api+path, bytes.NewReader(data))
	req.AddCookie(&http.Cookie{Name: "pa_session", Value: token})
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		s.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			s.t.Fatal(err)
		}
	}
}

func compose(args ...string) *exec.Cmd {
	all := []string{"compose", "-f", "compose.yaml", "-f", "deploy/test/compose.yaml", "-f", "deploy/test/import.yaml", "-f", "deploy/test/telemetry.yaml"}
	cmd := exec.Command("docker", append(all, args...)...)
	cmd.Dir = "../.."
	return cmd
}

// a76Report is a run's report, as the command prints it.
type a76Report struct {
	stdout, stderr string
	counts         map[string]int64
	refused        map[int]string
}

var (
	reportLine  = regexp.MustCompile(`(?m)^(.+?)\s+(\d+)$`)
	refusedLine = regexp.MustCompile(`(?m)^line (\d+): (.+)$`)
)

func parseReport(stdout, stderr string) a76Report {
	r := a76Report{stdout: stdout, stderr: stderr, counts: map[string]int64{}, refused: map[int]string{}}
	for _, m := range refusedLine.FindAllStringSubmatch(stdout, -1) {
		n, _ := strconv.Atoi(m[1])
		r.refused[n] = m[2]
	}
	for _, m := range reportLine.FindAllStringSubmatch(stdout, -1) {
		n, _ := strconv.ParseInt(m[2], 10, 64)
		r.counts[m[1]] = n
	}
	return r
}

// run imports lines through the command, as docs/import.md says.
func (s *a76Stack) run(lines []string, flags ...string) a76Report {
	s.t.Helper()
	cmd := compose(append(append([]string{"exec", "-T", "api", "pagerarchive", "import-messages"}, flags...), "-")...)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		s.t.Fatalf("import-messages: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	return parseReport(stdout.String(), stderr.String())
}

func (s *a76Stack) wantCounts(name string, r a76Report, want map[string]int64) {
	s.t.Helper()
	for k, v := range want {
		if got, ok := r.counts[k]; !ok || got != v {
			s.t.Errorf("%s: %q is %d (reported: %v), want %d", name, k, got, ok, v)
		}
	}
	s.t.Logf("%s: %v", name, r.counts)
}

// noText fails if a text holds a redacted detail or a refused line's text.
func (s *a76Stack) noText(where, text string) {
	s.t.Helper()
	for _, d := range append([]string{refusedMark}, redactedDetails...) {
		if strings.Contains(text, d) {
			s.t.Errorf("%s holds %q", where, d)
		}
	}
}

// a76Result is what an import left, without IDs: every imported message and
// its copies, in a stable order, hashed.
func (s *a76Stack) result() string {
	s.t.Helper()
	var sum string
	if err := s.db.QueryRow(s.ctx, `
		SELECT md5(string_agg(row::text, E'\n' ORDER BY row::text)) FROM (
			SELECT m.capcode, m.text, m.priority, m.heartbeat, m.sent_at, m.dedup_key, m.format, m.job_number, m.event_type,
			       m.description, m.address, m.locality, m.grid_ref, m.paging_group, m.units, m.extra, m.parse_failed,
			       m.address_unsplit, m.alias_id, m.location, m.location_source, m.location_mismatch,
			       (SELECT string_agg(i.received_at::text || ' ' || COALESCE(i.source_id, '-'), ',' ORDER BY i.received_at, i.source_id)
			        FROM import_receptions i WHERE i.message_id = m.id) AS copies
			FROM messages m WHERE EXISTS (SELECT 1 FROM import_receptions i WHERE i.message_id = m.id)) row`).Scan(&sum); err != nil {
		s.t.Fatal(err)
	}
	return sum
}

// forget removes what the imports stored, for the next run to start again.
func (s *a76Stack) forget() {
	s.t.Helper()
	s.exec(`DELETE FROM messages m USING import_receptions i WHERE i.message_id = m.id`)
	if n := s.int(`SELECT count(*) FROM import_receptions`); n != 0 {
		s.t.Fatalf("%d copies left", n)
	}
}

func TestA76FileChecks(t *testing.T) {
	s := a76Setup(t)
	key := s.addClient("a76-receiver")
	live := func(text string) int64 {
		body, _ := json.Marshal(map[string]any{"capcode": "0100001", "text": text, "received_at": time.Now().UTC(), "baud": 1200})
		req, _ := http.NewRequest(http.MethodPost, s.api+"/api/v1/pages", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("live page: %v %v", err, resp)
		}
		defer resp.Body.Close()
		var out struct {
			MessageID int64 `json:"message_id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.MessageID
	}
	firstLive := live("QDA LIVE PAGE BEFORE THE IMPORT")
	wait := func(want int64, what string) {
		t.Helper()
		end := time.Now().Add(10 * time.Second)
		for s.hook.n.Load() < want && time.Now().Before(end) {
			time.Sleep(100 * time.Millisecond)
		}
		if got := s.hook.n.Load(); got != want {
			t.Errorf("%s: the integration got %d deliveries, want %d", what, got, want)
		}
	}
	wait(1, "the live page before the import") // the integration works
	var seenBefore time.Time
	if err := s.db.QueryRow(s.ctx, `SELECT last_seen_at FROM clients WHERE id = 'a76-receiver'`).Scan(&seenBefore); err != nil {
		t.Fatal(err)
	}
	stream := s.openStream("")
	messagesBefore := s.int(`SELECT count(*) FROM messages`)

	now := time.Now().UTC()
	sorted := a76File(now)
	shuffled := slices.Clone(sorted)
	rand.New(rand.NewPCG(76, 76)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	var wantRefused []int
	for i, l := range shuffled {
		if strings.Contains(l, refusedMark) {
			wantRefused = append(wantRefused, i+1)
		}
	}
	if len(sorted) != a76Lines || len(wantRefused) != a76Refused {
		t.Fatalf("the file: %d lines, %d malformed", len(sorted), len(wantRefused))
	}
	wantReport := map[string]int64{"lines read": a76Lines, "duplicates merged": a76Pairs, "pages stored": a76Stored,
		"skipped by retention": a76TooOld, "refused lines": a76Refused}
	checkRefused := func(name string, r a76Report) {
		t.Helper()
		var got []int
		for n := range r.refused {
			got = append(got, n)
		}
		slices.Sort(got)
		if !slices.Equal(got, wantRefused) {
			t.Errorf("%s: refused lines %v, want %v", name, got, wantRefused)
		}
		s.noText(name+"'s report", r.stdout)
		s.noText(name+"'s progress", r.stderr)
	}

	// The dry run: nothing stored, and the 3 malformed lines by number and reason.
	dry := s.run(shuffled, "--dry-run")
	s.wantCounts("dry run", dry, map[string]int64{"lines read": a76Lines, "duplicates that would be merged": a76Pairs, "pages that would be stored": a76Stored,
		"skipped by retention": a76TooOld, "refused lines": a76Refused})
	checkRefused("the dry run", dry)
	t.Logf("the dry run's refused lines: %v", dry.refused)
	if n := s.int(`SELECT count(*) FROM messages`); n != messagesBefore {
		t.Errorf("the dry run stored %d messages", n-messagesBefore)
	}
	if n := s.int(`SELECT count(*) FROM import_receptions`); n != 0 {
		t.Errorf("the dry run stored %d copies", n)
	}

	// The import: 9,972 stored, nothing redacted anywhere, and nobody told.
	first := s.run(shuffled)
	s.wantCounts("the import", first, wantReport)
	checkRefused("the import", first)
	if n := s.int(`SELECT count(*) FROM messages`) - messagesBefore; n != a76Stored {
		t.Errorf("%d pages stored, want %d", n, a76Stored)
	}
	for _, d := range redactedDetails {
		if n := s.int(`SELECT count(*) FROM messages m WHERE m::text ILIKE $1`, "%"+d+"%"); n != 0 {
			t.Errorf("%d stored pages hold %q", n, d)
		}
		if n := s.int(`SELECT count(*) FROM import_receptions i WHERE i::text ILIKE $1`, "%"+d+"%"); n != 0 {
			t.Errorf("%d import rows hold %q", n, d)
		}
	}
	if n := s.int(`SELECT count(*) FROM messages WHERE text LIKE '%REDACTED%'`); n < 1000 {
		t.Errorf("%d stored pages hold REDACTED: the file's details aren't there", n)
	}
	if n := s.int(`SELECT count(*) FROM deliveries`); n != 1 {
		t.Errorf("%d deliveries queued, want the live page's 1", n)
	}
	time.Sleep(6 * time.Second) // the delivery worker has had its chance
	if got := s.hook.n.Load(); got != 1 {
		t.Errorf("the integration got %d deliveries, want the live page's 1", got)
	}
	var seenAfter time.Time
	if err := s.db.QueryRow(s.ctx, `SELECT last_seen_at FROM clients WHERE id = 'a76-receiver'`).Scan(&seenAfter); err != nil {
		t.Fatal(err)
	}
	if !seenAfter.Equal(seenBefore) {
		t.Errorf("the client's last-seen time moved from %v to %v", seenBefore, seenAfter)
	}
	listed, err := compose("exec", "-T", "api", "pagerarchive", "client", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("client list: %v %s", err, listed)
	}
	if strings.Contains(string(listed), "import") {
		t.Errorf("the clients list names import:\n%s", listed)
	}
	metrics := s.scrape()
	if !strings.Contains(metrics, `client="a76-receiver"`) || strings.Contains(metrics, `client="import"`) {
		t.Errorf("the metrics should name the receiver and not import")
	}
	var one struct {
		Receptions []struct {
			Client string `json:"client"`
		} `json:"receptions"`
	}
	var pairID int64
	if err := s.db.QueryRow(s.ctx, `SELECT id FROM messages WHERE text LIKE '%GRASS FIRE 0 @%' AND capcode = '0100002'`).Scan(&pairID); err != nil {
		t.Fatal(err)
	}
	s.call(s.admin, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", pairID), nil, &one)
	if len(one.Receptions) != 2 || one.Receptions[0].Client != "import" {
		t.Errorf("a merged pair's receptions: %+v, want import twice", one.Receptions)
	}
	var sent, firstSeen time.Time
	if err := s.db.QueryRow(s.ctx, `SELECT sent_at, (SELECT min(received_at) FROM import_receptions WHERE message_id = id) FROM messages WHERE id = $1`, pairID).Scan(&sent, &firstSeen); err != nil {
		t.Fatal(err)
	}
	if !sent.Equal(firstSeen) {
		t.Errorf("sent at %v, want the first copy's time %v (R21)", sent, firstSeen)
	}
	// A watch catching up from before the import hears of none of it.
	if count := s.catchUpCount(firstLive); count != 0 {
		t.Errorf("a watch catching up after the import got %d messages", count)
	}

	// Run again: nothing new.
	want := s.result()
	again := s.run(shuffled)
	s.wantCounts("the second run", again, map[string]int64{"lines read": a76Lines, "pages stored": 0, "duplicates merged": 0,
		"already imported": a76Stored + a76Pairs, "skipped by retention": a76TooOld, "refused lines": a76Refused})
	if n := s.int(`SELECT count(*) FROM messages`) - messagesBefore; n != a76Stored {
		t.Errorf("after the second run: %d pages, want %d", n, a76Stored)
	}
	if got := s.result(); got != want {
		t.Error("the second run changed the result")
	}

	// The same source IDs, with other times and text: source_id decides (R83c).
	var changed []string
	for i := range 100 {
		changed = append(changed, a76Line(now.Add(-time.Duration(i)*time.Hour), "0100004", fmt.Sprintf("QDCORRECTED %d", i), fmt.Sprintf("page-%d", i)))
	}
	corrected := s.run(changed)
	s.wantCounts("a corrected export", corrected, map[string]int64{"pages stored": 0, "already imported": 100})
	if got := s.result(); got != want {
		t.Error("source IDs seen before stored something new")
	}

	// In file order rather than shuffled: the same result.
	s.forget()
	inOrder := s.run(sorted)
	s.wantCounts("the file in time order", inOrder, wantReport)
	if got := s.result(); got != want {
		t.Error("the file in time order gives a different result from the shuffled file")
	}

	// Nothing yet has reached the open list.
	select {
	case id := <-stream:
		t.Errorf("the open list got message %d from an import", id)
	default:
	}

	// Killed part-way, then run again: the same result as one full run. The kill
	// takes the API with it, and so its open list.
	s.forget()
	stored := s.killPartWay(shuffled)
	t.Logf("killed part-way with %d copies stored, of %d", stored, a76Stored+a76Pairs)
	if stored == 0 || stored >= a76Stored+a76Pairs {
		t.Errorf("the kill left %d copies: not part-way", stored)
	}
	rerun := s.run(shuffled)
	s.wantCounts("the run after the kill", rerun, map[string]int64{"lines read": a76Lines, "skipped by retention": a76TooOld, "refused lines": a76Refused})
	if got := s.result(); got != want {
		t.Error("killed and run again gives a different result from one full run")
	}
	if n := s.int(`SELECT count(*) FROM messages`) - messagesBefore; n != a76Stored {
		t.Errorf("after the kill and the rerun: %d pages, want %d", n, a76Stored)
	}

	// None of it reached a log or the exported telemetry, which is really on.
	if got := s.hook.n.Load(); got != 1 {
		t.Errorf("after every run the integration got %d deliveries, want the live page's 1", got)
	}
	stream = s.openStream("")
	lastLive := live("QDA LIVE PAGE AFTER THE IMPORT")
	wait(2, "the live page after the import")
	select {
	case id := <-stream:
		if id != lastLive {
			t.Errorf("the open list got message %d, want only the live page, %d", id, lastLive)
		}
	case <-time.After(5 * time.Second):
		t.Error("the open list didn't get the live page")
	}
	select {
	case id := <-stream:
		t.Errorf("the open list also got message %d", id)
	case <-time.After(time.Second):
	}
	logs, err := compose("logs", "--no-color", "api", "db").CombinedOutput()
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	s.noText("the API's or database's log", string(logs))
	end := time.Now().Add(30 * time.Second)
	for time.Now().Before(end) {
		if spans, _, _ := s.collector.Records(); spans > 0 {
			break
		}
		time.Sleep(time.Second)
	}
	spans, logRecords, points := s.collector.Records()
	t.Logf("telemetry exported: %d spans, %d log records, %d metric points; API and database log %d bytes", spans, logRecords, points, len(logs))
	if spans == 0 {
		t.Error("no telemetry arrived: the check is empty")
	}
	s.noText("the exported telemetry", s.collector.Dump())
}

func (s *a76Stack) addClient(id string) string {
	s.t.Helper()
	out, err := compose("exec", "-T", "api", "pagerarchive", "client", "add", id).CombinedOutput()
	if err != nil {
		s.t.Fatalf("client add: %v %s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return lines[len(lines)-1]
}

func (s *a76Stack) scrape() string {
	s.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, s.api+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("METRICS_TOKEN"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("metrics: %d", resp.StatusCode)
	}
	return string(b)
}

// openStream opens an open list as user B; the channel gets each message's ID.
func (s *a76Stack) openStream(query string) <-chan int64 {
	s.t.Helper()
	ctx, cancel := context.WithCancel(s.ctx)
	s.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/api/v1/stream?"+query, nil)
	req.AddCookie(&http.Cookie{Name: "pa_session", Value: s.userB})
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		s.t.Fatalf("stream: %v %v", err, resp)
	}
	out := make(chan int64, 1024)
	ready := make(chan struct{})
	var once sync.Once
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		message := false
		for sc.Scan() {
			switch line := sc.Text(); {
			case line == ": connected":
				once.Do(func() { close(ready) })
			case line == "event: message":
				message = true
			case line == "":
				message = false
			case message && strings.HasPrefix(line, "data: "):
				var m struct {
					ID int64 `json:"id"`
				}
				if json.Unmarshal([]byte(line[len("data: "):]), &m) == nil {
					out <- m.ID
				}
			}
		}
	}()
	<-ready
	return out
}

// catchUpCount asks for what was stored after a message, as the PowerShell
// module's watch does when it reconnects (ADR 0010 §3).
func (s *a76Stack) catchUpCount(after int64) int {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/stream?after_id=%d", s.api, after), nil)
	req.AddCookie(&http.Cookie{Name: "pa_session", Value: s.userB})
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		s.t.Fatalf("catch-up: %v %v", err, resp)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	event := ""
	for sc.Scan() {
		switch line := sc.Text(); {
		case strings.HasPrefix(line, "event: "):
			event = line[len("event: "):]
		case event == "catch-up" && strings.HasPrefix(line, "data: "):
			var c struct {
				Count int `json:"count"`
			}
			if err := json.Unmarshal([]byte(line[len("data: "):]), &c); err != nil {
				s.t.Fatal(err)
			}
			return c.Count
		}
	}
	s.t.Fatal("no catch-up event")
	return 0
}

// killPartWay starts an import and kills it once some batches are in, by
// stopping the API's container, which takes everything running in it. The
// container comes back as a deployment's would. It returns the copies left.
func (s *a76Stack) killPartWay(lines []string) int64 {
	s.t.Helper()
	cmd := compose("exec", "-T", "api", "pagerarchive", "import-messages", "-")
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	end := time.Now().Add(2 * time.Minute)
	for s.int(`SELECT count(*) FROM import_receptions`) < 2000 {
		if time.Now().After(end) {
			s.t.Fatalf("the import didn't get to 2,000 copies:\n%s", output.String())
		}
		select {
		case err := <-done:
			s.t.Fatalf("the import ended before the kill: %v\n%s", err, output.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if out, err := compose("kill", "-s", "SIGKILL", "api").CombinedOutput(); err != nil {
		s.t.Fatalf("kill: %v %s", err, out)
	}
	select {
	case <-done:
	case <-time.After(time.Minute):
		s.t.Fatal("the command didn't end when its container was killed")
	}
	s.noText("the killed run's output", output.String())
	if out, err := compose("start", "api").CombinedOutput(); err != nil {
		s.t.Fatalf("start: %v %s", err, out)
	}
	if err := waitFor(2*time.Minute, func() bool {
		resp, err := http.Get(s.api + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}); err != nil {
		s.t.Fatal("the API didn't come back")
	}
	return s.int(`SELECT count(*) FROM import_receptions`)
}

func waitFor(d time.Duration, cond func() bool) error {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("timed out")
}
