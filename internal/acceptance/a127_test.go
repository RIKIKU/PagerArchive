package acceptance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// queryLog is a pgx tracer that records the SQL of each query made for an
// HTTP request (its context is the request's) while it's on, and passes
// every query on to next.
type queryLog struct {
	next pgx.QueryTracer

	mu  sync.Mutex
	on  bool
	sql []string

	// hook, if set, sees every query before it runs, and gives the context it
	// runs with (A129).
	hook func(ctx context.Context, sql string) context.Context
}

func (q *queryLog) TraceQueryStart(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if q.next != nil {
		ctx = q.next.TraceQueryStart(ctx, c, data)
	}
	q.mu.Lock()
	hook := q.hook
	q.mu.Unlock()
	if hook != nil {
		ctx = hook(ctx, data.SQL)
	}
	if ctx.Value(http.ServerContextKey) != nil {
		q.mu.Lock()
		if q.on {
			q.sql = append(q.sql, data.SQL)
		}
		q.mu.Unlock()
	}
	return ctx
}

func (q *queryLog) TraceQueryEnd(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryEndData) {
	if q.next != nil {
		q.next.TraceQueryEnd(ctx, c, data)
	}
}

// start records requests' queries from now, and stop ends that and returns them.
func (q *queryLog) start() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.on, q.sql = true, nil
}

func (q *queryLog) stop() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.on = false
	return q.sql
}

// countFlags counts the flagged messages for /metrics in the background,
// every every, as serve does (R52).
func (e *env) countFlags(every time.Duration) {
	e.srv.Flagged = &server.FlagCounts{Store: e.st, Log: e.srv.Log, Every: every}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.srv.Flagged.Run(ctx); close(done) }()
	e.t.Cleanup(func() { cancel(); <-done })
}

// scrape gets /metrics with the token, and how long it took.
func (e *env) scrape() (string, time.Duration) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.url+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+metricsToken)
	start := time.Now()
	// A scrape that waits for messages' lock fails here, rather than hanging.
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	took := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("scrape: %d %s", resp.StatusCode, b)
	}
	return string(b), took
}

// sampleValue is the value of the sample named series (with its labels) in
// body, or "" if there's none.
func sampleValue(body, series string) string {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, series+" "); ok {
			return v
		}
	}
	return ""
}

const countedAt = "pagerarchive_messages_flagged_counted_timestamp_seconds"

func flagged(body string) string {
	return fmt.Sprintf("%s %s %s",
		sampleValue(body, `pagerarchive_messages_flagged{flag="parse_failed"}`),
		sampleValue(body, `pagerarchive_messages_flagged{flag="address_unsplit"}`),
		sampleValue(body, `pagerarchive_messages_flagged{flag="location_mismatch"}`))
}

