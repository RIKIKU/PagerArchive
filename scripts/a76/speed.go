//go:build ignore

// A76's speed (R83f), for scripts/a76.sh: pages made up as they're streamed to
// `pagerarchive import-messages -`, while a client posts live pages, then A75's
// searches. It prints the rate over time, the live pages before and during the
// import, and the searches, and exits non-zero if the import is under 500 pages
// a second, a live page is late (R32, R51a) or lost, or a search after the
// import is over 1 second (R33b). Searches during the import aren't timed
// (R83f). The memory budget is scripts/a76.sh's: it samples docker stats.
//
//	usage: go run scripts/a76/speed.go -db URL -api URL -key KEY -seed N -pages N [-sample FILE] -o DIR -- docker compose ... exec -T api pagerarchive import-messages -
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RIKIKU/PagerArchive/internal/seeddata"
)

var failed atomic.Bool

func fail(format string, args ...any) {
	failed.Store(true)
	fmt.Printf("FAIL: "+format+"\n", args...)
}

func die(err error) {
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

func main() {
	dbURL := flag.String("db", "", "database URL")
	api := flag.String("api", "http://127.0.0.1:8080", "API URL")
	key := flag.String("key", "", "the live client's API key")
	seed := flag.Int("seed", seeddata.A75Pages, "pages seeded first, as A75 does")
	pages := flag.Int("pages", 200_000, "pages imported")
	shuffle := flag.Bool("shuffle", false, "send the pages in random order (held in memory first)")
	sample := flag.String("sample", "", "addresses to use, NUMBER STREET|LOCALITY per line")
	out := flag.String("o", ".", "where the results go")
	every := flag.Duration("every", 5*time.Second, "how often the client posts a live page")
	hookAddr := flag.String("hook", "0.0.0.0:14321", "where the test integration's webhook listens")
	hookURL := flag.String("hook-url", "http://host.docker.internal:14321/hook", "the webhook as the API reaches it")
	hostRange := flag.String("hook-hosts", "172.16.0.0/12", "the allow list entry that lets the API reach it")
	phase := flag.String("phase", "", "a file to write \"import\" in when the import starts, for the memory samples")
	flag.Parse()
	command := flag.Args()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dbURL)
	die(err)
	defer pool.Close()

	admin, userB := tokens(ctx, pool)
	if *seed > 0 {
		start := time.Now()
		seeded, built, err := seeddata.Seed(ctx, pool, *seed)
		die(err)
		fmt.Printf("seeded %d pages in %s, built the search index in %s, with VACUUM ANALYZE %s\n", *seed, seeded.Round(time.Second), built.Round(time.Second), time.Since(start).Round(time.Second))
	}
	var before int64
	die(pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&before))

	// The test integration: a webhook selecting Station 1, every page of
	// which the import also holds, so a delivery of one would show.
	hook := startHook(*hookAddr)
	call(*api, admin, http.MethodPut, "/api/v1/admin/integration-hosts", map[string]any{"entries": []string{*hostRange}})
	call(*api, userB, http.MethodPost, "/api/v1/integrations", map[string]any{
		"name": "A76", "kind": "webhook", "content": "full", "capcodes": []string{"0100001"},
		"filter": map[string]any{}, "config": map[string]any{}, "secrets": map[string]any{"url": *hookURL},
	})
	sse := startStream(ctx, *api, userB)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	live := &liveLog{hook: hook, sse: sse}
	go func() { defer wg.Done(); live.loop(*api, *key, *every, stop) }()
	// A minute of live pages with no import, to compare with.
	time.Sleep(time.Minute)
	live.importStarts()

	if *phase != "" {
		die(os.WriteFile(*phase, []byte("import\n"), 0o644))
	}
	rates := runImport(command, *pages, *shuffle, *sample, *out)
	close(stop)
	wg.Wait()
	time.Sleep(10 * time.Second) // the last live page's delivery

	var after, copies int64
	var sizes string
	die(pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&after))
	die(pool.QueryRow(ctx, `SELECT count(*) FROM import_receptions`).Scan(&copies))
	die(pool.QueryRow(ctx, `SELECT 'messages with its indexes ' || pg_size_pretty(pg_total_relation_size('messages')) ||
		', of which the search index ' || pg_size_pretty(pg_relation_size('messages_search_trgm_idx')) ||
		'; import_receptions with its indexes ' || pg_size_pretty(pg_total_relation_size('import_receptions')) ||
		'; the database ' || pg_size_pretty(pg_database_size(current_database()))`).Scan(&sizes))
	fmt.Printf("messages before %d, after %d; import receptions %d\n%s\n", before, after, copies, sizes)
	rates.print()
	live.print()
	var queued int64
	die(pool.QueryRow(ctx, `SELECT count(*) FROM deliveries`).Scan(&queued))
	if int(queued) != live.posted() {
		fail("%d deliveries queued for %d live pages: the import queued some (R83b)", queued, live.posted())
	}
	checkSearches(ctx, pool, *api, userB)
	if failed.Load() {
		os.Exit(1)
	}
	fmt.Println("A76 speed: PASS")
}

