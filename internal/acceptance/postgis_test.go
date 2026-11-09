package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store"
)

// sqlLog records every statement run through a store (a pgx.QueryTracer).
type sqlLog struct {
	mu  sync.Mutex
	sql []string
}

func (l *sqlLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sql = append(l.sql, d.SQL)
	return ctx
}

func (l *sqlLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (l *sqlLog) has(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, q := range l.sql {
		if strings.Contains(strings.ToUpper(q), s) {
			return true
		}
	}
	return false
}

// logLines parses slog's JSON lines.
func logLines(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// A83 (R84c): given a database whose PostGIS extensions are registered at an
// older version than the image provides, as after pulling a newer
// postgis/postgis image, when the API starts (migrations, then
// store.UpdatePostGIS, as serve runs them), then every installed PostGIS
// extension is at the image's version and each update is logged at INFO with
// its versions; a second start runs no ALTER and logs nothing; and when the
// API's role doesn't own the extension, a WARN names it and the fix, and the
// start carries on.
func TestA83PostGISUpdatedOnStart(t *testing.T) {
	url := os.Getenv("PAGERARCHIVE_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("PAGERARCHIVE_REQUIRE_DB") == "1" {
			t.Fatal("PAGERARCHIVE_TEST_DATABASE_URL is not set")
		}
		t.Skip("PAGERARCHIVE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// Named for this test, and gone afterwards: the other tests start from
	// postgis and pg_trgm only (setup drops the public schema).
	const role = "pagerarchive_postgis_test"
	cleanup := func() {
		_, _ = admin.Exec(ctx, `DROP EXTENSION IF EXISTS postgis_tiger_geocoder, postgis_topology CASCADE`)
		_, _ = admin.Exec(ctx, `DROP EXTENSION IF EXISTS fuzzystrmatch`)
		_, _ = admin.Exec(ctx, `ALTER EXTENSION postgis UPDATE`)
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS `+role)
	}
	cleanup()
	t.Cleanup(cleanup)
	exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public")

	// The image's own first start makes these as well as postgis (the tiger
	// geocoder only before PostGIS 3.7, which no longer ships it).
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	exec(`CREATE EXTENSION IF NOT EXISTS postgis_topology`)
	var tiger bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_available_extensions WHERE name = 'postgis_tiger_geocoder')`).Scan(&tiger); err != nil {
		t.Fatal(err)
	}
	if tiger {
		exec(`CREATE EXTENSION IF NOT EXISTS postgis_tiger_geocoder CASCADE`)
	}

	type version struct{ installed, image string }
	versions := func() map[string]version {
		t.Helper()
		rows, err := admin.Query(ctx, `
SELECT e.extname, e.extversion, a.default_version
FROM pg_extension e JOIN pg_available_extensions a ON a.name = e.extname
WHERE e.extname LIKE 'postgis%'`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]version{}
		for rows.Next() {
			var n string
			var v version
			if err := rows.Scan(&n, &v.installed, &v.image); err != nil {
				t.Fatal(err)
			}
			out[n] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Register each at the release before the image's, as a database made by
	// an older image is: the image ships an upgrade script from each earlier
	// release, but installs only its own, so the older version is set in the
	// catalogue (the library is the image's either way).
	older := map[string]string{}
	for name, v := range versions() {
		var from string
		if err := admin.QueryRow(ctx, `
SELECT source FROM pg_extension_update_paths($1)
WHERE target = $2 AND source <> target AND path IS NOT NULL AND source ~ '^\d+\.\d+\.\d+$'
ORDER BY string_to_array(source, '.')::int[] DESC LIMIT 1`, name, v.image).Scan(&from); err != nil {
			t.Fatalf("no older version of %s to upgrade from: %v", name, err)
		}
		older[name] = from
	}
	for _, want := range []string{"postgis", "postgis_topology"} {
		if _, ok := older[want]; !ok {
			t.Fatalf("%s isn't installed: %v", want, older)
		}
	}
	setOlder := func() {
		t.Helper()
		for name, from := range older {
			exec(`UPDATE pg_extension SET extversion = $1 WHERE extname = $2`, from, name)
		}
	}
	setOlder()
	t.Logf("A83: registered at %v; the image provides %v", older, versions())

	start := func(dbURL string) (*bytes.Buffer, *sqlLog) {
		t.Helper()
		var logs bytes.Buffer
		trace := &sqlLog{}
		st, err := store.Open(ctx, dbURL, trace)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		store.UpdatePostGIS(ctx, st.Pool, slog.New(slog.NewJSONHandler(&logs, nil)))
		return &logs, trace
	}

	// The first start after the image changed updates every one.
	logs, _ := start(url)
	for name, v := range versions() {
		if v.installed != v.image {
			t.Errorf("%s is at %s after the start, not the image's %s", name, v.installed, v.image)
		}
	}
	updated := map[string]bool{}
	for _, l := range logLines(t, logs) {
		if l["level"] != "INFO" || l["msg"] != "PostGIS extension updated" {
			t.Errorf("unexpected log line: %v", l)
			continue
		}
		name, _ := l["extension"].(string)
		if l["from"] != older[name] || l["to"] != versions()[name].image {
			t.Errorf("%s logged from %v to %v, want %s to %s", name, l["from"], l["to"], older[name], versions()[name].image)
		}
		updated[name] = true
	}
	for name := range older {
		if !updated[name] {
			t.Errorf("no INFO line for %s:\n%s", name, logs)
		}
	}
	t.Logf("A83: first start logged:\n%s", logs)

	// A second start finds them current: no ALTER, and nothing logged.
	logs, trace := start(url)
	if trace.has("ALTER EXTENSION") {
		t.Error("the second start ran ALTER EXTENSION")
	}
	if logs.Len() != 0 {
		t.Errorf("the second start logged:\n%s", logs)
	}
	t.Log("A83: second start ran no ALTER and logged nothing")

	// The API's role doesn't own postgis: a WARN for each, and the start goes on.
	exec(`CREATE ROLE ` + role + ` LOGIN PASSWORD 'not-a-secret'`)
	setOlder()
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = role, "not-a-secret"
	other, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(ctx)
	var warn bytes.Buffer
	store.UpdatePostGIS(ctx, other, slog.New(slog.NewJSONHandler(&warn, nil)))
	warned := map[string]bool{}
	for _, l := range logLines(t, &warn) {
		msg, _ := l["msg"].(string)
		name, _ := l["extension"].(string)
		if l["level"] != "WARN" || !strings.Contains(msg, "run the fix as the extension's owner") || l["fix"] != "ALTER EXTENSION "+name+" UPDATE" {
			t.Errorf("unexpected log line: %v", l)
			continue
		}
		warned[name] = true
	}
	for name := range older {
		if !warned[name] {
			t.Errorf("no WARN for %s:\n%s", name, warn.String())
		}
	}
	for name, v := range versions() {
		if v.installed != older[name] {
			t.Errorf("%s changed to %s without its owner", name, v.installed)
		}
	}
	if err := other.Ping(ctx); err != nil {
		t.Errorf("the connection didn't survive the failed updates: %v", err)
	}
	t.Logf("A83: as a role that doesn't own them:\n%s", warn.String())
}
