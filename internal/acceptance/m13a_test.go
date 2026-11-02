package acceptance

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
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
	t.Logf("seeded %d pages in %s, built the search index in %s, all with VACUUM ANALYZE in %s", n,
		seeded.Round(time.Second), built.Round(time.Second), time.Since(start).Round(time.Second))
	var table, index, database string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT pg_size_pretty(pg_total_relation_size('messages')),
		COALESCE(pg_size_pretty(pg_relation_size(to_regclass('messages_search_trgm_idx'))), 'missing'), pg_size_pretty(pg_database_size(current_database()))`).Scan(&table, &index, &database); err != nil {
		t.Fatal(err)
	}
	t.Logf("messages with its indexes %s, of which the search index %s; the database %s", table, index, database)

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
		// Not A75's, for the record: common searches stay fast.
		{"one term in a third of the pages", "", "", "SMOKE", false},
		{`a common term with alias:"Station 1"`, "Station 1", "", "smoke", false},
	}
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
		a75ColdStart(t, e, container)
		t0 := time.Now()
		var cold []int64
		for _, m := range e.list(query) {
			cold = append(cold, m.ID)
		}
		coldTime := time.Since(t0)
		var got []int64
		var times []time.Duration
		// Three times warm, unless one is already over the limit.
		for len(times) < 3 && (len(times) == 0 || times[len(times)-1] <= time.Second) {
			t0 = time.Now()
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
		t0 = time.Now()
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
		t.Logf("%-42s %3d listed, cold %s, warm %s (slowest %s; the query before M13a %s)", s.name, len(got), coldTime.Round(time.Millisecond),
			strings.Join(each, " "), slowest.Round(time.Millisecond), before.Round(time.Millisecond))
		if !slices.Equal(got, want) {
			t.Errorf("%s: lists %v, the query before M13a %v", s.name, got, want)
		}
		if !slices.Equal(cold, want) {
			t.Errorf("%s: after a cold start lists %v, the query before M13a %v", s.name, cold, want)
		}
		if s.a75 && coldTime > time.Second {
			t.Errorf("%s: the first run after a cold start took %s, over 1 second (R33b)", s.name, coldTime.Round(time.Millisecond))
		}
		if s.a75 && slowest > time.Second {
			t.Errorf("%s: %s, over 1 second (R33b)", s.name, slowest.Round(time.Millisecond))
		}
	}
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