// tokens makes an admin and user B, each with a session, as scripts/e2e.sh
// seeds them, and the standard fixture's aliases if they aren't there.
func tokens(ctx context.Context, pool *pgxpool.Pool) (admin, userB string) {
	admin, userB = "a76-admin-"+randomHex(), "a76-user-b-"+randomHex()
	for _, sql := range []string{
		`INSERT INTO users (subject, email, email_key, name, role) VALUES ('a76-admin', 'a76-admin@example.test', 'a76-admin@example.test', 'A76 admin', 'admin')`,
		`INSERT INTO users (subject, email, email_key, name, role, invited_by) SELECT 'a76-b', 'b@example.test', 'b@example.test', 'User B', 'user', id FROM users WHERE subject = 'a76-admin'`,
		fmt.Sprintf(`INSERT INTO sessions (token_hash, user_id, expires_at) SELECT sha256(convert_to('%s', 'UTF8')), id, now() + interval '2 days' FROM users WHERE subject = 'a76-admin'`, admin),
		fmt.Sprintf(`INSERT INTO sessions (token_hash, user_id, expires_at) SELECT sha256(convert_to('%s', 'UTF8')), id, now() + interval '2 days' FROM users WHERE subject = 'a76-b'`, userB),
	} {
		_, err := pool.Exec(ctx, sql)
		die(err)
	}
	return admin, userB
}

func randomHex() string { return fmt.Sprintf("%016x", rand.Uint64()) }

func call(api, token, method, path string, body any) {
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, api+path, bytes.NewReader(data))
	req.AddCookie(&http.Cookie{Name: "pa_session", Value: token})
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	die(err)
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		die(fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, b))
	}
}

// page makes one made-up page in the example formats, with A75's phrases, so
// A75's searches find them whatever was seeded. 1 in 200 holds details the
// redaction rules remove. It returns a JSON line.
func pageLine(r *rand.Rand, n int, at time.Time, addrs [][2]string) []byte {
	capcode := fmt.Sprintf("010000%d", 1+n%4)
	a := addrs[r.IntN(len(addrs))]
	phrases := []string{"SMOKE FROM ROOF", "CHECK HOSES TODAY", "GRASS FIRE", "STRUCTURE FIRE, SMOKE SHOWING", "ROUTINE PUMP CHECK", "MEET AT THE STATION"}
	phrase := phrases[n%6]
	text := ""
	switch k := r.IntN(10); {
	case k < 6:
		types := []string{"HSF1", "GRS3", "MED1", "ALM2"}
		grid := ""
		if r.IntN(3) == 0 {
			grid = fmt.Sprintf(" GRID %03d%03d", r.IntN(1000), r.IntN(1000))
		}
		if r.IntN(200) == 0 {
			phrase += " CALL 0412 345 678 AGE: 45"
		}
		text = fmt.Sprintf("@@EX%07d %s %s %d @ %s, %s%s UNITS T%d #STN%d", n%10_000_000, types[r.IntN(len(types))], phrase, n, a[0], a[1], grid, r.IntN(9), 1+n%4)
	case k < 8:
		text = fmt.Sprintf("HbRE EX%07d %s %d #STN%d", n%10_000_000, phrase, n, 1+n%4)
	case k < 9:
		text = fmt.Sprintf("QD%s %d #STN%d", phrase, n, 1+n%4)
	default:
		if r.IntN(2) == 0 {
			capcode, text = "0000050", "QDHEARTBEAT"
		} else {
			capcode, text = fmt.Sprintf("0200%03d", r.IntN(1000)), fmt.Sprintf("%s %d", phrase, n)
		}
	}
	line, _ := json.Marshal(map[string]string{
		"received_at": at.Format(time.RFC3339), "capcode": capcode, "text": text, "source_id": "a76-" + strconv.Itoa(n),
	})
	return append(line, '\n')
}

