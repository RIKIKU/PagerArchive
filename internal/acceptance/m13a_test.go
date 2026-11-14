package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/search"
	"github.com/RIKIKU/PagerArchive/internal/seeddata"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// R33b: text search reads the text and extra fields joined by a separator
// (ADR 0017), but a term still matches within one field, as it did when each
// field was searched on its own: never across the boundary between the text
// and an extra field, or between two extra fields.
func TestSearchFieldBoundary(t *testing.T) {
	e := setup(t)
	id := e.mustPost("a", "0100001", "QDCHECK THE PANEL", e.now).MessageID
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE messages SET extra = '{"unit": "KILO", "crew": "WATT"}' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	// A page whose own text holds the separator, sent through the API.
	e.mustPost("a", "0100002", "QDALPHA\x1fBETA 50% OFF", e.now.Add(-time.Minute))
	shown := func(text string) string {
		var got []string
		for _, m := range e.list("q=" + url.QueryEscape(text)) {
			got = append(got, strings.TrimPrefix(m.Text, "QD"))
		}
		slices.Sort(got)
		return strings.Join(got, " | ")
	}
	panel, alpha := "CHECK THE PANEL", "ALPHA\x1fBETA 50% OFF"
	tests := []struct{ text, want string }{
		{"kilo", panel},               // an extra field
		{"watt", panel},               // the other one
		{"kilowatt", ""},              // across two extra fields
		{"wattkilo", ""},              // either way round
		{"tk", ""},                    // across them, too short for the index
		{"or:tk or:zz", ""},           // the same, as or: terms
		{`"kilo watt"`, ""},           // with a space between
		{"panelkilo", ""},             // across the text and an extra field
		{"panelwatt", ""},             // either one
		{"kilo\x1fwatt", ""},          // holding the separator, across two fields
		{"panel\x1fkilo", ""},         // holding it, across the text and a field
		{"alpha\x1fbeta", alpha},      // holding it, within one field
		{"kilo watt", panel},          // two terms, each in a field
		{"or:kilowatt or:50%", alpha}, // or: terms, and a typed % matches itself
		{"5%0", ""},                   // as % and not as a wildcard
		{"Panel", panel},              // case is ignored
	}
	for _, tt := range tests {
		if got := shown(tt.text); got != tt.want {
			t.Errorf("%q: %q, want %q", tt.text, got, tt.want)
		}
	}
}

