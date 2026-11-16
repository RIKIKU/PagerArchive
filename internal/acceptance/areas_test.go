package acceptance

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	"github.com/RIKIKU/PagerArchive/internal/areas"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// areaOf is a message's area as one word: its name, or "none".
func areaOf(m server.Message) string {
	if m.Area == nil {
		return "none"
	}
	return *m.Area
}

// pageAreas is every stored page's area, by job number and id order, from
// the database: "EX0000971 A, …".
func pageAreas(t *testing.T, ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) string {
	t.Helper()
	rows, err := q.Query(ctx, `SELECT m.job_number || ' ' || COALESCE(ma.area, 'none') FROM messages m
		LEFT JOIN message_areas ma ON ma.message_id = m.id ORDER BY m.id`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(got, ", ")
}

// A111. Areas (R88-R88e; ADR 0035), with the example's made-up zones: Zone A
// west of 11 FICTION ST, MADEUPTON and along SAMPLE RD, Zone B east of it,
// the edge between them through 11 FICTION ST, and TESTVILLE in neither.
func TestA111Areas(t *testing.T) {
	e := setup(t)
	at := func(i int) time.Time { return e.now.Add(time.Duration(i-10) * time.Minute) }
	ids := map[string]int64{}
	for i, p := range []struct{ key, capcode, text string }{
		{"west", "0100001", "@@EX0000971 HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON #STN1"},   // Zone A
		{"east", "0100002", "@@EX0000971 HSF1 SMOKE FROM ROOF @ 5 EXAMPLE ST, MADEUPTON #STN2"},   // the same job, Zone B
		{"edge", "0100001", "@@EX0000972 HSF1 SMOKE IN SHED @ 11 FICTION ST, MADEUPTON #STN1"},    // on the edge: none
		{"street", "0100001", "@@EX0000973 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1"},         // a street only, approximate: Zone A
		{"outside", "0100001", "@@EX0000974 GRS3 GRASS FIRE @ 4 FICTION ST, TESTVILLE #STN1"},     // in no area
		{"nowhere", "0100001", "@@EX0000975 MED1 PERSON COLLAPSED @ 9 NOWHERE RD, FARAWAY #STN1"}, // no location
		{"east2", "0100003", "@@EX0000976 HSF1 SMOKE FROM ROOF @ 15 FICTION ST, MADEUPTON #STN3"}, // Zone B
	} {
		ids[p.key] = e.mustPost("a", p.capcode, p.text, at(i)).MessageID
	}
	want := "EX0000971 A, EX0000971 B, EX0000972 none, EX0000973 A, EX0000974 none, EX0000975 none, EX0000976 B"
	if got := pageAreas(t, e.ctx, e.st.Pool); got != want {
		t.Errorf("areas stored: %s\nwant %s", got, want)
	}

	// R88b: a message's view, the list, a job's view and the map say it.
	for key, want := range map[string]string{"west": "A", "east": "B", "edge": "none", "street": "A", "outside": "none", "nowhere": "none"} {
		var m server.Message
		e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", ids[key]), nil, &m)
		if got := areaOf(m); got != want {
			t.Errorf("%s's view: area %s, want %s", key, got, want)
		}
	}
	var list server.MessagePage
	e.user.call(http.MethodGet, "/api/v1/messages?job=EX0000971", nil, &list)
	var got []string
	for _, m := range list.Messages {
		got = append(got, areaOf(m))
	}
	if strings.Join(got, " ") != "B A" {
		t.Errorf("EX0000971's list, newest first: %v, want [B A]", got)
	}
	for job, want := range map[string]string{"EX0000971": "A B", "EX0000972": "", "EX0000975": "", "EX0000999": ""} {
		var ja server.JobAreas
		if code, _ := e.user.call(http.MethodGet, "/api/v1/jobs/"+job+"/areas", nil, &ja); code != http.StatusOK || strings.Join(ja.Areas, " ") != want || ja.Areas == nil {
			t.Errorf("%s's areas: %d %v, want %q", job, code, ja.Areas, want)
		}
	}
	var job server.Job
	e.user.call(http.MethodGet, "/api/v1/jobs/EX0000971", nil, &job)
	if len(job.Messages) != 2 || areaOf(job.Messages[0]) != "B" || areaOf(job.Messages[1]) != "A" {
		t.Errorf("EX0000971's job view: %+v", job.Messages)
	}
	var mp server.MapPage
	e.user.call(http.MethodGet, "/api/v1/map", nil, &mp)
	byID := map[int64]string{}
	for _, m := range mp.Messages {
		byID[m.ID] = areaOf(m)
	}
	if byID[ids["west"]] != "A" || byID[ids["east2"]] != "B" || byID[ids["outside"]] != "none" || len(mp.Messages) != 6 {
		t.Errorf("the map's messages: %v (%d), want west A, east2 B, outside none, 6 located", byID, len(mp.Messages))
	}

	// Meta names them, for the map and the labels.
	var meta server.Meta
	e.user.call(http.MethodGet, "/api/v1/meta", nil, &meta)
	if meta.Areas == nil || meta.Areas.Label != "Zone" || meta.Areas.Count != 2 || meta.Areas.Attribution != "Example zones, made up for PagerArchive" || meta.Areas.Version == "" {
		t.Fatalf("meta's areas: %+v", meta.Areas)
	}

	// R88c: the outlines, small, gzipped, and kept until the areas change.
	get := func(path string, header map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, e.url+path, nil)
		req.AddCookie(e.user.cookie)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		// The transport would gunzip it itself, and hide the header.
		resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}
	resp, body := get("/api/v1/map/areas?v="+meta.Areas.Version, map[string]string{"Accept-Encoding": "gzip"})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" ||
		resp.Header.Get("Content-Type") != "application/geo+json" {
		t.Fatalf("outlines: %d, headers %v", resp.StatusCode, resp.Header)
	}
	zr, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var fc struct {
		Type     string `json:"type"`
		Features []struct {
			Properties struct {
				Name  string `json:"name"`
				Label bool   `json:"label"`
			} `json:"properties"`
			Geometry struct {
				Type string `json:"type"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(plain, &fc); err != nil {
		t.Fatal(err)
	}
	var feats []string
	for _, f := range fc.Features {
		feats = append(feats, fmt.Sprintf("%s %s %v", f.Properties.Name, f.Geometry.Type, f.Properties.Label))
	}
	if fc.Type != "FeatureCollection" || strings.Join(feats, ", ") != "A MultiPolygon false, B MultiPolygon false, A Point true, B Point true" || len(plain) > 1<<20 {
		t.Errorf("outlines: %s %s, %d bytes", fc.Type, feats, len(plain))
	}
	t.Logf("the example's outlines: %d bytes, %d gzipped", len(plain), len(body))
	if resp, _ := get("/api/v1/map/areas?v="+meta.Areas.Version, map[string]string{"If-None-Match": resp.Header.Get("ETag")}); resp.StatusCode != http.StatusNotModified {
		t.Errorf("asked again with its ETag: %d, want 304", resp.StatusCode)
	}
	if resp, body := get("/api/v1/map/areas", nil); resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "private, no-cache" || resp.Header.Get("Content-Encoding") != "" || string(body) != string(plain) {
		t.Errorf("without its version: %d %q %q", resp.StatusCode, resp.Header.Get("Cache-Control"), resp.Header.Get("Content-Encoding"))
	}
	// Only for those signed in, like the rest of the map.
	out, err := http.Get(e.url + "/api/v1/map/areas")
	if err != nil {
		t.Fatal(err)
	}
	out.Body.Close()
	if out.StatusCode != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", out.StatusCode)
	}

	// R88e: Administration sees which are loaded; users don't.
	var aa server.AdminAreas
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/areas", nil, &aa); code != http.StatusOK || !aa.Loaded || aa.Label != "Zone" ||
		aa.Source != "example-areas.geojson" || strings.Join(aa.Names, " ") != "A B" || aa.Pending {
		t.Errorf("admin's areas: %d %+v", code, aa)
	}
	if code, _ := e.user.call(http.MethodGet, "/api/v1/admin/areas", nil, nil); code != http.StatusForbidden {
		t.Errorf("a user asking for the admin's areas: %d, want 403", code)
	}

	// A re-parse (R23a) works them out again with the page's location.
	if err := e.st.RequestReparse(e.ctx, db.RequestReparseParams{CapcodePattern: "%", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.worker.RunOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := pageAreas(t, e.ctx, e.st.Pool); got != want {
		t.Errorf("after a re-parse: %s\nwant %s", got, want)
	}

	// R88d: changed areas, the edge moved east past 15 FICTION ST, are loaded
	// again and every page's area worked out again in the background. The
	// network profile's parse hash doesn't change, so nothing is re-parsed.
	dir := t.TempDir()
	for _, f := range []string{"example-network.json", "example-addresses.json", "example-fire.json"} {
		copyFile(t, filepath.Join("../../profiles/example", f), filepath.Join(dir, f))
	}
	geo, err := os.ReadFile("../../profiles/example/example-areas.geojson")
	if err != nil {
		t.Fatal(err)
	}
	moved := strings.ReplaceAll(string(geo), "-140.865155", "-140.8635")
	if err := os.WriteFile(filepath.Join(dir, "example-areas.geojson"), []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	net, err := os.ReadFile(filepath.Join(dir, "example-network.json"))
	if err != nil {
		t.Fatal(err)
	}
	relabelled := strings.Replace(string(net), `"label": "Zone"`, `"label": "Sector"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "example-network.json"), []byte(relabelled), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := profile.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n2 := set.Networks["example-network"]
	if n2.Hash != e.srv.Ingest.Network.Hash || n2.Areas.Label != "Sector" {
		t.Errorf("the profile's parse hash changed with its areas (%s, was %s), or its label didn't (%q)", n2.Hash, e.srv.Ingest.Network.Hash, n2.Areas.Label)
	}
	f, err := areas.ReadFile(n2.Areas.Path, n2.Areas.NameProperty)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := areas.Load(e.ctx, e.st, f)
	if err != nil || stats.Skipped || stats.Areas != 2 {
		t.Fatalf("loading the moved areas: %+v %v", stats, err)
	}
	// Until the background has worked them out, pages keep the area they had.
	if got := pageAreas(t, e.ctx, e.st.Pool); got != want {
		t.Errorf("before the backfill: %s\nwant %s", got, want)
	}
	if pending, err := areas.Pending(e.ctx, e.st); err != nil || !pending {
		t.Errorf("pending before the backfill: %v %v, want true", pending, err)
	}
	if err := areas.Backfill(e.ctx, e.st, nil, 0); err != nil {
		t.Fatal(err)
	}
	wantMoved := "EX0000971 A, EX0000971 B, EX0000972 A, EX0000973 A, EX0000974 none, EX0000975 none, EX0000976 A"
	if got := pageAreas(t, e.ctx, e.st.Pool); got != wantMoved {
		t.Errorf("after the areas moved: %s\nwant %s", got, wantMoved)
	}
	// Loaded again unchanged, they're skipped.
	if stats, err := areas.Load(e.ctx, e.st, f); err != nil || !stats.Skipped {
		t.Errorf("loading the same areas again: %+v %v, want skipped", stats, err)
	}
	// Retention takes a page's area with it (R45).
	if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM messages WHERE id = $1`, ids["west"]); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM message_areas WHERE message_id = $1`, ids["west"]).Scan(&left); err != nil || left != 0 {
		t.Errorf("a deleted page's area: %d rows %v", left, err)
	}
	// Overlapping areas (a mistake in a file): a point in two at once is in
	// none, and a point in one of them is in it.
	overlap := strings.Replace(moved, `"features": [`, `"features": [
    {"type": "Feature", "properties": {"zone": "C"}, "geometry": {"type": "Polygon", "coordinates": [[[-140.9, -34.84], [-140.866, -34.84], [-140.866, -34.82], [-140.9, -34.82], [-140.9, -34.84]]]}},`, 1)
	if err := os.WriteFile(filepath.Join(dir, "example-areas.geojson"), []byte(overlap), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, err = areas.ReadFile(n2.Areas.Path, n2.Areas.NameProperty); err != nil {
		t.Fatal(err)
	}
	if _, err := areas.Load(e.ctx, e.st, f); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		lon, lat float64
		want     string
	}{
		{-140.867343, -34.831226, "none"}, // 1 FICTION ST: in A and C
		{-140.885179, -34.823895, "none"}, // 50 SAMPLE RD: in A and C
		{-140.93, -34.85, "A"},            // in A only
		{-140.862041, -34.834061, "B"},    // in B only
	} {
		var got *string
		if err := e.st.Pool.QueryRow(e.ctx, `SELECT area_at(ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography)`, p.lon, p.lat).Scan(&got); err != nil {
			t.Fatal(err)
		}
		g := "none"
		if got != nil {
			g = *got
		}
		if g != p.want {
			t.Errorf("overlapping areas, (%g, %g): %q, want %s", p.lon, p.lat, g, p.want)
		}
	}
	// With no areas in the profile, every page's area goes.
	if err := areas.Clear(e.ctx, e.st); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM message_areas) + (SELECT count(*) FROM areas) + (SELECT count(*) FROM area_parts)`).Scan(&left); err != nil || left != 0 {
		t.Errorf("after the areas were removed: %d rows %v, want 0", left, err)
	}
	if pending, err := areas.Pending(e.ctx, e.st); err != nil || pending {
		t.Errorf("pending with no areas: %v %v", pending, err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A112. Areas after an upgrade (R88a; ADR 0035): pages stored before
// migration 00026 get their areas in the background, once, resumably, and
// the log holds counts only (P1).
func TestA112AreasAfterUpgrade(t *testing.T) {
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
	if _, err := provider.UpTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	// The previous release's pages, made up, placed where the example's
	// addresses are: in Zone A, Zone B, on the edge, outside, and unplaced.
	for i, p := range []struct {
		job      string
		lon, lat float64
	}{
		{"EX0000981", -140.867343, -34.831226}, // 1 FICTION ST, MADEUPTON: A
		{"EX0000982", -140.862041, -34.834061}, // EXAMPLE ST: B
		{"EX0000983", -140.865155, -34.831224}, // 11 FICTION ST, on the edge: none
		{"EX0000984", -140.869049, -34.651097}, // TESTVILLE: none
		{"EX0000985", 0, 0},                    // not placed
		{"EX0000986", -140.69, -34.85},         // Zone B's second part
	} {
		text := "@@" + p.job + " made-up page " + fmt.Sprint(i)
		exec(`INSERT INTO messages (capcode, text, sent_at, dedup_key, priority, job_number, location, location_source)
		      VALUES ('0100001', $1::text, $2, sha256(convert_to($1::text, 'UTF8')), 'Emergency', $3,
		              CASE WHEN $4::float8 = 0 THEN NULL ELSE ST_SetSRID(ST_MakePoint($4::float8, $5::float8), 4326)::geography END,
		              CASE WHEN $4::float8 = 0 THEN NULL ELSE 'address' END)`,
			text, time.Date(2026, time.October, 6+i, 10, 0, 0, 0, time.UTC), p.job, p.lon, p.lat)
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
	f, err := areas.ReadFile(n.Areas.Path, n.Areas.NameProperty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := areas.Load(ctx, st, f); err != nil {
		t.Fatal(err)
	}
	if got := pageAreas(t, ctx, conn); !strings.HasPrefix(got, "EX0000981 none, EX0000982 none") {
		t.Errorf("before the backfill: %s, want none yet", got)
	}
	var logs syncBuffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	if err := areas.Backfill(ctx, st, log, 0); err != nil {
		t.Fatal(err)
	}
	want := "EX0000981 A, EX0000982 B, EX0000983 none, EX0000984 none, EX0000985 none, EX0000986 B"
	if got := pageAreas(t, ctx, conn); got != want {
		t.Errorf("after the backfill: %s\nwant %s", got, want)
	}
	if v, err := st.GetSetting(ctx, "message_areas_backfill"); err != nil || v != "done" {
		t.Errorf("backfill state %q %v, want done", v, err)
	}
	// Once: done, a start does nothing.
	exec(`DELETE FROM message_areas`)
	if err := areas.Backfill(ctx, st, log, 0); err != nil {
		t.Fatal(err)
	}
	if got := pageAreas(t, ctx, conn); strings.Contains(got, " A") {
		t.Errorf("done, a second start wrote areas: %s", got)
	}
	// Resumable: stopped after the second page, it carries on from the third
	// (the first two keep what they have: here, none).
	var second int64
	if err := conn.QueryRow(ctx, `SELECT id FROM messages WHERE job_number = 'EX0000982'`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE settings SET value = $1 WHERE key = 'message_areas_backfill'`, fmt.Sprint(second))
	if err := areas.Backfill(ctx, st, log, 0); err != nil {
		t.Fatal(err)
	}
	if got, want := pageAreas(t, ctx, conn), "EX0000981 none, EX0000982 none, EX0000983 none, EX0000984 none, EX0000985 none, EX0000986 B"; got != want {
		t.Errorf("resumed: %s\nwant %s", got, want)
	}
	// From the start again: the same as the first time, and a wrong row (a
	// page placed again since) is put right.
	exec(`INSERT INTO message_areas (message_id, area) SELECT id, 'A' FROM messages WHERE job_number = 'EX0000984'`)
	exec(`UPDATE settings SET value = '0' WHERE key = 'message_areas_backfill'`)
	if err := areas.Backfill(ctx, st, log, 0); err != nil {
		t.Fatal(err)
	}
	if got := pageAreas(t, ctx, conn); got != want {
		t.Errorf("run again: %s\nwant %s", got, want)
	}
	out := logs.String()
	if !strings.Contains(out, "stored pages' areas worked out") {
		t.Errorf("no progress in the log:\n%s", out)
	}
	for _, s := range []string{"made-up page", "FICTION", "MADEUPTON", "EX00009"} {
		if strings.Contains(out, s) {
			t.Errorf("the log holds %q", s)
		}
	}
}