func readSample(path string) [][2]string {
	addrs := [][2]string{{"12 FICTION ST", "MADEUPTON"}, {"3 FICTION ST", "MADEUPTON"}, {"SAMPLE RD", "MADEUPTON"}, {"4 DEMO PL", "MADEUPTON"}, {"7 EXAMPLE ST", "MADEUPTON"}}
	if path == "" {
		return addrs
	}
	f, err := os.Open(path)
	die(err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if a, l, ok := strings.Cut(sc.Text(), "|"); ok {
			addrs = append(addrs, [2]string{a, l})
		}
	}
	return addrs
}

type rateSample struct {
	at   time.Duration
	done int64
}

type rates struct {
	start, stored time.Time
	read, sorted  time.Duration
	total         time.Duration
	samples       []rateSample
	pages         int
	report        []byte
}

var doneRe = regexp.MustCompile(`^(\d+) of (\d+) pages`)

// runImport runs the command, streaming the pages to its standard input, and
// notes how far it has got from its progress lines.
func runImport(command []string, pages int, shuffle bool, sample, out string) *rates {
	addrs := readSample(sample)
	cmd := exec.Command(command[0], command[1:]...)
	stdin, err := cmd.StdinPipe()
	die(err)
	stderr, err := cmd.StderrPipe()
	die(err)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	rt := &rates{start: time.Now(), pages: pages}
	die(cmd.Start())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			elapsed := time.Since(rt.start)
			fmt.Printf("[%6.0fs] %s\n", elapsed.Seconds(), line)
			switch {
			case strings.HasPrefix(line, "read ") && strings.Contains(line, "indexing"):
				rt.read = elapsed
			case strings.HasPrefix(line, "indexed "):
				rt.sorted = elapsed
			default:
				if m := doneRe.FindStringSubmatch(line); m != nil {
					n, _ := strconv.ParseInt(m[1], 10, 64)
					rt.samples = append(rt.samples, rateSample{elapsed, n})
				}
			}
		}
	}()
	go func() {
		defer stdin.Close()
		w := bufio.NewWriterSize(stdin, 1<<20)
		defer w.Flush()
		r := rand.New(rand.NewPCG(76, 83))
		// Evenly over 3,640 days, inside the default retention, ending an hour ago.
		span := 3640 * 24 * time.Hour
		end := time.Now().Add(-time.Hour)
		var lines [][]byte
		for i := 0; i < pages; i++ {
			at := end.Add(-span + time.Duration(float64(span)*float64(i)/float64(pages)))
			line := pageLine(r, i, at, addrs)
			if shuffle {
				lines = append(lines, line)
			} else if _, err := w.Write(line); err != nil {
				return
			}
		}
		r.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
		for _, line := range lines {
			if _, err := w.Write(line); err != nil {
				return
			}
		}
	}()
	<-done
	err = cmd.Wait()
	rt.total = time.Since(rt.start)
	rt.report = stdout.Bytes()
	die(os.WriteFile(out+"/a76-report.txt", rt.report, 0o644))
	if err != nil {
		fail("the import failed: %v", err)
	}
	return rt
}

func (rt *rates) print() {
	fmt.Printf("--- the import's report\n%s", rt.report)
	fmt.Printf("--- rate\nread %s, indexed by %s, finished at %s\n", rt.read.Round(time.Second), rt.sorted.Round(time.Second), rt.total.Round(time.Second))
	overall := float64(rt.pages) / rt.total.Seconds()
	storing := float64(rt.pages) / (rt.total - rt.sorted).Seconds()
	fmt.Printf("%d pages: %.0f a second overall, %.0f a second while storing\n", rt.pages, overall, storing)
	// The rate in each tenth of the storing stage.
	for i := 1; i <= 10; i++ {
		var from, to *rateSample
		for j := range rt.samples {
			s := &rt.samples[j]
			if s.done*10 >= int64(rt.pages)*int64(i-1) && from == nil {
				from = s
			}
			if s.done*10 <= int64(rt.pages)*int64(i) {
				to = s
			}
		}
		if from != nil && to != nil && to.at > from.at {
			fmt.Printf("  %3d%%-%3d%%: %5.0f a second\n", 10*(i-1), 10*i, float64(to.done-from.done)/(to.at-from.at).Seconds())
		}
	}
	if overall < 500 {
		fail("%.0f pages a second, under 500 (R83f)", overall)
	}
}