// R33b (ADR 0017): terms too short for a trigram, which the bigram index
// finds, list the pages they did before it, whichever try answers: anywhere
// in the text or one extra field, case ignored (non-ASCII letters too), a
// typed % or _ as itself, never across two fields.
func TestSearchShortTerms(t *testing.T) {
	e := setup(t)
	for i, text := range []string{
		"@@EX0000101 HSF1 SMOKE FROM ROOF 1 M 23 B4 @ 12 FICTION ST, MADEUPTON #STN1",
		"QDB4 THEN 23",
		"QDMAP 2 3 B 4",
		"QDCHECK PANEL",
		"QDPANEL ÉB ÄÖ",
		"QD50% OFF",
		"QD4_5 AND 405",
	} {
		e.mustPost("a", "0100001", text, e.now.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE messages SET extra = '{"grid": "M 23 B4", "unit": "K"}' WHERE text = 'QDCHECK PANEL'`); err != nil {
		t.Fatal(err)
	}
	shown := func(text string) string {
		var got []string
		for _, m := range e.list("q=" + url.QueryEscape(text)) {
			got = append(got, strings.TrimPrefix(m.Text, "QD"))
		}
		slices.Sort(got)
		return strings.Join(got, " | ")
	}
	ref := "@@EX0000101 HSF1 SMOKE FROM ROOF 1 M 23 B4 @ 12 FICTION ST, MADEUPTON #STN1"
	tests := []struct{ text, want string }{
		{"m 23 b4", ref + " | CHECK PANEL"},            // in the text, or all in one extra field
		{"b4 23", ref + " | B4 THEN 23 | CHECK PANEL"}, // in any order
		{"m 23 b4 smoke", ref},                         // with a longer term, as before
		{"panel éb", "PANEL ÉB ÄÖ"},                    // with a longer term, case ignored
		{"check 4k", ""},                               // with a longer term, across two extra fields
		{"405 4_", "4_5 AND 405"},                      // a typed _ with a longer term
		{"23 b4 zz", ""},                               // one of them nowhere
		{"b k", ref + " | CHECK PANEL"},                // single letters: no bigram, as before
		{"4k", ""},                                     // across two extra fields
		{"lk", ""},                                     // across the text and an extra field
		{"éb äö", "PANEL ÉB ÄÖ"},                       // case ignored
		{"0%", "50% OFF"},                              // a typed % as itself
		{"4_", "4_5 AND 405"},                          // and _
		{"0_", ""},                                     // not as a wildcard
	}
	for _, tt := range tests {
		for _, mode := range []string{"first", "1 ms first try", "bounded", "second"} {
			e.st.FirstTry, e.st.BoundedTryOnly, e.st.SecondTryOnly = 0, mode == "bounded", mode == "second"
			if mode == "1 ms first try" {
				e.st.FirstTry = time.Millisecond
			}
			if got := shown(tt.text); got != tt.want {
				t.Errorf("%q, %s: %q, want %q", tt.text, mode, got, tt.want)
			}
		}
	}
	e.st.FirstTry, e.st.BoundedTryOnly, e.st.SecondTryOnly = 0, false, false
}

// R33b (ADR 0017): the bigram index is given a search's rarest bigrams, by the
// statistics on it, and all of them before there are any.
func TestSearchRarestBigrams(t *testing.T) {
	e := setup(t)
	if _, err := e.st.Pool.Exec(e.ctx, `ANALYZE messages`); err != nil {
		t.Fatal(err)
	}
	if got, err := e.st.RarestBigrams(e.ctx, []string{"tanker9", "st"}); err != nil || len(got) != 7 {
		t.Errorf("with no pages, %q (%v), want all 7", got, err)
	}
	for i := range 200 {
		text := "QDCHECK HOSES @ 12 FICTION ST"
		if i == 7 {
			text = "QDTANKER9 @ 12 FICTION ST"
		}
		e.mustPost("a", "0100001", text, e.now.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := e.st.Pool.Exec(e.ctx, `ANALYZE messages`); err != nil {
		t.Fatal(err)
	}
	got, err := e.st.RarestBigrams(e.ctx, []string{"tanker9", "st"})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	// ta, an, nk, ke, er and r9 are in one page; st is in all of them.
	if len(got) != 3 || slices.Contains(got, "st") {
		t.Errorf("%q, want 3 without st", got)
	}
	if rows := e.list("q=" + url.QueryEscape("tanker9 st")); len(rows) != 1 {
		t.Errorf("tanker9 st: %d pages, want 1", len(rows))
	}
}

// R33b and P1: a value the nightly run redacts is no longer found by search,
// in the text or an extra field: the search index is derived from them, so
// it follows the redaction in the same run.
func TestNightlyRedactionReachesSearch(t *testing.T) {
	e := setup(t)
	id := e.mustPost("a", "0100001", "@@EX0000051 HSF1 SMOKE @ 12 FICTION ST, MADEUPTON #STN1", e.now).MessageID
	// As if stored before a rule matching this number existed.
	phone := "0412 345 678"
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE messages SET text = $2, description = $3, extra = jsonb_build_object('caller', $4::text) WHERE id = $1`, id,
		"@@EX0000051 HSF1 CALL "+phone+" @ 12 FICTION ST, MADEUPTON #STN1", "CALL "+phone, phone); err != nil {
		t.Fatal(err)
	}
	found := func(text string) int { return len(e.list("q=" + url.QueryEscape(`"`+text+`"`))) }
	if n := found(phone); n != 1 {
		t.Fatalf("before the nightly run: %d found, want 1", n)
	}
	if _, err := e.job.RunOnce(e.ctx, e.now); err != nil {
		t.Fatal(err)
	}
	if n := found(phone); n != 0 {
		t.Errorf("after the nightly run: %d found, want 0", n)
	}
	if n := found("345 678"); n != 0 {
		t.Errorf("part of it after the nightly run: %d found, want 0", n)
	}
	if n := found("CALL REDACTED"); n != 1 {
		t.Errorf("the redacted text: %d found, want 1", n)
	}
}

// The text condition before M13a (ADR 0015), the reference for A75: each
// term in the text or any one extra field, with the filters A75 uses.
const a75Reference = `
SELECT m.id FROM messages m
LEFT JOIN aliases a ON a.id = m.alias_id
WHERE ((cardinality($1::text[]) = 0 AND cardinality($2::text[]) = 0)
       OR (cardinality($1::text[]) > 0
           AND NOT EXISTS (SELECT 1 FROM unnest($1::text[]) AS term (p)
                           WHERE NOT (m.text ILIKE term.p
                                      OR EXISTS (SELECT 1 FROM jsonb_each_text(m.extra) e WHERE e.value ILIKE term.p))))
       OR EXISTS (SELECT 1 FROM unnest($2::text[]) AS term (p)
                  WHERE m.text ILIKE term.p
                     OR EXISTS (SELECT 1 FROM jsonb_each_text(m.extra) e WHERE e.value ILIKE term.p)))
  AND (cardinality($3::text[]) = 0 OR a.name ILIKE ANY($3::text[]))
  AND (cardinality($5::text[]) = 0 OR a.name ILIKE ANY($5::text[]))
  AND a.ignored IS NOT TRUE
ORDER BY m.sent_at DESC, m.id DESC
LIMIT $4`

// a75ColdStart stops the test database's container, drops the operating
// system's page cache and starts the container again, as a reboot leaves them:
// PostgreSQL's cache and the system's both cold. It returns when the database
// accepts connections, having made none, with the API's pool empty, as it is
// when the API starts after a reboot.
func a75ColdStart(t *testing.T, e *env, container string) {
	t.Helper()
	for _, cmd := range [][]string{
		{"docker", "stop", container},
		{"sudo", "-n", "sh", "-c", "sync; echo 3 > /proc/sys/vm/drop_caches"},
		{"docker", "start", container},
	} {
		if out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", cmd, err, out)
		}
	}
	for end := time.Now().Add(2 * time.Minute); exec.Command("docker", "exec", container, "pg_isready", "-q", "-h", "127.0.0.1").Run() != nil; {
		if time.Now().After(end) {
			t.Fatal("the database didn't start within 2 minutes")
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.st.Pool.Reset()
}

// a75ColdRun runs the search once straight after a75ColdStart, and says how
// long it took and what it read from the disks. A cold run reads the same
// blocks each time, so one that took longer read them more slowly (ADR 0017).
func a75ColdRun(t *testing.T, e *env, container, query string) ([]int64, time.Duration, string) {
	t.Helper()
	a75ColdStart(t, e, container)
	r0, ms0 := a75DiskReads()
	t0 := time.Now()
	var ids []int64
	for _, m := range e.list(query) {
		ids = append(ids, m.ID)
	}
	d := time.Since(t0)
	r1, ms1 := a75DiskReads()
	if r0 < 0 || r1 <= r0 {
		return ids, d, ""
	}
	return ids, d, fmt.Sprintf(" (%d disk reads, %.2f ms each)", r1-r0, float64(ms1-ms0)/float64(r1-r0))
}

// a75DiskReads is the reads completed and the milliseconds spent reading on
// the whole disks, from /proc/diskstats; -1 without it.
func a75DiskReads() (reads, ms int64) {
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return -1, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 7 || !a75WholeDisk.MatchString(f[2]) {
			continue
		}
		r, _ := strconv.ParseInt(f[3], 10, 64)
		m, _ := strconv.ParseInt(f[6], 10, 64)
		reads, ms = reads+r, ms+m
	}
	return reads, ms
}

var a75WholeDisk = regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|nvme[0-9]+n[0-9]+)$`)

// A75. Search speed: 4,562,500 made-up pages, each of A75's searches lists
// its first page within 1 second, and the same pages as the query before
// M13a. That counts its first run straight after a restart with both caches
// cold, then three runs warm. Run by CI's search-speed job (PAGERARCHIVE_A75=1,
// PAGERARCHIVE_A75_CONTAINER naming the test database's container, and sudo);
// PAGERARCHIVE_A75_PAGES sets a smaller number for a quick local try.
func TestA75SearchSpeed(t *testing.T) {
	if os.Getenv("PAGERARCHIVE_A75") != "1" {
		t.Skip("set PAGERARCHIVE_A75=1: seeds 4,562,500 pages (CI's search-speed job)")
	}
	container := os.Getenv("PAGERARCHIVE_A75_CONTAINER")
	if container == "" {
		t.Fatal("set PAGERARCHIVE_A75_CONTAINER: A75's cold starts restart the test database's container")
	}
	pages := seeddata.A75Pages
	if v := os.Getenv("PAGERARCHIVE_A75_PAGES"); v != "" {
		if pages, _ = strconv.Atoi(v); pages < 1 {
			t.Fatalf("PAGERARCHIVE_A75_PAGES=%q", v)
		}
	}
	e := setup(t)
	start := time.Now()
	seeded, built, err := seeddata.Seed(e.ctx, e.st.Pool, pages)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d pages in %s, built the search indexes and the reports' counts in %s, all in %s", n,
		seeded.Round(time.Second), built.Round(time.Second), time.Since(start).Round(time.Second))
	var table, index, bigrams, alias, database string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT pg_size_pretty(pg_total_relation_size('messages')),
		COALESCE(pg_size_pretty(pg_relation_size(to_regclass('messages_search_trgm_idx'))), 'missing'),
		COALESCE(pg_size_pretty(pg_relation_size(to_regclass('messages_search_bigrams_idx'))), 'missing'),
		COALESCE(pg_size_pretty(pg_relation_size(to_regclass('messages_alias_sent_idx'))), 'missing'), pg_size_pretty(pg_database_size(current_database()))`).Scan(&table, &index, &bigrams, &alias, &database); err != nil {
		t.Fatal(err)
	}
	t.Logf("messages with its indexes %s, of which the search index %s, the short terms' %s, the alias's %s; the database %s", table, index, bigrams, alias, database)

	// alias is a typed alias: value (partial, quoted or not); picked is one
	// picked from the suggestions (alias_like).
	searches := []struct {
		name          string
		alias, picked string
		text          string
		a75           bool
	}{
		{"one term that matches nothing", "", "", "ZZNOTHING", true},
		{"three terms that together match nothing", "", "", "hoses grass pump", true},
		{`"Smoke showing" or:"grass fire"`, "", "", `"Smoke showing" or:"grass fire"`, true},
		{"a term found only in an extra field", "", "", "tanker9", true},
		{`a term with alias:"Station 1"`, "Station 1", "", "12345", true},
		// The sixth (owner, 2026-11-02): a term in a quarter of the pages,
		// none of them Station 1's.
		{`alias:"Station 1", a term never in its pages`, "Station 1", "", "STN2", true},
		{`the same with alias:sta`, "sta", "", "STN2", true},
		{`the same with "Station 1" picked`, "", "Station 1", "STN2", true},
		// Production's (owner, 2026-11-08): a common term with the busy
		// alias, in a third of its 80,000 pages, and a map reference typed
		// without quotes, three terms too short for the trigram index.
		{`a common term with alias:"Station 5", the busy alias`, "Station 5", "", "fire", true},
		{"a map reference typed without quotes", "", "", "m 23 b4", true},
		// Production's shape (owner, 2026-11-08): a letter, a number of three
		// digits in 2.4% of the pages and a square in 0.7%, together in about
		// 1 page in 1,000.
		{"the same, with a 3-digit page", "", "", "m 345 c7", true},
		// Not A75's, for the record: common searches stay fast.
		{"one term in a third of the pages", "", "", "SMOKE", false},
		{`a common term with alias:"Station 1"`, "Station 1", "", "smoke", false},
		{`a common term with alias:"Unit 0001", a small alias`, "Unit 0001", "", "smoke", false},
		{"two short terms in most pages", "", "", "st 12", false},
		{"a common term and a short one in every page", "", "", "smoke st", false},
		{"a rare term and a short one in every page", "", "", "tanker9 st", false},
		{"a term in 2.4% of the pages and a short one", "", "", "345 c7", false},
	}
	type a75Again struct {
		name, query string
		want        []int64
		first       time.Duration
	}
	var again []a75Again
	for _, s := range searches {
		query := "q=" + url.QueryEscape(s.text)
		var alias, picked []string
		if s.alias != "" {
			query += "&alias=" + url.QueryEscape(s.alias)
			alias = []string{s.alias}
		}
		if s.picked != "" {
			query += "&alias_like=" + url.QueryEscape(s.picked)
			picked = []string{s.picked}
		}
		// The first run, straight after a restart with both caches cold.
		cold, coldTime, coldDisk := a75ColdRun(t, e, container, query)
		var got []int64
		var times []time.Duration
		// Three times warm, unless one is already over the limit.
		for len(times) < 3 && (len(times) == 0 || times[len(times)-1] <= time.Second) {
			t0 := time.Now()
			ms := e.list(query)
			times = append(times, time.Since(t0))
			got = got[:0]
			for _, m := range ms {
				got = append(got, m.ID)
			}
		}
		slowest := slices.Max(times)
		p, err := search.Filter{Text: s.text, Alias: alias, AliasLike: picked}.Params(e.srv.Ingest.Network, nil, 50)
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		rows, err := e.st.Pool.Query(e.ctx, a75Reference, p.QAll, p.QAny, p.Aliases, 50, p.AliasLikes)
		if err != nil {
			t.Fatal(err)
		}
		var want []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			want = append(want, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		before := time.Since(t0)
		var each []string
		for _, d := range times {
			each = append(each, d.Round(time.Millisecond).String())
		}
		t.Logf("%-42s %3d listed, cold %s%s, warm %s (slowest %s; the query before M13a %s)", s.name, len(got), coldTime.Round(time.Millisecond), coldDisk,
			strings.Join(each, " "), slowest.Round(time.Millisecond), before.Round(time.Millisecond))
		if !slices.Equal(got, want) {
			t.Errorf("%s: lists %v, the query before M13a %v", s.name, got, want)
		}
		if !slices.Equal(cold, want) {
			t.Errorf("%s: after a cold start lists %v, the query before M13a %v", s.name, cold, want)
		}
		if s.a75 && slowest > time.Second {
			t.Errorf("%s: %s, over 1 second (R33b)", s.name, slowest.Round(time.Millisecond))
		}
		if s.a75 && coldTime > time.Second {
			again = append(again, a75Again{s.name, query, want, coldTime})
		}
	}
	// A cold run over 1 second gets one more cold start, after all the others
	// (ADR 0017, "A75's cold runs on CI's runners"): a runner's disk can read
	// several times slower for a few seconds, and the same search from a cold
	// start minutes later reads the same blocks. The better of the two counts.
	// A search that is slow cold is slow both times.
	for _, a := range again {
		cold, coldTime, coldDisk := a75ColdRun(t, e, container, a.query)
		t.Logf("%-42s cold again %s%s (the first %s)", a.name, coldTime.Round(time.Millisecond), coldDisk, a.first.Round(time.Millisecond))
		if !slices.Equal(cold, a.want) {
			t.Errorf("%s: after another cold start lists %v, the query before M13a %v", a.name, cold, a.want)
		}
		if coldTime > time.Second {
			t.Errorf("%s: the first run after a cold start took %s, and %s after another, over 1 second (R33b)", a.name,
				a.first.Round(time.Millisecond), coldTime.Round(time.Millisecond))
		}
	}
	// A71 on the same pages (M14, ADR 0026): the scrubber's counts.
	a71CountsSpeed(t, e, container)
	// A99 on the same pages (M24, ADR 0031): the reports.
	a99ReportsSpeed(t, e, container)
}

