package acceptance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/geo"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// placed fetches a message as the current member and returns its location.
func (e *env) placed(id int64) (server.Message, *server.Location) {
	e.t.Helper()
	var m server.Message
	if code := e.getJSON("/api/v1/messages/"+itoa(id), &m); code != http.StatusOK {
		e.t.Fatalf("GET message %d: %d", id, code)
	}
	return m, m.Location
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// 12 FICTION ST, MADEUPTON in the example dataset.
var fictionSt12 = geo.Point{Lon: -140.864937, Lat: -34.831359}

func TestLocationFromAddressFirst(t *testing.T) {
	e := setup(t)
	// Golden row 1: a house number and a grid reference that agree.
	id := e.mustPost("a", "0100001", "@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1", e.now).MessageID
	e.as = e.admin
	m, loc := e.placed(id)
	if loc == nil || loc.Source != "address" || loc.Approximate || geo.Distance(geo.Point{Lon: loc.Lon, Lat: loc.Lat}, fictionSt12) > 1 {
		t.Fatalf("location %+v, want 12 FICTION ST from the address", loc)
	}
	if m.Flags == nil || m.Flags.LocationMismatch {
		t.Errorf("flags %+v, want no location_mismatch", m.Flags)
	}
}

func TestStreetOnlyAddressIsApproximate(t *testing.T) {
	e := setup(t)
	// Golden row 2: no house number, so the street's centroid (section 14).
	id := e.mustPost("a", "0100002", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2", e.now).MessageID
	_, loc := e.placed(id)
	if loc == nil || loc.Source != "street" || !loc.Approximate {
		t.Fatalf("location %+v, want SAMPLE RD, approximate", loc)
	}
	// The centroid is moved onto one of the street's address points.
	var onStreet bool
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT EXISTS (SELECT 1 FROM address_points WHERE street = 'SAMPLE RD'
		AND ST_DWithin(geom, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography, 0.5))`, loc.Lon, loc.Lat).Scan(&onStreet); err != nil || !onStreet {
		t.Errorf("street point isn't one of SAMPLE RD's address points (%v)", err)
	}
	// A house number that isn't in the dataset falls back to the street.
	id = e.mustPost("a", "0100002", "@@EX0000012 GRS3 GRASS FIRE @ 999 SAMPLE RD, MADEUPTON #STN2", e.now).MessageID
	if _, l2 := e.placed(id); l2 == nil || l2.Source != "street" || l2.Lon != loc.Lon || l2.Lat != loc.Lat {
		t.Errorf("unknown number: %+v, want the street point %+v", l2, loc)
	}
	// 3/12A FICTION ST: a unit with a suffix finds number 12.
	id = e.mustPost("a", "0100002", "@@EX0000013 HSF1 ALARM @ 3/12A FICTION ST, MADEUPTON #STN2", e.now).MessageID
	if _, l3 := e.placed(id); l3 == nil || l3.Source != "address" || geo.Distance(geo.Point{Lon: l3.Lon, Lat: l3.Lat}, fictionSt12) > 1 {
		t.Errorf("3/12A: %+v, want 12 FICTION ST", l3)
	}
}

func TestGridReferenceWhenAddressUnknown(t *testing.T) {
	e := setup(t)
	// The street isn't in the dataset; the grid reference places the page
	// in MADEUPTON's 100 km square (R25).
	id := e.mustPost("a", "0100001", "@@EX0000014 HSF1 SMOKE @ 9 NOWHERE LANE, MADEUPTON GRID 123456 #STN1", e.now).MessageID
	_, loc := e.placed(id)
	if loc == nil || loc.Source != "grid" || loc.Approximate {
		t.Fatalf("location %+v, want from the grid reference", loc)
	}
	e1, n1 := geo.ToUTM(geo.Point{Lon: loc.Lon, Lat: loc.Lat}, 7)
	if d := (e1-512350)*(e1-512350) + (n1-6145650)*(n1-6145650); d > 0.25 {
		t.Errorf("grid point at E %.1f N %.1f, want the centre of cell 07HEB 123 456", e1, n1)
	}
	// No locality we know: nothing to complete the reference from.
	id = e.mustPost("a", "0100001", "@@EX0000015 HSF1 SMOKE @ 9 NOWHERE LANE, NOWHEREVILLE GRID 123456 #STN1", e.now).MessageID
	if _, loc := e.placed(id); loc != nil {
		t.Errorf("unknown locality placed at %+v", loc)
	}
}

func TestLocationMismatch(t *testing.T) {
	e := setup(t)
	// R26: the address and the grid reference are ~100 km apart.
	far := e.mustPost("a", "0100001", "@@EX0000016 HSF1 SMOKE @ 12 FICTION ST, MADEUPTON GRID 999999 #STN1", e.now).MessageID
	// A street-only address is measured to the nearest point on the street:
	// the far end of SAMPLE RD is ~1.75 km from its centroid but on the road.
	end := e.mustPost("a", "0100002", "@@EX0000017 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON GRID 140464 #STN2", e.now).MessageID
	off := e.mustPost("a", "0100002", "@@EX0000018 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON GRID 140600 #STN2", e.now).MessageID

	e.as = e.admin
	for id, want := range map[int64]bool{far: true, end: false, off: true} {
		m, loc := e.placed(id)
		if m.Flags == nil || m.Flags.LocationMismatch != want {
			t.Errorf("message %d: flags %+v, want location_mismatch %v", id, m.Flags, want)
		}
		// The address still wins (R24).
		if loc == nil || loc.Source == "grid" {
			t.Errorf("message %d: location %+v, want from the address", id, loc)
		}
	}
	if ms := e.list("location_mismatch=true"); len(ms) != 2 {
		t.Errorf("admin flag filter: %d messages, want 2", len(ms))
	}
	// Users never see the flag (R35c).
	e.as = e.user
	if m, _ := e.placed(far); m.Flags != nil {
		t.Errorf("user sees flags %+v", m.Flags)
	}
}

func TestMapShowsLocatedMessagesFromTheLast24Hours(t *testing.T) {
	e := setup(t)
	e.srv.Now = func() time.Time { return e.now }
	recent := e.mustPost("a", "0100001", "@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1", e.now.Add(-time.Hour)).MessageID
	other := e.mustPost("a", "0100002", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2", e.now.Add(-2*time.Hour)).MessageID
	e.mustPost("a", "0100001", "@@EX0000019 HSF1 OLD @ 1 EXAMPLE ST, MADEUPTON #STN1", e.now.Add(-25*time.Hour))
	e.mustPost("a", "0100001", "QDTraining Tuesday 7pm at the station", e.now.Add(-time.Minute)) // not placed

	var page server.MapPage
	if code := e.getJSON("/api/v1/map", &page); code != http.StatusOK {
		t.Fatalf("GET map: %d", code)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != recent || page.Messages[1].ID != other || page.Truncated {
		t.Fatalf("map: %d messages, want the 2 located in the last 24 hours", len(page.Messages))
	}
	// The map follows the filters (section 14), including a date range.
	if e.getJSON("/api/v1/map?group=STN2", &page); len(page.Messages) != 1 || page.Messages[0].ID != other {
		t.Errorf("group filter: %d messages", len(page.Messages))
	}
	from := e.now.Add(-48 * time.Hour).Format(time.RFC3339)
	if e.getJSON("/api/v1/map?from="+from, &page); len(page.Messages) != 3 {
		t.Errorf("explicit range: %d messages, want 3", len(page.Messages))
	}
}

func TestMapTilesAreServedWithRanges(t *testing.T) {
	e := setup(t)
	if code := e.getJSON("/api/v1/map/tiles.pmtiles", nil); code != http.StatusNotFound {
		t.Errorf("no MAP_TILES: %d, want 404", code)
	}
	path := filepath.Join(t.TempDir(), "map.pmtiles")
	if err := os.WriteFile(path, bytes.Repeat([]byte("0123456789"), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	e.srv.MapTiles = path
	req, _ := http.NewRequest(http.MethodGet, e.url+"/api/v1/map/tiles.pmtiles", nil)
	req.Header.Set("Range", "bytes=10-19")
	req.AddCookie(e.as.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "0123456789" {
		t.Errorf("range request: %d %q, want 206 and 10 bytes", resp.StatusCode, body)
	}
	// Signed-in users only (A29).
	req, _ = http.NewRequest(http.MethodGet, e.url+"/api/v1/map/tiles.pmtiles", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no session: %v %v, want 401", resp.StatusCode, err)
	}
}

// A11. Capcode report.
func TestA11CapcodeReport(t *testing.T) {
	e := setup(t)
	// UTC deployment in this harness; the dates are in it (R35a).
	day := func(month time.Month, d int) time.Time { return time.Date(2026, month, d, 10, 0, 0, 0, time.UTC) }
	e.mustPost("a", "0100001", "@@EX0000021 HSF1 SMOKE @ 12 FICTION ST, MADEUPTON #STN1", day(time.October, 1))
	e.mustPost("a", "0100001", "@@EX0000022 HSF1 SMOKE @ 3 FICTION ST, MADEUPTON #STN1", day(time.October, 3))
	e.mustPost("a", "0100001", "@@EX0000023 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1", day(time.October, 5))
	e.mustPost("a", "0100001", "QDTraining Tuesday 7pm at the station", day(time.October, 4))
	e.mustPost("a", "0100001", "@@EX0000024 HSF1 SMOKE @ 1 EXAMPLE ST, MADEUPTON #STN1", day(time.September, 28))
	// Not counted: another capcode, and a heartbeat.
	e.mustPost("a", "0100002", "@@EX0000021 HSF1 SMOKE @ 12 FICTION ST, MADEUPTON #STN2", day(time.October, 1))

	var rep server.Report
	if code := e.getJSON("/api/v1/reports/capcode?capcode=0100001&from=2026-10-01&to=2026-10-07", &rep); code != http.StatusOK {
		t.Fatalf("report: %d", code)
	}
	got := map[string]int64{}
	for _, c := range rep.Categories {
		got[c.Category] = c.Count
	}
	if len(rep.Categories) != 2 || got["Structure fire"] != 2 || got["Grass fire"] != 1 || rep.Total != 3 {
		t.Errorf("report %+v, want Structure fire 2, Grass fire 1, total 3", rep)
	}
	if rep.Categories[0].Category != "Structure fire" {
		t.Errorf("categories in network order: %+v", rep.Categories)
	}
}

func TestReportOtherListsRawCodes(t *testing.T) {
	e := setup(t)
	at := time.Date(2026, time.October, 2, 10, 0, 0, 0, time.UTC)
	e.mustPost("a", "0100001", "@@EX0000031 ZZZ9 ODD @ 12 FICTION ST, MADEUPTON #STN1", at)
	e.mustPost("a", "0100001", "@@EX0000032 ZZZ9 ODD @ 12 FICTION ST, MADEUPTON #STN1", at.Add(time.Minute))
	e.mustPost("a", "0100001", "@@GARBLED 123", at.Add(2*time.Minute)) // parse_failed: no code
	var rep server.Report
	e.getJSON("/api/v1/reports/capcode?capcode=100001&from=2026-10-01&to=2026-10-02", &rep)
	if rep.Total != 3 || len(rep.Categories) != 1 || rep.Categories[0].Category != "Other" || len(rep.Categories[0].Codes) != 2 {
		t.Fatalf("report %+v", rep)
	}
	codes := rep.Categories[0].Codes
	if codes[0].Code == nil || *codes[0].Code != "ZZZ9" || codes[0].Count != 2 || codes[1].Code != nil || codes[1].Count != 1 {
		t.Errorf("Other codes %+v, want ZZZ9 2 then (none) 1", codes)
	}
	if code := e.getJSON("/api/v1/reports/capcode?capcode=0100001&from=2026-10-05&to=2026-10-01", nil); code != http.StatusBadRequest {
		t.Errorf("reversed range: %d, want 400", code)
	}
}

// A12. Retention change.
func TestA12RetentionChange(t *testing.T) {
	e := setup(t)
	mel := e.job.Location
	run := time.Date(2026, time.October, 13, 3, 0, 0, 0, mel) // the nightly run
	e.srv.Now = func() time.Time { return run.Add(-time.Hour) }
	for i, years := range []int{3, 2, 1} {
		e.mustPost("a", "0100001", "@@EX000004"+itoa(int64(i))+" HSF1 SMOKE @ 12 FICTION ST, MADEUPTON #STN1", run.AddDate(-years, 0, 0))
	}

	e.as = e.admin
	var st server.Retention
	if code := e.getJSON("/api/v1/admin/retention", &st); code != http.StatusOK || st.Days != 3650 {
		t.Fatalf("retention: %d %+v, want the default 3650 days", code, st)
	}
	// Shortening needs the cut-off date confirmed (R46).
	var confirm server.RetentionConfirm
	if code := e.putJSON("/api/v1/admin/retention", server.RetentionChange{Days: 730}, &confirm); code != http.StatusConflict || confirm.Code != server.CodeConfirmCutoff {
		t.Fatalf("unconfirmed shortening: %d, want 409", code)
	}
	if confirm.Cutoff != "2024-10-13" || confirm.Message != "All messages older than 13 October 2024 will be deleted at the next nightly run. Confirm?" {
		t.Errorf("confirmation %+v", confirm)
	}
	if code, _ := e.as.call(http.MethodPut, "/api/v1/admin/retention", server.RetentionChange{Days: 730, ConfirmCutoff: "2024-10-12"}, nil); code != http.StatusConflict {
		t.Errorf("wrong date confirmed: %d, want 409", code)
	}
	if code, _ := e.as.call(http.MethodPut, "/api/v1/admin/retention", server.RetentionChange{Days: 730, ConfirmCutoff: confirm.Cutoff}, &st); code != http.StatusOK || st.Days != 730 {
		t.Fatalf("confirmed: %d %+v", code, st)
	}
	if len(e.messages()) != 3 {
		t.Fatal("deleted before the nightly run")
	}

	res, err := e.job.RunOnce(e.ctx, run)
	if err != nil || res.Deleted != 1 {
		t.Fatalf("nightly run: %+v %v, want 1 deleted", res, err)
	}
	ms := e.messages()
	if len(ms) != 2 {
		t.Fatalf("%d messages kept, want 2", len(ms))
	}
	for _, m := range ms {
		if m.SentAt.Before(run.AddDate(-2, 0, 0)) {
			t.Errorf("message from %s kept", m.SentAt)
		}
	}
	if n := len(e.receptionsAll()); n != 2 {
		t.Errorf("%d receptions left, want 2 (deleted with their messages)", n)
	}
	// Bounds and roles.
	for _, days := range []int{29, 36501} {
		if code, _ := e.as.call(http.MethodPut, "/api/v1/admin/retention", server.RetentionChange{Days: days}, nil); code != http.StatusUnprocessableEntity {
			t.Errorf("%d days: %d, want 422", days, code)
		}
	}
	// Lengthening needs no confirmation.
	if code, _ := e.as.call(http.MethodPut, "/api/v1/admin/retention", server.RetentionChange{Days: 1000}, nil); code != http.StatusOK {
		t.Errorf("lengthening: %d", code)
	}
	e.as = e.user
	if code := e.getJSON("/api/v1/admin/retention", nil); code != http.StatusForbidden {
		t.Errorf("user: %d, want 403", code)
	}
}

// putJSON PUTs body as the current member and decodes any JSON reply,
// error or not, into out.
func (e *env) putJSON(path string, body, out any) int {
	e.t.Helper()
	resp, err := e.as.do(http.MethodPut, e.url+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (e *env) receptionsAll() []int64 {
	e.t.Helper()
	var ids []int64
	rows, _ := e.st.Pool.Query(e.ctx, `SELECT message_id FROM receptions`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func TestNightlyRedactsStoredMessagesAndReparses(t *testing.T) {
	e := setup(t)
	id := e.mustPost("a", "0100001", "@@EX0000051 HSF1 SMOKE @ 12 FICTION ST, MADEUPTON #STN1", e.now).MessageID
	// As if stored before a rule matching this number existed.
	phone := "0412 345 678"
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE messages SET text = $2, description = $3 WHERE id = $1`, id,
		"@@EX0000051 HSF1 CALL "+phone+" @ 12 FICTION ST, MADEUPTON #STN1", "CALL "+phone); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job.RunOnce(e.ctx, e.now); err != nil {
		t.Fatal(err)
	}
	if hits := e.rowsContaining(phone); len(hits) != 0 {
		t.Errorf("still stored: %v", hits)
	}
	var m server.Message
	e.getJSON("/api/v1/messages/"+itoa(id), &m)
	if s(m.Description) != "CALL REDACTED" || m.Location == nil {
		t.Errorf("re-parsed description %q, location %+v", s(m.Description), m.Location)
	}
	// Rules unchanged since: the next run doesn't rescan.
	if res, err := e.job.RunOnce(e.ctx, e.now.Add(24*time.Hour)); err != nil || res.Redacted != 0 {
		t.Errorf("second run: %+v %v", res, err)
	}
}

func TestNightlySchedule(t *testing.T) {
	e := setup(t)
	mel := e.job.Location
	at := func(d, h, m int) time.Time { return time.Date(2026, time.October, d, h, m, 0, 0, mel) }
	if got := e.job.NextRun(at(13, 2, 59)); !got.Equal(at(13, 3, 0)) {
		t.Errorf("before 03:00: next %s", got)
	}
	if got := e.job.NextRun(at(13, 3, 0)); !got.Equal(at(14, 3, 0)) {
		t.Errorf("at 03:00: next %s", got)
	}
	// The cut-off is a calendar date in the deployment time zone (R45).
	if got := e.job.Cutoff(at(13, 3, 0), 30); !got.Equal(time.Date(2026, time.September, 13, 0, 0, 0, 0, mel)) {
		t.Errorf("cut-off %s", got)
	}
}

// A13. Client liveness.
func TestA13ClientLiveness(t *testing.T) {
	e := setup(t)
	now := e.now
	e.srv.Now = func() time.Time { return now }
	e.mustPost("a", "0000042", "QDHousekeeping", now.Add(-31*time.Minute)) // ignored pages count too (R16)
	e.mustPost("b", "0100001", "QDTraining Tuesday 7pm at the station", now.Add(-5*time.Minute))
	if _, err := e.st.AddClient(e.ctx, "c", "Client C"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.RevokeClient(e.ctx, "c"); err != nil {
		t.Fatal(err)
	}

	e.as = e.admin
	var clients []server.ClientStatus
	if code := e.getJSON("/api/v1/admin/clients", &clients); code != http.StatusOK || len(clients) != 2 {
		t.Fatalf("clients: %d %+v, want A and B (revoked C not listed)", code, clients)
	}
	got := map[string]bool{}
	for _, c := range clients {
		got[c.ID] = c.Receiving
	}
	if got["a"] || !got["b"] {
		t.Errorf("receiving %v, want A not receiving and B receiving", got)
	}
	// A client never heard from counts from when it was added.
	e.srv.Now = func() time.Time { return time.Now().Add(31 * time.Minute) }
	if _, err := e.st.AddClient(e.ctx, "d", "Client D"); err != nil {
		t.Fatal(err)
	}
	e.getJSON("/api/v1/admin/clients", &clients)
	for _, c := range clients {
		if c.ID == "d" && c.Receiving {
			t.Error("client D never heard from in 31 minutes, shown receiving")
		}
	}
	e.as = e.user
	if code := e.getJSON("/api/v1/admin/clients", nil); code != http.StatusForbidden {
		t.Errorf("user: %d, want 403", code)
	}
}
