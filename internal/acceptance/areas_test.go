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

// areaOf is a message's areas as one word: its zone and its region, "A/North",
// each "none" when it's in none of that layer.
func areaOf(m server.Message) string {
	zone, region := "none", "none"
	for _, a := range m.Areas {
		switch a.Layer {
		case "zones":
			zone = a.Name
		case "regions":
			region = a.Name
		}
	}
	return zone + "/" + region
}

// refs is a list of areas as "zones:A regions:North".
func refs(as []server.AreaRef) string {
	var out []string
	for _, a := range as {
		out = append(out, a.Layer+":"+a.Name)
	}
	return strings.Join(out, " ")
}

// pageAreas is every stored page's zone and region, by job number and id
// order, from the database: "EX0000971 A/North, …".
func pageAreas(t *testing.T, ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) string {
	t.Helper()
	rows, err := q.Query(ctx, `SELECT m.job_number || ' '
		|| COALESCE((SELECT area FROM message_areas WHERE message_id = m.id AND layer = 'zones'), 'none') || '/'
		|| COALESCE((SELECT area FROM message_areas WHERE message_id = m.id AND layer = 'regions'), 'none')
		FROM messages m ORDER BY m.id`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(got, ", ")
}

// layer is the example profile's layer of areas with this id, from dir.
func layer(t *testing.T, dir, id string) *profile.Areas {
	t.Helper()
	set, err := profile.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range set.Networks["example-network"].Areas {
		if l.ID == id {
			return l
		}
	}
	t.Fatalf("no layer %q in %s", id, dir)
	return nil
}

// A111. Areas (R88-R88e; ADR 0035), with the example's two made-up layers:
// zones (Zone A west of 11 FICTION ST, MADEUPTON and along SAMPLE RD, Zone B
// east of it, the edge between them through 11 FICTION ST) and, over them,
// regions (Region North over MADEUPTON north of 1 EXAMPLE ST, Region South
// below). TESTVILLE is in neither.
func TestA111Areas(t *testing.T) {
	e := setup(t)
	at := func(i int) time.Time { return e.now.Add(time.Duration(i-10) * time.Minute) }
	ids := map[string]int64{}
	for i, p := range []struct{ key, capcode, text string }{
		{"west", "0100001", "@@EX0000971 HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON #STN1"},   // Zone A, Region North
		{"east", "0100002", "@@EX0000971 HSF1 SMOKE FROM ROOF @ 5 EXAMPLE ST, MADEUPTON #STN2"},   // the same job, Zone B, Region North
		{"edge", "0100001", "@@EX0000972 HSF1 SMOKE IN SHED @ 11 FICTION ST, MADEUPTON #STN1"},    // on the zones' edge: no zone, Region North
		{"street", "0100001", "@@EX0000973 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1"},         // a street only, approximate: Zone A, Region North
		{"outside", "0100001", "@@EX0000974 GRS3 GRASS FIRE @ 4 FICTION ST, TESTVILLE #STN1"},     // in no area of either
		{"nowhere", "0100001", "@@EX0000975 MED1 PERSON COLLAPSED @ 9 NOWHERE RD, FARAWAY #STN1"}, // no location
		{"east2", "0100003", "@@EX0000976 HSF1 SMOKE FROM ROOF @ 15 FICTION ST, MADEUPTON #STN3"}, // Zone B, Region North
		{"south", "0100003", "@@EX0000977 HSF1 SMOKE FROM ROOF @ 1 EXAMPLE ST, MADEUPTON #STN3"},  // Zone B, Region South
	} {
		ids[p.key] = e.mustPost("a", p.capcode, p.text, at(i)).MessageID
	}
	want := "EX0000971 A/North, EX0000971 B/North, EX0000972 none/North, EX0000973 A/North, EX0000974 none/none, EX0000975 none/none, EX0000976 B/North, EX0000977 B/South"
	if got := pageAreas(t, e.ctx, e.st.Pool); got != want {
		t.Errorf("areas stored: %s\nwant %s", got, want)
	}

	// R88b: a message's view, the list, a job's view and the map say each
	// layer's, in the profile's order.
	for key, want := range map[string]string{"west": "A/North", "east": "B/North", "edge": "none/North", "street": "A/North", "outside": "none/none", "nowhere": "none/none", "south": "B/South"} {
		var m server.Message
		e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", ids[key]), nil, &m)
		if got := areaOf(m); got != want {
			t.Errorf("%s's view: areas %s, want %s", key, got, want)
		}
	}
	var west server.Message
	e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", ids["west"]), nil, &west)
	if got := refs(west.Areas); got != "zones:A regions:North" {
		t.Errorf("west's areas in order: %s, want zones first", got)
	}
	var list server.MessagePage
	e.user.call(http.MethodGet, "/api/v1/messages?job=EX0000971", nil, &list)
	var got []string
	for _, m := range list.Messages {
		got = append(got, areaOf(m))
	}
	if strings.Join(got, " ") != "B/North A/North" {
		t.Errorf("EX0000971's list, newest first: %v, want [B/North A/North]", got)
	}
	for job, want := range map[string]string{"EX0000971": "zones:A zones:B regions:North", "EX0000972": "regions:North", "EX0000974": "", "EX0000975": "", "EX0000999": ""} {
		var ja server.JobAreas
		if code, _ := e.user.call(http.MethodGet, "/api/v1/jobs/"+job+"/areas", nil, &ja); code != http.StatusOK || refs(ja.Areas) != want || ja.Areas == nil {
			t.Errorf("%s's areas: %d %v, want %q", job, code, ja.Areas, want)
		}
	}
	var job server.Job
	e.user.call(http.MethodGet, "/api/v1/jobs/EX0000971", nil, &job)
	if len(job.Messages) != 2 || areaOf(job.Messages[0]) != "B/North" || areaOf(job.Messages[1]) != "A/North" {
		t.Errorf("EX0000971's job view: %+v", job.Messages)
	}
	var mp server.MapPage
	e.user.call(http.MethodGet, "/api/v1/map", nil, &mp)
	byID := map[int64]string{}
	for _, m := range mp.Messages {
		byID[m.ID] = areaOf(m)
	}
	if byID[ids["west"]] != "A/North" || byID[ids["south"]] != "B/South" || byID[ids["outside"]] != "none/none" || len(mp.Messages) != 7 {
		t.Errorf("the map's messages: %v (%d), want west A/North, south B/South, outside none/none, 7 located", byID, len(mp.Messages))
	}

	// Meta names each layer, for the map and the labels, in the profile's order.
	var meta server.Meta
	e.user.call(http.MethodGet, "/api/v1/meta", nil, &meta)
	if len(meta.Areas) != 2 {
		t.Fatalf("meta's areas: %+v", meta.Areas)
	}
	if z, r := meta.Areas[0], meta.Areas[1]; z.ID != "zones" || z.Label != "Zone" || z.Count != 2 || z.Outline != "solid" || z.Attribution != "Example zones, made up for PagerArchive" || z.Version == "" ||
		r.ID != "regions" || r.Label != "Region" || r.Count != 2 || r.Outline != "dashed" || r.Attribution != "Example regions, made up for PagerArchive" || r.Version == "" {
		t.Fatalf("meta's areas: %+v", meta.Areas)
	}

	// R88c: each layer's outlines, small, gzipped, and kept until its areas
	// change; together well under 1 MB.
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
	total := 0
	for _, l := range []struct{ id, version, want string }{
		{"zones", meta.Areas[0].Version, "edges, A Point true, B Point true"},
		{"regions", meta.Areas[1].Version, "edges, North Point true, South Point true"},
	} {
		path := "/api/v1/map/areas/" + l.id
		resp, body := get(path+"?v="+l.version, map[string]string{"Accept-Encoding": "gzip"})
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" ||
			resp.Header.Get("Content-Type") != "application/geo+json" {
			t.Fatalf("%s's outlines: %d, headers %v", l.id, resp.StatusCode, resp.Header)
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
					Edges bool   `json:"edges"`
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
			if f.Properties.Edges && f.Properties.Name == "" && strings.HasSuffix(f.Geometry.Type, "LineString") {
				feats = append(feats, "edges")
				continue
			}
			feats = append(feats, fmt.Sprintf("%s %s %v", f.Properties.Name, f.Geometry.Type, f.Properties.Label))
		}
		if fc.Type != "FeatureCollection" || strings.Join(feats, ", ") != l.want {
			t.Errorf("%s's outlines: %s %s", l.id, fc.Type, feats)
		}
		total += len(plain)
		t.Logf("the example's %s outlines: %d bytes, %d gzipped", l.id, len(plain), len(body))
		if resp, _ := get(path+"?v="+l.version, map[string]string{"If-None-Match": resp.Header.Get("ETag")}); resp.StatusCode != http.StatusNotModified {
			t.Errorf("%s asked again with its ETag: %d, want 304", l.id, resp.StatusCode)
		}
		if resp, body := get(path, nil); resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "private, no-cache" || resp.Header.Get("Content-Encoding") != "" || string(body) != string(plain) {
			t.Errorf("%s without its version: %d %q %q", l.id, resp.StatusCode, resp.Header.Get("Cache-Control"), resp.Header.Get("Content-Encoding"))
		}
		// Only for those signed in, like the rest of the map.
		out, err := http.Get(e.url + path)
		if err != nil {
			t.Fatal(err)
		}
		out.Body.Close()
		if out.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s signed out: %d, want 401", l.id, out.StatusCode)
		}
	}
	if total > 1<<20 {
		t.Errorf("the outlines weigh %d bytes together, want under 1 MB", total)
	}
	if resp, _ := get("/api/v1/map/areas/nosuch", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a layer the profile doesn't name: %d, want 404", resp.StatusCode)
	}

	// R88e: Administration sees each layer; users don't.
	var aa server.AdminAreas
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/areas", nil, &aa); code != http.StatusOK || len(aa.Layers) != 2 {
		t.Fatalf("admin's areas: %d %+v", code, aa)
	}
	for i, w := range []struct{ id, label, source, names string }{{"zones", "Zone", "example-areas.geojson", "A B"}, {"regions", "Region", "example-regions.geojson", "North South"}} {
		l := aa.Layers[i]
		if l.ID != w.id || !l.Loaded || l.Label != w.label || l.Source != w.source || strings.Join(l.Names, " ") != w.names || l.Pending || l.Error != "" || l.Stale {
			t.Errorf("admin's layer %d: %+v, want %+v", i, l, w)
		}
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

	// R88d: changed zones, the edge moved east past 15 FICTION ST, are
	// loaded again and every page's zone worked out again in the background;
	// the regions aren't touched. The network profile's parse hash doesn't
	// change, so nothing is re-parsed.
	dir := t.TempDir()
	for _, f := range []string{"example-network.json", "example-addresses.json", "example-fire.json", "example-regions.geojson"} {
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
	if n2.Hash != e.srv.Ingest.Network.Hash || n2.Areas[0].Label != "Sector" {
		t.Errorf("the profile's parse hash changed with its areas (%s, was %s), or its label didn't (%q)", n2.Hash, e.srv.Ingest.Network.Hash, n2.Areas[0].Label)
	}
	// A region's row put wrong: the regions aren't worked out again, so it stays.
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE message_areas SET area = 'South' WHERE message_id = $1 AND layer = 'regions'`, ids["east2"]); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	started := areas.Start(e.ctx, e.st, n2.Areas, slog.New(slog.NewJSONHandler(&logs, nil)))
	if len(started) != 2 || started[0].Error != "" || started[1].Error != "" || started[0].Label != "Sector" {
		t.Fatalf("started again: %+v", started)
	}
	if !strings.Contains(logs.String(), `"layer":"zones"`) || strings.Contains(logs.String(), `"layer":"regions"`) {
		t.Errorf("only the zones were to load again:\n%s", logs.String())
	}
	// Until the background has worked them out, pages keep the zone they had.
	wantWrong := strings.Replace(want, "EX0000976 B/North", "EX0000976 B/South", 1)
	if got := pageAreas(t, e.ctx, e.st.Pool); got != wantWrong {
		t.Errorf("before the backfill: %s\nwant %s", got, wantWrong)
	}
	for l, want := range map[string]bool{"zones": true, "regions": false} {
		if pending, err := areas.Pending(e.ctx, e.st, l); err != nil || pending != want {
			t.Errorf("%s pending before the backfill: %v %v, want %v", l, pending, err, want)
		}
	}
	if err := areas.Backfill(e.ctx, e.st, nil, 0); err != nil {
		t.Fatal(err)
	}
	wantMoved := "EX0000971 A/North, EX0000971 B/North, EX0000972 A/North, EX0000973 A/North, EX0000974 none/none, EX0000975 none/none, EX0000976 A/South, EX0000977 B/South"
	if got := pageAreas(t, e.ctx, e.st.Pool); got != wantMoved {
		t.Errorf("after the zones moved: %s\nwant %s", got, wantMoved)
	}
	// Loaded again unchanged, they're skipped.
	f, err := areas.ReadFile(n2.Areas[0].Path, n2.Areas[0].NameProperty)
	if err != nil {
		t.Fatal(err)
	}
	if stats, err := areas.Load(e.ctx, e.st, "zones", f); err != nil || !stats.Skipped {
		t.Errorf("loading the same zones again: %+v %v, want skipped", stats, err)
	}
	// Retention takes a page's areas with it (R45).
	if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM messages WHERE id = $1`, ids["west"]); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM message_areas WHERE message_id = $1`, ids["west"]).Scan(&left); err != nil || left != 0 {
		t.Errorf("a deleted page's areas: %d rows %v", left, err)
	}
	// Overlapping areas in one layer (a mistake in a file): a point in two
	// at once is in none of that layer, and a point in one of them is in it.
	// The other layer overlapping it is no mistake: it's another layer.
	overlap := strings.Replace(moved, `"features": [`, `"features": [
    {"type": "Feature", "properties": {"zone": "C"}, "geometry": {"type": "Polygon", "coordinates": [[[-140.9, -34.84], [-140.866, -34.84], [-140.866, -34.82], [-140.9, -34.82], [-140.9, -34.84]]]}},`, 1)
	if err := os.WriteFile(filepath.Join(dir, "example-areas.geojson"), []byte(overlap), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, err = areas.ReadFile(n2.Areas[0].Path, n2.Areas[0].NameProperty); err != nil {
		t.Fatal(err)
	}
	if _, err := areas.Load(e.ctx, e.st, "zones", f); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		lon, lat     float64
		zone, region string
	}{
		{-140.867343, -34.831226, "none", "North"}, // 1 FICTION ST: in A and C
		{-140.885179, -34.823895, "none", "North"}, // 50 SAMPLE RD: in A and C
		{-140.93, -34.85, "A", "South"},            // in A only
		{-140.862041, -34.834061, "B", "South"},    // in B only
	} {
		var zone, region *string
		if err := e.st.Pool.QueryRow(e.ctx, `SELECT area_at('zones', g), area_at('regions', g) FROM (SELECT ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography AS g) s`,
			p.lon, p.lat).Scan(&zone, &region); err != nil {
			t.Fatal(err)
		}
		z, r := "none", "none"
		if zone != nil {
			z = *zone
		}
		if region != nil {
			r = *region
		}
		if z != p.zone || r != p.region {
			t.Errorf("overlapping zones, (%g, %g): %s/%s, want %s/%s", p.lon, p.lat, z, r, p.zone, p.region)
		}
	}
	// A layer the profile no longer names goes, with every page's area of
	// it; the other stays.
	if err := areas.Clear(e.ctx, e.st, []string{"zones"}); err != nil {
		t.Fatal(err)
	}
	var regions, zones int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM message_areas WHERE layer = 'regions') + (SELECT count(*) FROM areas WHERE layer = 'regions') + (SELECT count(*) FROM area_parts WHERE layer = 'regions'),
		(SELECT count(*) FROM areas WHERE layer = 'zones')`).Scan(&regions, &zones); err != nil || regions != 0 || zones != 3 {
		t.Errorf("after the regions were removed: %d rows of them %v, want 0; %d zones, want 3", regions, err, zones)
	}
	// And with no areas in the profile, every page's area goes.
	if err := areas.Clear(e.ctx, e.st, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM message_areas) + (SELECT count(*) FROM areas) + (SELECT count(*) FROM area_parts)`).Scan(&left); err != nil || left != 0 {
		t.Errorf("after the areas were removed: %d rows %v, want 0", left, err)
	}
	if pending, err := areas.Pending(e.ctx, e.st, "zones"); err != nil || pending {
		t.Errorf("pending with no areas: %v %v", pending, err)
	}
}

// A114. Areas that don't load don't stop the API (R88d; ADR 0035): a layer
// whose file is missing, or broken, is logged as an error and Administration
// says why; one loaded before keeps the areas it had until its file is put
// right; the other layers load as ever.
func TestA114AreasThatDontLoad(t *testing.T) {
	e := setup(t)
	id := e.mustPost("a", "0100001", "@@EX0000991 HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON #STN1", e.now).MessageID
	dir := t.TempDir()
	for _, f := range []string{"example-network.json", "example-addresses.json", "example-fire.json", "example-areas.geojson"} {
		copyFile(t, filepath.Join("../../profiles/example", f), filepath.Join(dir, f))
	}
	if err := os.WriteFile(filepath.Join(dir, "example-areas.geojson"), []byte(`{"type": "FeatureCollection", "features": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The regions' file is missing, as A15's was; the zones' is broken, but
	// the zones were loaded before (by setup).
	zones, regions := layer(t, dir, "zones"), layer(t, dir, "regions")
	var logs syncBuffer
	started := areas.Start(e.ctx, e.st, []*profile.Areas{zones, regions}, slog.New(slog.NewJSONHandler(&logs, nil)))
	if len(started) != 2 {
		t.Fatalf("started: %+v", started)
	}
	z, r := started[0], started[1]
	if !strings.Contains(z.Error, "example-areas.geojson: at /features: has no features") || !z.Stale || !z.Shown() || strings.Join(z.Names, " ") != "A B" {
		t.Errorf("the broken zones: %+v", z)
	}
	// The regions were loaded before too (by setup), so they're kept the same way.
	if !strings.Contains(r.Error, "example-regions.geojson") || !r.Stale || !r.Shown() {
		t.Errorf("the missing regions: %+v", r)
	}
	for _, s := range []string{`"level":"ERROR"`, "areas didn't load", `"layer":"zones"`, `"layer":"regions"`, "has no features", "no such file"} {
		if !strings.Contains(logs.String(), s) {
			t.Errorf("the log has no %q:\n%s", s, logs.String())
		}
	}
	// Nothing loaded before: a new layer whose file is missing has nothing to show.
	fresh := *regions
	fresh.ID = "fresh"
	got := areas.Start(e.ctx, e.st, []*profile.Areas{zones, regions, &fresh}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if f := got[2]; f.Error == "" || f.Stale || f.Shown() {
		t.Errorf("a new layer with no file: %+v", f)
	}
	// The pages keep their areas, and the API shows the ones kept.
	e.srv.Areas = got
	var m server.Message
	e.user.call(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", id), nil, &m)
	if areaOf(m) != "A/North" {
		t.Errorf("a page's areas with its files broken: %s, want A/North", areaOf(m))
	}
	var meta server.Meta
	e.user.call(http.MethodGet, "/api/v1/meta", nil, &meta)
	if len(meta.Areas) != 2 || meta.Areas[0].ID != "zones" || meta.Areas[1].ID != "regions" {
		t.Errorf("meta's areas: %+v, want zones and regions, not the one with nothing to show", meta.Areas)
	}
	var aa server.AdminAreas
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/areas", nil, &aa); code != http.StatusOK || len(aa.Layers) != 3 {
		t.Fatalf("admin's areas: %d %+v", code, aa)
	}
	if l := aa.Layers[0]; !l.Loaded || !l.Stale || !strings.Contains(l.Error, "has no features") {
		t.Errorf("admin's zones: %+v", l)
	}
	if l := aa.Layers[2]; l.ID != "fresh" || l.Loaded || l.Stale || !strings.Contains(l.Error, "no such file") || len(l.Names) != 0 || l.Pending {
		t.Errorf("admin's fresh layer: %+v", l)
	}
	// Put right, it loads at the next start.
	copyFile(t, "../../profiles/example/example-areas.geojson", filepath.Join(dir, "example-areas.geojson"))
	if got := areas.Start(e.ctx, e.st, []*profile.Areas{zones}, slog.New(slog.NewJSONHandler(io.Discard, nil))); got[0].Error != "" || got[0].Stale || !got[0].Shown() {
		t.Errorf("put right: %+v", got[0])
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
// the log holds counts only (P1). A layer added later is worked out on its
// own: the layers there already aren't read again.
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
		{"EX0000981", -140.867343, -34.831226}, // 1 FICTION ST, MADEUPTON: A, North
		{"EX0000982", -140.862041, -34.834061}, // EXAMPLE ST: B, South
		{"EX0000983", -140.865155, -34.831224}, // 11 FICTION ST, on the zones' edge: none, North
		{"EX0000984", -140.869049, -34.651097}, // TESTVILLE: none
		{"EX0000985", 0, 0},                    // not placed
		{"EX0000986", -140.69, -34.85},         // Zone B's second part, in no region
	} {
		text := "@@" + p.job + " made-up page " + fmt.Sprint(i)
		exec(`INSERT INTO messages (capcode, text, sent_at, dedup_key, priority, job_number, location, location_source)
		      VALUES ('0100001', $1::text, $2, sha256(convert_to($1::text, 'UTF8')), 'Emergency', $3,
		              CASE WHEN $4::float8 = 0 THEN NULL ELSE ST_SetSRID(ST_MakePoint($4::float8, $5::float8), 4326)::geography END,
		              CASE WHEN $4::float8 = 0 THEN NULL ELSE 'address' END)`,
			text, time.Date(2026, time.October, 6+i, 10, 0, 0, 0, time.UTC), p.job, p.lon, p.lat)
	}

	// The upgrade: what the API runs on start, with the zones only at first.
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	zones, regions := layer(t, "../../profiles/example", "zones"), layer(t, "../../profiles/example", "regions")
	if l := areas.Start(ctx, st, []*profile.Areas{zones}, slog.New(slog.NewJSONHandler(io.Discard, nil))); l[0].Error != "" {
		t.Fatal(l[0].Error)
	}
	if got := pageAreas(t, ctx, conn); !strings.HasPrefix(got, "EX0000981 none/none, EX0000982 none/none") {
		t.Errorf("before the backfill: %s, want none yet", got)
	}
	var logs syncBuffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	if err := areas.Backfill(ctx, st, log, 0); err != nil {
		t.Fatal(err)
	}
	want := "EX0000981 A/none, EX0000982 B/none, EX0000983 none/none, EX0000984 none/none, EX0000985 none/none, EX0000986 B/none"
	if got := pageAreas(t, ctx, conn); got != want {
		t.Errorf("after the backfill: %s\nwant %s", got, want)
	}
	if v, err := st.GetSetting(ctx, "message_areas_backfill:zones"); err != nil || v != "done" {
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
	exec(`UPDATE settings SET value = $1 WHERE key = 'message_areas_backfill:zones'`, fmt.Sprint(second))
	if err := areas.Backfill(ctx, st, log, 0); err != nil {
		t.Fatal(err)
	}
	if got, want := pageAreas(t, ctx, conn), "EX0000981 none/none, EX0000982 none/none, EX0000983 none/none, EX0000984 none/none, EX0000985 none/none, EX0000986 B/none"; got != want {
		t.Errorf("resumed: %s\nwant %s", got, want)
	}
	// From the start again: the same as the first time, and a wrong row (a
	// page placed again since) is put right.
	exec(`INSERT INTO message_areas (message_id, layer, area) SELECT id, 'zones', 'A' FROM messages WHERE job_number = 'EX0000984'`)
	exec(`UPDATE settings SET value = '0' WHERE key = 'message_areas_backfill:zones'`)
	if err := areas.Backfill(ctx, st, log, 0); err != nil {
		t.Fatal(err)
	}
	if got := pageAreas(t, ctx, conn); got != want {
		t.Errorf("run again: %s\nwant %s", got, want)
	}

	// The regions added: only they are worked out. A zone put wrong stays
	// wrong, so the zones weren't read again.
	exec(`UPDATE message_areas SET area = 'B' WHERE layer = 'zones' AND message_id = (SELECT id FROM messages WHERE job_number = 'EX0000981')`)
	if l := areas.Start(ctx, st, []*profile.Areas{zones, regions}, log); l[0].Error != "" || l[1].Error != "" {
		t.Fatalf("with the regions: %+v", l)
	}
	if v, err := st.GetSetting(ctx, "message_areas_backfill:zones"); err != nil || v != "done" {
		t.Errorf("the zones' backfill state %q %v after the regions were added, want done", v, err)
	}
	if err := areas.Backfill(ctx, st, log, 0); err != nil {
		t.Fatal(err)
	}
	if got, want := pageAreas(t, ctx, conn), "EX0000981 B/North, EX0000982 B/South, EX0000983 none/North, EX0000984 none/none, EX0000985 none/none, EX0000986 B/none"; got != want {
		t.Errorf("with the regions: %s\nwant %s", got, want)
	}
	out := logs.String()
	if !strings.Contains(out, "stored pages' areas worked out") || !strings.Contains(out, `"layer":"regions"`) {
		t.Errorf("no progress in the log:\n%s", out)
	}
	for _, s := range []string{"made-up page", "FICTION", "MADEUPTON", "EX00009"} {
		if strings.Contains(out, s) {
			t.Errorf("the log holds %q", s)
		}
	}
}