// R33b (ADR 0017): a search's second try, which also finds the alias:
// values' pages by alias_id, lists the same pages as its first, for typed
// alias: values (partial and quoted), picked ones, several, both kinds, and
// a value that names no alias.
func TestSearchSecondTryAliases(t *testing.T) {
	e := setup(t)
	for i, p := range []struct{ capcode, text string }{
		{"0100001", "QDSMOKE SEEN AT STN1"},
		{"0100001", "QDGRASS FIRE NEAR STN1"},
		{"0100002", "QDSMOKE SEEN AT STN2"},
		{"0100002", "QDGRASS FIRE NEAR STN2"},
		{"0100003", "QDSMOKE SEEN AT STN3"},
		{"0100004", "QDCHECK HOSES AT STN4"},
		{"0999999", "QDSMOKE SEEN, NO ALIAS"},
	} {
		e.mustPost("a", p.capcode, p.text, e.now.Add(-time.Duration(i)*time.Minute))
	}
	shown := func(query string) string {
		var got []string
		for _, m := range e.list(query) {
			got = append(got, strings.TrimPrefix(m.Text, "QD"))
		}
		slices.Sort(got)
		return strings.Join(got, " | ")
	}
	q := func(text string) string { return "q=" + url.QueryEscape(text) }
	tests := []struct{ query, want string }{
		{q("STN2") + "&alias=" + url.QueryEscape("Station 1"), ""},
		{q("STN2") + "&alias=sta", "GRASS FIRE NEAR STN2 | SMOKE SEEN AT STN2"},
		{q("STN2") + "&alias_like=" + url.QueryEscape("Station 2"), "GRASS FIRE NEAR STN2 | SMOKE SEEN AT STN2"},
		{q("STN2") + "&alias_like=" + url.QueryEscape("station"), ""}, // picked matches the whole name
		{q("smoke") + "&alias=" + url.QueryEscape("Station 1") + "&alias=" + url.QueryEscape("Station 3"), "SMOKE SEEN AT STN1 | SMOKE SEEN AT STN3"},
		{q("smoke") + "&alias=sta&alias_like=" + url.QueryEscape("Station 2"), "SMOKE SEEN AT STN2"},
		{q("smoke") + "&alias=nowhere", ""},
		{q("or:hoses or:grass") + "&alias=" + url.QueryEscape("Station 4"), "CHECK HOSES AT STN4"},
		{q("smoke"), "SMOKE SEEN AT STN1 | SMOKE SEEN AT STN2 | SMOKE SEEN AT STN3 | SMOKE SEEN, NO ALIAS"},
	}
	for _, tt := range tests {
		e.st.SecondTryOnly = false
		first := shown(tt.query)
		e.st.SecondTryOnly = true
		second := shown(tt.query)
		e.st.SecondTryOnly = false
		if first != tt.want || second != tt.want {
			t.Errorf("%s: first try %q, second try %q, want %q", tt.query, first, second, tt.want)
		}
	}
}