// A75's searches (R33b), as TestA75SearchSpeed runs them, as user B.
var a75Searches = []struct{ name, query string }{
	{"one term that matches nothing", "q=" + url.QueryEscape("ZZNOTHING")},
	{"three terms that together match nothing", "q=" + url.QueryEscape("hoses grass pump")},
	{`"Smoke showing" or:"grass fire"`, "q=" + url.QueryEscape(`"Smoke showing" or:"grass fire"`)},
	{"a term found only in an extra field", "q=" + url.QueryEscape("tanker9")},
	{`a term with alias:"Station 1"`, "q=12345&alias=" + url.QueryEscape("Station 1")},
	{`alias:"Station 1", a term never in its pages`, "q=STN2&alias=" + url.QueryEscape("Station 1")},
	{"the same with alias:sta", "q=STN2&alias=sta"},
	{`the same with "Station 1" picked`, "q=STN2&alias_like=" + url.QueryEscape("Station 1")},
	{`a common term with alias:"Station 5", the busy alias`, "q=fire&alias=" + url.QueryEscape("Station 5")},
	{"a map reference typed without quotes", "q=" + url.QueryEscape("m 23 b4")},
}

// checkSearches times A75's searches once the import has ended: straight away,
// as the import ends with ANALYZE, so nothing has to catch up first (ADR 0018).
// Searches during an import aren't timed (R83f).
func checkSearches(ctx context.Context, pool *pgxpool.Pool, api, token string) {
	var analyzed, autovacuumed string
	var changed int64
	die(pool.QueryRow(ctx, `SELECT COALESCE(to_char(last_analyze, 'HH24:MI:SS'), 'never'), COALESCE(to_char(last_autovacuum, 'HH24:MI:SS'), 'never'), n_mod_since_analyze
		FROM pg_stat_user_tables WHERE relname = 'messages'`).Scan(&analyzed, &autovacuumed, &changed))
	fmt.Printf("--- A75's searches after the import, at %s UTC (last ANALYZE %s, last autovacuum %s, changed since analysed %d)\n",
		time.Now().UTC().Format("15:04:05"), analyzed, autovacuumed, changed)
	for _, s := range a75Searches {
		var each []string
		var slowest time.Duration
		listed := 0
		for range 3 {
			req, _ := http.NewRequest(http.MethodGet, api+"/api/v1/messages?"+s.query, nil)
			req.AddCookie(&http.Cookie{Name: "pa_session", Value: token})
			t0 := time.Now()
			resp, err := http.DefaultClient.Do(req)
			die(err)
			var page struct {
				Messages []json.RawMessage `json:"messages"`
			}
			err = json.NewDecoder(resp.Body).Decode(&page)
			resp.Body.Close()
			d := time.Since(t0)
			die(err)
			if resp.StatusCode != http.StatusOK {
				fail("%s: status %d", s.name, resp.StatusCode)
			}
			listed = len(page.Messages)
			slowest = max(slowest, d)
			each = append(each, d.Round(time.Millisecond).String())
		}
		fmt.Printf("  %-46s %3d listed, %s\n", s.name, listed, strings.Join(each, " "))
		if slowest > time.Second {
			fail("%s: %s after the import, over 1 second (R33b)", s.name, slowest.Round(time.Millisecond))
		}
	}
}

// The webhook's receiver: when each live page's mark arrived.
type marks struct {
	mu   sync.Mutex
	seen map[string]time.Time
	n    int
}

var markRe = regexp.MustCompile(`A76LIVE\d+`)

func (m *marks) note(data []byte) {
	mark := markRe.Find(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]time.Time{}
	}
	m.n++
	if mark != nil {
		if _, ok := m.seen[string(mark)]; !ok {
			m.seen[string(mark)] = time.Now()
		}
	}
}

func (m *marks) at(mark string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.seen[mark]
	return t, ok
}

