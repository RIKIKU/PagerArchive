package acceptance

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql for goose
	"github.com/pressly/goose/v3"

	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/reports"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

// noLocalityAgency is a made-up agency profile with no locality field: its
// locality is at the end of the address, or of the description when a page
// has no address (R24, ADR 0033).
const noLocalityAgency = `{
  "kind": "agency",
  "name": "example-no-locality",
  "description": "Example Ambulance Service",
  "formats": [
    {
      "name": "emergency",
      "priorities": ["Emergency"],
      "match": "^(?P<job_number>EX[0-9]{7}) (?P<event_type>[A-Z]+[0-9]) (?P<description>[^@#]+?) @ (?P<address>[^#]+?) #(?P<group>\\S+)$"
    },
    {
      "name": "no-address",
      "priorities": ["Emergency"],
      "match": "^(?P<job_number>EX[0-9]{7}) (?P<event_type>[A-Z]+[0-9]) (?P<description>[^@#]+?) #(?P<group>\\S+)$"
    }
  ]
}`

// localityReport is the localities report as one line: each row's name,
// jobs, share and whether it's on the map.
func localityReport(r server.LocalitiesReport) string {
	var got []string
	for _, row := range r.Rows {
		name, place := "none", "not on the map"
		if row.Locality != nil {
			name = *row.Locality
		}
		if row.Lon != nil && row.Lat != nil {
			place = "on the map"
		}
		got = append(got, fmt.Sprintf("%s %d %.0f%% %s", name, row.Jobs, row.Share*100, place))
	}
	return strings.Join(got, "; ")
}

