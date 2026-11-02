package acceptance

import (
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/search"
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
  AND a.ignored IS NOT TRUE
ORDER BY m.sent_at DESC, m.id DESC
LIMIT $4`

// The made-up pages for A75: 10 years at 1,250 a day, newest first, from
// Stations 1 to 4 in turn. Every page has extra fields; "TANKER9" is in one
// page in 10,000, only in an extra field (Station 4's).
const a75Seed = `
INSERT INTO messages (capcode, text, priority, sent_at, dedup_key, paging_group, alias_id, extra)
SELECT a.pattern_key,
       (ARRAY['@@','Hb','QD'])[1 + g % 3]
         || (ARRAY['SMOKE FROM ROOF','CHECK HOSES TODAY','GRASS FIRE','STRUCTURE FIRE, SMOKE SHOWING','ROUTINE PUMP CHECK','MEET AT THE STATION'])[1 + (g * 7) % 6]
         || ' ' || g || ' @ ' || (g % 900) || ' FICTION ST, MADEUPTON #STN' || (1 + g % 4),
       (ARRAY['Emergency','Non-emergency','Admin'])[1 + g % 3],
       now() - g * interval '69.12 seconds',
       sha256(g::text::bytea), 'STN' || (1 + g % 4), a.id,
       jsonb_build_object('unit', 'PUMPER ' || (g % 20),
                          'crew', CASE WHEN g % 10000 = 7 THEN 'TANKER9' ELSE 'CREW ' || (g % 7) END)
FROM generate_series(1, $1::int) g
JOIN aliases a ON a.pattern_key = '010000' || (1 + g % 4)`

// A75. Search speed: 4,562,500 made-up pages, each of A75's searches lists
// its first page within 1 second, and the same pages as the query before
// M13a. Run by CI's search-speed job (PAGERARCHIVE_A75=1); PAGERARCHIVE_A75_PAGES
// sets a smaller number for a quick local try.
func TestA75SearchSpeed(t *testing.T) {
	if os.Getenv("PAGERARCHIVE_A75") != "1" {
		t.Skip("set PAGERARCHIVE_A75=1: seeds 4,562,500 pages (CI's search-speed job)")
	}
	pages := 4_562_500
	if v := os.Getenv("PAGERARCHIVE_A75_PAGES"); v != "" {
		if pages, _ = strconv.Atoi(v); pages < 1 {
			t.Fatalf("PAGERARCHIVE_A75_PAGES=%q", v)
		}
	}
	e := setup(t)
	// The search index as the migrations made it, built once after seeding:
	// much faster than adding 4,562,500 pages to it one by one.
	var def *string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT pg_get_indexdef(to_regclass('messages_search_trgm_idx'))`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if def != nil {
		if _, err := e.st.Pool.Exec(e.ctx, `DROP INDEX messages_search_trgm_idx`); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if _, err := e.st.Pool.Exec(e.ctx, a75Seed, pages); err != nil {
		t.Fatal(err)
	}
	seeded := time.Since(start)
	if def != nil {
		if _, err := e.st.Pool.Exec(e.ctx, *def); err != nil {
			t.Fatal(err)
		}
	}
	built := time.Since(start) - seeded
	if _, err := e.st.Pool.Exec(e.ctx, `VACUUM ANALYZE messages`); err != nil {
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

	searches := []struct {
		name  string
		alias string
		text  string
		a75   bool
	}{
		{"one term that matches nothing", "", "ZZNOTHING", true},
		{"three terms that together match nothing", "", "hoses grass pump", true},
		{`"Smoke showing" or:"grass fire"`, "", `"Smoke showing" or:"grass fire"`, true},
		{"a term found only in an extra field", "", "tanker9", true},
		{`a term with alias:"Station 1"`, "Station 1", "12345", true},
		// Not A75's, for the record: common searches stay fast.
		{"one term in a third of the pages", "", "SMOKE", false},
		{`a common term with alias:"Station 1"`, "Station 1", "smoke", false},
	}
	for _, s := range searches {
		query := "q=" + url.QueryEscape(s.text)
		var alias []string
		if s.alias != "" {
			query += "&alias=" + url.QueryEscape(s.alias)
			alias = []string{s.alias}
		}
		var got []int64
		var times []time.Duration
		// Three times, unless one is already over the limit.
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
		p, err := search.Filter{Text: s.text, Alias: alias}.Params(e.srv.Ingest.Network, nil, 50)
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		rows, err := e.st.Pool.Query(e.ctx, a75Reference, p.QAll, p.QAny, p.Aliases, 50)
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
		t.Logf("%-42s %3d listed, %s (slowest %s; the query before M13a %s)", s.name, len(got),
			strings.Join(each, " "), slowest.Round(time.Millisecond), before.Round(time.Millisecond))
		if !slices.Equal(got, want) {
			t.Errorf("%s: lists %v, the query before M13a %v", s.name, got, want)
		}
		if s.a75 && slowest > time.Second {
			t.Errorf("%s: %s, over 1 second (R33b)", s.name, slowest.Round(time.Millisecond))
		}
	}
}