// A127. A scrape doesn't count the archive.
func TestA127ScrapeDoesNotCountArchive(t *testing.T) {
	e := setup(t)
	var ids []int64
	for i := 1; i <= 8; i++ {
		ids = append(ids, e.mustPost("a", "0100001", fmt.Sprintf("MADE UP TEST PAGE NUMBER %d", i), e.now.Add(time.Duration(-i)*time.Minute)).MessageID)
	}
	// The fixture's flags, set by hand: 3 parse_failed, 2 address_unsplit,
	// 1 location_mismatch.
	flag := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.st.Pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	flag(`UPDATE messages SET parse_failed = false, address_unsplit = false, location_mismatch = false`)
	flag(`UPDATE messages SET parse_failed = true WHERE id = ANY($1)`, ids[0:3])
	flag(`UPDATE messages SET address_unsplit = true WHERE id = ANY($1)`, ids[3:5])
	flag(`UPDATE messages SET location_mismatch = true WHERE id = $1`, ids[5])

	// Before the first count: the other metrics, without the counts.
	body, _ := e.scrape()
	if !strings.Contains(body, `pagerarchive_pages_received_total{outcome="stored"} 8`) {
		t.Errorf("before the first count, the scrape has no stored pages:\n%s", body)
	}
	if strings.Contains(body, "pagerarchive_messages_flagged") {
		t.Errorf("before the first count, the scrape has the flagged counts or their time")
	}

	e.queries.start()
	e.countFlags(time.Second)
	if !eventually(time.Second, func() bool { b, _ := e.scrape(); return flagged(b) == "3 2 1" }) {
		b, _ := e.scrape()
		t.Fatalf("flagged counts %q within a second, want 3 2 1", flagged(b))
	}
	body, _ = e.scrape()
	first := sampleValue(body, countedAt)
	if first == "" || !strings.Contains(body, "# TYPE "+countedAt+" gauge") {
		t.Fatalf("no counted time:\n%s", body)
	}
	t.Logf("counted at %s: %s", first, flagged(body))

	flag(`UPDATE messages SET parse_failed = true WHERE id = ANY($1)`, ids[6:8])
	var later string
	if !eventually(1500*time.Millisecond, func() bool {
		b, _ := e.scrape()
		later = sampleValue(b, countedAt)
		return flagged(b) == "5 2 1" && after(later, first)
	}) {
		b, _ := e.scrape()
		t.Fatalf("after 2 more parse_failed: %q at %s, want 5 2 1 after %s", flagged(b), sampleValue(b, countedAt), first)
	}

	// A count that can't finish: the scrapes still answer at once, with the
	// last counts and their time, and the failed count is a WARN.
	tx, err := e.st.Pool.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A scrape that fails while it's held mustn't leave the lock behind.
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(e.ctx, `LOCK TABLE messages IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	b, _ := e.scrape()
	held := sampleValue(b, countedAt)
	end := time.Now().Add(3 * time.Second)
	for n := 0; time.Now().Before(end); n++ {
		b, took := e.scrape()
		if took > time.Second {
			t.Errorf("scrape %d while messages is locked took %s", n, took)
		}
		if flagged(b) != "5 2 1" || sampleValue(b, countedAt) != held {
			t.Errorf("scrape %d while messages is locked: %q at %s, want 5 2 1 at %s", n, flagged(b), sampleValue(b, countedAt), held)
		}
		time.Sleep(200 * time.Millisecond)
	}
	warned := regexp.MustCompile(`"level":"WARN","msg":"metrics: the flagged messages couldn't be counted; the last counts are kept","error":"[^"]+","counted_at":"[^"]+"`)
	if !eventually(time.Second, func() bool { return warned.MatchString(e.logs.String()) }) {
		t.Errorf("no WARN for the failed count:\n%s", e.logs.String())
	}
	if err := tx.Rollback(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !eventually(2*time.Second, func() bool { b, _ := e.scrape(); return after(sampleValue(b, countedAt), held) }) {
		t.Errorf("the counted time didn't move on after the lock went (still %s)", held)
	}
	for _, line := range strings.Split(e.logs.String(), "\n") {
		if strings.Contains(line, "flagged messages couldn't be counted") {
			t.Logf("%s", line)
			break
		}
	}

	// No query made for any scrape read messages; the log saw the scrapes'
	// other queries.
	queries := e.queries.stop()
	readsMessages := regexp.MustCompile(`(?i)\bmessages\b`)
	clients := 0
	for _, q := range queries {
		if readsMessages.MatchString(q) {
			t.Errorf("a scrape queried messages: %s", q)
		}
		if strings.Contains(q, "FROM clients") {
			clients++
		}
	}
	if clients == 0 {
		t.Errorf("the query log saw no scrape's queries (%d queries)", len(queries))
	}
	t.Logf("%d queries for scrapes, none reading messages", len(queries))

	// Autovacuum's settings for messages (R52, R91a).
	var opts []string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT reloptions FROM pg_class WHERE oid = 'messages'::regclass`).Scan(&opts); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(opts, ",")
	for _, want := range []string{"autovacuum_vacuum_max_threshold=50000", "autovacuum_analyze_scale_factor=0.02"} {
		if !strings.Contains(got, want) {
			t.Errorf("messages' settings %q, want %s", got, want)
		}
	}
}

// after is whether the time a is after b (both a sample's value).
func after(a, b string) bool {
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	return err1 == nil && err2 == nil && x > y
}