// A102. Derived localities (R24, R38c; ADR 0033).
func TestA102DerivedLocalities(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "example-no-locality.json"), []byte(noLocalityAgency), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := profile.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Ingest.Agencies["example-no-locality"] = set.Agencies["example-no-locality"] // the worker's map too
	name := "example-no-locality"
	tpl, err := e.st.CreateTemplate(e.ctx, db.CreateTemplateParams{Name: "Example Ambulance", Agency: "Example Ambulance Service", AgencyProfile: &name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateAlias(e.ctx, db.CreateAliasParams{Pattern: "0500001", Name: "Ambulance 1", TemplateID: &tpl}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for i, text := range []string{
		"@@EX0000951 HSF1 SMOKE FROM ROOF @ 12 FICTION ST MADEUPTON #AMB1", // a house number in a locality
		"@@EX0000952 GRS3 GRASS FIRE @ SAMPLE RD MADEUPTON #AMB1",          // a street only
		"@@EX0000953 GRS3 GRASS FIRE NEAR TESTVILLE #AMB1",                 // no address: the description's end
		"@@EX0000954 MED1 FALL @ 7 NOWHERE LANE FARAWAY #AMB1",             // a locality the dataset doesn't have
	} {
		pr := e.mustPost("a", "0500001", text, reportDay(6+i, 10, 0))
		ids[text[2:11]] = pr.MessageID
	}
	var r server.LocalitiesReport
	e.report("localities", reportWeek+"&agency="+neturl.QueryEscape("Example Ambulance Service"), &r)
	want := "MADEUPTON 2 50% on the map; TESTVILLE 1 25% on the map; none 1 25% not on the map"
	if got := localityReport(r); got != want || r.Jobs != 4 {
		t.Errorf("localities: %s (%d jobs)\nwant %s (4 jobs)", got, r.Jobs, want)
	}
	// Long-running jobs show it too (R38g): only listed jobs have a span, so
	// this checks the stored job.
	var loc string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT locality FROM report_jobs WHERE job_key = 'jEX0000953'`).Scan(&loc); err != nil || loc != "TESTVILLE" {
		t.Errorf("EX0000953's counted locality: %q %v, want TESTVILLE", loc, err)
	}

	// Derived, not parsed (P3): the page's own locality field stays null in
	// the message's view, and the page is still placed on the map (R24).
	var detail server.Message
	e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", ids["EX0000951"]), nil, &detail)
	if detail.Locality != nil || deref(detail.Address) != "12 FICTION ST MADEUPTON" || detail.Location == nil || detail.Location.Source != geocode.FromAddress {
		t.Errorf("EX0000951's view: locality %v, address %q, location %+v; want no locality, its address, placed by its address", detail.Locality, deref(detail.Address), detail.Location)
	}

	// A profile's own field wins: Example Fire's pages count their locality
	// field, and keep no derived one.
	e.mustPost("a", "0100001", "@@EX0000955 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1", reportDay(10, 10, 0))
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM message_localities l JOIN messages m ON m.id = l.message_id WHERE m.locality IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d pages with a locality field have a derived one too (%v), want 0", n, err)
	}

	// A re-parse keeps them (R23a), and the report is the same.
	if err := e.st.RequestReparse(e.ctx, db.RequestReparseParams{CapcodePattern: "%", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.worker.RunOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.report("localities", reportWeek+"&agency="+neturl.QueryEscape("Example Ambulance Service"), &r)
	if got := localityReport(r); got != want {
		t.Errorf("after a re-parse: %s\nwant %s", got, want)
	}
	// Retention takes them with their pages.
	if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM messages WHERE id = $1`, ids["EX0000953"]); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM message_localities WHERE message_id = $1`, ids["EX0000953"]).Scan(&n); err != nil || n != 0 {
		t.Errorf("a deleted page's derived locality: %d rows (%v), want 0", n, err)
	}
	e.report("localities", reportWeek+"&agency="+neturl.QueryEscape("Example Ambulance Service"), &r)
	if want := "MADEUPTON 2 67% on the map; none 1 33% not on the map"; localityReport(r) != want {
		t.Errorf("after retention: %s\nwant %s", localityReport(r), want)
	}
}

// A103. An upgrade from v1.11.1 (migration 00024) with pages stored and the
// report counts built: the API works out the stored pages' localities in
// the background, once, and the counts pick them up (R24, R38c; ADR 0033).
func TestA103LocalitiesBackfilled(t *testing.T) {
	url := os.Getenv("PAGERARCHIVE_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("PAGERARCHIVE_REQUIRE_DB") == "1" {
			t.Fatal("PAGERARCHIVE_TEST_DATABASE_URL is not set")
		}
		t.Skip("PAGERARCHIVE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public")
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("../store/migrations"), goose.WithLogger(goose.NopLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 24); err != nil {
		t.Fatal(err)
	}
	// v1.11.1's pages, made up: parsed by a profile with no locality field,
	// so their locality is null, but one with the field.
	for i, p := range []struct{ job, address, description, locality string }{
		{"EX0000961", "12 FICTION ST MADEUPTON", "SMOKE FROM ROOF", ""},
		{"EX0000962", "SAMPLE RD MADEUPTON", "GRASS FIRE", ""},
		{"EX0000963", "", "GRASS FIRE NEAR TESTVILLE", ""},
		{"EX0000964", "7 NOWHERE LANE FARAWAY", "FALL", ""},
		{"EX0000965", "12 FICTION ST", "SMOKE", "SAMPLEBURY"},
	} {
		text := "@@" + p.job + " made-up page " + fmt.Sprint(i)
		exec(`INSERT INTO messages (capcode, text, sent_at, dedup_key, priority, job_number, address, description, locality)
		      VALUES ('0500001', $1::text, $2, sha256(convert_to($1::text, 'UTF8')), 'Emergency', $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''))`,
			text, time.Date(2026, time.October, 6+i, 10, 0, 0, 0, time.UTC), p.job, p.address, p.description, p.locality)
	}

	// The upgrade: what the API runs on start.
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set, err := profile.LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	n := set.Networks["example-network"]
	ds, err := streets.LoadFile(n.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	ix := streets.NewIndex(ds.Streets, n.StreetTypes)
	hash, err := geocode.DatasetHash(n.DatasetPath, n.StreetTypes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := geocode.Load(ctx, st, n.DatasetPath, ix, hash); err != nil {
		t.Fatal(err)
	}
	rep := &reports.Service{Pool: st.Pool, Location: time.UTC}
	// The counts as v1.11.1 built them, its queue counted: only the locality
	// field.
	if err := rep.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rep.CatchUp(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	week := reports.Filter{From: time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC), To: time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC)}
	counts := func() string {
		t.Helper()
		c, err := rep.Localities(ctx, week)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range c {
			out = append(out, fmt.Sprintf("%q %d", r.Key, r.Jobs))
		}
		return strings.Join(out, ", ")
	}
	if got, want := counts(), `"" 4, "SAMPLEBURY" 1`; got != want {
		t.Errorf("before the backfill: %s, want %s", got, want)
	}

	g := &geocode.Geocoder{Index: ix, GridSystem: n.GridSystem}
	var logs syncBuffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	if err := g.BackfillLocalities(ctx, st, log, 0, rep.Rebuild); err != nil {
		t.Fatal(err)
	}
	if v, err := st.GetSetting(ctx, "message_localities_backfill"); err != nil || v != "done" {
		t.Errorf("backfill state %q %v, want done", v, err)
	}
	rows := func() string {
		t.Helper()
		r, err := conn.Query(ctx, `SELECT m.job_number || ' ' || l.locality FROM message_localities l JOIN messages m ON m.id = l.message_id ORDER BY m.id`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(r, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, ", ")
	}
	wantRows := "EX0000961 MADEUPTON, EX0000962 MADEUPTON, EX0000963 TESTVILLE"
	if got := rows(); got != wantRows {
		t.Errorf("derived localities: %s, want %s", got, wantRows)
	}
	// The counts follow: built again once, with nothing queued job by job
	// (ADR 0033 §5).
	var queued int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM report_dirty`).Scan(&queued); err != nil || queued != 0 {
		t.Errorf("%d jobs queued by the backfill (%v), want 0", queued, err)
	}
	if got, want := counts(), `"MADEUPTON" 2, "" 1, "SAMPLEBURY" 1, "TESTVILLE" 1`; got != want {
		t.Errorf("after the backfill: %s, want %s", got, want)
	}
	// Once: a second start does nothing; one stopped after its last batch
	// only builds the counts again; and run again from the start (its state
	// lost), it changes nothing.
	recounts := 0
	recount := func(ctx context.Context) error { recounts++; return rep.Rebuild(ctx) }
	if err := g.BackfillLocalities(ctx, st, log, 0, recount); err != nil || recounts != 0 {
		t.Fatalf("done: %d recounts, %v; want none", recounts, err)
	}
	exec(`UPDATE settings SET value = 'recount' WHERE key = 'message_localities_backfill'`)
	if err := g.BackfillLocalities(ctx, st, log, 0, recount); err != nil || recounts != 1 {
		t.Fatalf("stopped before its recount: %d recounts, %v; want 1", recounts, err)
	}
	exec(`DELETE FROM settings WHERE key = 'message_localities_backfill'`)
	if err := g.BackfillLocalities(ctx, st, log, 0, recount); err != nil {
		t.Fatal(err)
	}
	if got := rows(); got != wantRows {
		t.Errorf("run again: %s, want %s", got, wantRows)
	}
	if got, want := counts(), `"MADEUPTON" 2, "" 1, "SAMPLEBURY" 1, "TESTVILLE" 1`; got != want {
		t.Errorf("run again: %s, want %s", got, want)
	}
	// Progress is logged with counts only (P1).
	out := logs.String()
	if !strings.Contains(out, "stored pages' localities worked out") || !strings.Contains(out, "report counts built again") {
		t.Errorf("no progress in the log:\n%s", out)
	}
	for _, s := range []string{"FICTION", "MADEUPTON", "TESTVILLE", "GRASS"} {
		if strings.Contains(out, s) {
			t.Errorf("the log holds %q", s)
		}
	}
}