func startHook(addr string) *marks {
	m := &marks{}
	l, err := net.Listen("tcp", addr)
	die(err)
	go func() {
		_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			m.note(b)
		}))
	}()
	return m
}

func startStream(ctx context.Context, api, token string) *marks {
	m := &marks{}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api+"/api/v1/stream", nil)
	req.AddCookie(&http.Cookie{Name: "pa_session", Value: token})
	resp, err := http.DefaultClient.Do(req)
	die(err)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		message := false
		for sc.Scan() {
			switch line := sc.Text(); {
			case line == "event: message":
				message = true
			case line == "":
				message = false
			case message && strings.HasPrefix(line, "data:"):
				m.note(sc.Bytes())
			}
		}
	}()
	return m
}

type livePage struct {
	mark     string
	posted   time.Time
	took     time.Duration
	status   int
	received time.Time
}

type liveLog struct {
	hook, sse *marks
	mu        sync.Mutex
	pages     []livePage
	before    int // how many came before the import
}

func (l *liveLog) loop(api, key string, every time.Duration, stop <-chan struct{}) {
	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		case <-time.After(every):
		}
		now := time.Now().UTC()
		mark := "A76LIVE" + strconv.Itoa(i)
		body, _ := json.Marshal(map[string]any{"capcode": "0100001", "text": fmt.Sprintf("@@EX0000076 HSF1 %s SMOKE @ 12 FICTION ST, MADEUPTON #STN1", mark), "received_at": now, "baud": 1200})
		req, _ := http.NewRequest(http.MethodPost, api+"/api/v1/pages", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		t0 := time.Now()
		resp, err := http.DefaultClient.Do(req)
		p := livePage{mark: mark, posted: t0, took: time.Since(t0)}
		if err == nil {
			p.status = resp.StatusCode
			resp.Body.Close()
		}
		l.mu.Lock()
		l.pages = append(l.pages, p)
		l.mu.Unlock()
	}
}

func (l *liveLog) posted() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pages)
}

// importStarts notes where the pages before the import end, to compare with.
func (l *liveLog) importStarts() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.before = len(l.pages)
}

func (l *liveLog) print() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.report("before the import", l.pages[:l.before])
	l.report("during the import", l.pages[l.before:])
	fmt.Printf("  the integration's webhook got %d requests, the open list %d messages\n", l.hook.n, l.sse.n)
	if l.hook.n != len(l.pages) {
		fail("the webhook got %d requests for %d live pages: the import set it off (R83b)", l.hook.n, len(l.pages))
	}
	if l.sse.n != len(l.pages) {
		fail("the open list got %d messages for %d live pages: the import sent live updates (R83b)", l.sse.n, len(l.pages))
	}
}

func (l *liveLog) report(when string, pages []livePage) {
	fmt.Println("--- live pages " + when)
	var post, stream, hook []time.Duration
	lost := 0
	for _, p := range pages {
		post = append(post, p.took)
		if p.status != http.StatusOK {
			lost++
			continue
		}
		s, ok := l.sse.at(p.mark)
		if !ok {
			lost++
			continue
		}
		stream = append(stream, s.Sub(p.posted))
		h, ok := l.hook.at(p.mark)
		if !ok {
			lost++
			continue
		}
		hook = append(hook, h.Sub(p.posted))
	}
	show := func(name string, ds []time.Duration, limit time.Duration, spec string) {
		if len(ds) == 0 {
			fail("%s: none", name)
			return
		}
		slices.Sort(ds)
		fmt.Printf("  %-34s median %6s, 99th percentile %6s, slowest %s\n", name, ds[len(ds)/2].Round(time.Millisecond), ds[len(ds)*99/100].Round(time.Millisecond), ds[len(ds)-1].Round(time.Millisecond))
		if ds[len(ds)-1] > limit {
			fail("%s: the slowest took %s, over %s (%s)", name, ds[len(ds)-1].Round(time.Millisecond), limit, spec)
		}
	}
	fmt.Printf("  %d live pages posted, %d lost or refused\n", len(pages), lost)
	show("post answered", post, 2*time.Second, "R32")
	show("live update in an open list", stream, 2*time.Second, "R32")
	show("delivered to the integration", hook, 5*time.Second, "R51a")
	if lost > 0 {
		fail("%d live pages lost or refused", lost)
	}
}