// R33b (ADR 0017): a search lists the same pages in the same order whichever
// try answers it. The bounded try sorts what it finds itself, with the id
// ordering pages at one time, and stops short with 1,000 pages or more. The
// pages are stored oldest first, so the order the index finds them in isn't the
// search's: the first 1,000 found aren't the newest.
func TestSearchBoundedTry(t *testing.T) {
	e := setup(t)
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO messages (capcode, text, sent_at, dedup_key, extra)
		SELECT '0100001', 'QD' || CASE WHEN g <= 60 THEN 'TIEPAGE ' WHEN g <= 120 THEN 'TIMEPAGE ' ELSE 'MANYPAGES ' END || g,
		       $1::timestamptz - CASE WHEN g <= 60 THEN interval '0' ELSE (2000 - g) * interval '1 minute' END,
		       sha256(('bounded' || g)::bytea), '{}'
		FROM generate_series(1, 1220) g`, e.now); err != nil {
		t.Fatal(err)
	}
	listed := func(query string) string {
		var page server.MessagePage
		if code := e.getJSON("/api/v1/messages?"+query, &page); code != http.StatusOK {
			t.Fatalf("GET messages?%s: %d", query, code)
		}
		out := page.Next + ":"
		for _, m := range page.Messages {
			out += strconv.FormatInt(m.ID, 10) + " "
		}
		return out
	}
	q := func(text string) string { return "q=" + url.QueryEscape(text) }
	var next server.MessagePage
	if code := e.getJSON("/api/v1/messages?"+q("tiepage"), &next); code != http.StatusOK || next.Next == "" {
		t.Fatalf("the first page of tiepage: %d, next %q", code, next.Next)
	}
	for _, query := range []string{
		q("tiepage"),                // more than a page, all at one time
		q("tiepage") + "&limit=3",   // a smaller page
		q("tiepage") + "&limit=200", // all of them
		q("tiepage") + "&before=" + url.QueryEscape(next.Next), // the next page
		q("timepage"),              // each at its own time
		q("timepage") + "&limit=3", // the newest of them
		q("manypages"),             // too many for the bounded try
		q("manypages") + "&limit=200",
		q("tiepage 12"),
		q("or:tiepage or:zznothing"),
		q("tiepage manypages"), // never together
	} {
		e.st.BoundedTryOnly = false
		want := listed(query)
		e.st.BoundedTryOnly = true
		got := listed(query)
		e.st.BoundedTryOnly = false
		if got != want {
			t.Errorf("%s: the bounded try lists %q, the first try %q", query, got, want)
		}
		// Through the tries in turn, as a first try that misses goes.
		e.st.FirstTry = time.Millisecond
		turn := listed(query)
		e.st.FirstTry = 0
		if turn != want {
			t.Errorf("%s: with a 1 ms first try it lists %q, the first try %q", query, turn, want)
		}
	}
}
