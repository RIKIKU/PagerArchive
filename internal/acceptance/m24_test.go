package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/reports"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// M24's reports (R38a-R38i, ADR 0031), on SPEC.md's report fixture: made-up
// jobs in the week of Monday 5 October 2026, in the deployment's time zone
// (UTC here).

// reportDay is a time on a day of October 2026 (day 5 is a Monday).
func reportDay(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, time.UTC)
}

// reportFixture posts SPEC.md's report fixture (A91).
func reportFixture(t *testing.T, e *env) {
	t.Helper()
	profile := "example-fire"
	rescue, err := e.st.CreateTemplate(e.ctx, db.CreateTemplateParams{Name: "Example Rescue", Agency: "Example Rescue Service", AgencyProfile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateAlias(e.ctx, db.CreateAliasParams{Pattern: "0200001", Name: "Rescue 1", TemplateID: &rescue}); err != nil {
		t.Fatal(err)
	}
	if err := e.catAdd("0300009", "Network test", "Example Fire", true); err != nil {
		t.Fatal(err)
	}
	pages := []struct {
		capcode string
		text    string
		at      time.Time
	}{
		// The week before (28 September to 4 October): one job, Station 1's.
		{"0100001", "@@EX0000890 HSF1 SMOKE FROM SHED @ 1 FICTION ST, MADEUPTON UNITS P1 #STN1", time.Date(2026, time.September, 28, 10, 0, 0, 0, time.UTC)},
		// EX0000901: Monday 14:10 to Stations 1-3, Station 1 again at 14:12,
		// and a follow-up at 16:40: 5 pages, 3 capcodes, appliances P1 and T1.
		{"0100001", "@@EX0000901 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON UNITS P1 T1 #STN1", reportDay(5, 14, 10)},
		{"0100002", "@@EX0000901 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON UNITS P1 T1 #STN2", reportDay(5, 14, 10)},
		{"0100003", "@@EX0000901 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON UNITS P1 T1 #STN3", reportDay(5, 14, 10)},
		{"0100001", "@@EX0000901 HSF1 SMOKE FROM ROOF, CALL BACK @ 12 FICTION ST, MADEUPTON UNITS P1 T1 #STN1", reportDay(5, 14, 12)},
		{"0100001", "HbRE EX0000901 CREWS RETURNING #STN1", reportDay(5, 16, 40)},
		// EX0000902: Tuesday 09:05, Station 2, Testville.
		{"0100002", "@@EX0000902 GRS3 GRASS FIRE @ SAMPLE RD, TESTVILLE UNITS T1 #STN2", reportDay(6, 9, 5)},
		// A training page with no job number: a job of its own, Admin.
		{"0100003", "QDTraining Wednesday 7pm at the station", reportDay(7, 19, 0)},
		// Not counted: a heartbeat, and a page to an ignored alias.
		{"0000050", "QDHEARTBEAT", reportDay(7, 12, 0)},
		{"0300009", "@@EX0000906 HSF1 NETWORK TEST @ 1 FICTION ST, MADEUPTON UNITS X9 #NET", reportDay(7, 12, 5)},
		// EX0000903: Saturday 14:30 and 14:31, Stations 1 and 2.
		{"0100001", "@@EX0000903 HSF1 SMOKE IN KITCHEN @ 3 FICTION ST, MADEUPTON UNITS P1 #STN1", reportDay(10, 14, 30)},
		{"0100002", "@@EX0000903 HSF1 SMOKE IN KITCHEN @ 3 FICTION ST, MADEUPTON UNITS P1 #STN2", reportDay(10, 14, 31)},
		// EX0000904: Saturday 14:50, Rescue 1, Samplebury (not in the dataset).
		{"0200001", "@@EX0000904 MED1 PERSON FALLEN @ 4 OTHER RD, SAMPLEBURY UNITS R1 #RSQ1", reportDay(10, 14, 50)},
		// EX0000905: Sunday 03:15, a capcode with no alias (text only).
		{"0999999", "@@EX0000905 HSF1 SMOKE SEEN @ 5 FICTION ST, MADEUPTON #STN9", reportDay(11, 3, 15)},
	}
	for _, p := range pages {
		e.mustPost("a", p.capcode, p.text, p.at)
	}
}

// reportPrivate is page text in the report fixture: descriptions and
// addresses, which no report may hold (R38a, P1).
var reportPrivate = []string{"SMOKE FROM ROOF", "SMOKE FROM SHED", "CALL BACK", "CREWS RETURNING", "GRASS FIRE", "Training Wednesday",
	"HEARTBEAT", "NETWORK TEST", "SMOKE IN KITCHEN", "PERSON FALLEN", "SMOKE SEEN", "FICTION ST", "SAMPLE RD", "OTHER RD"}

// report asks for a report as the signed-in user and decodes it, after
// checking it holds no page text.
func (e *env) report(name, query string, v any) {
	e.t.Helper()
	resp, err := e.as.do(http.MethodGet, e.url+"/api/v1/reports/"+name+"?"+query, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("report %s?%s: %d %s", name, query, resp.StatusCode, body)
	}
	for _, text := range reportPrivate {
		if strings.Contains(string(body), text) {
			e.t.Errorf("report %s holds page text %q", name, text)
		}
	}
	// A fresh value: decoding into a used one keeps fields the JSON leaves out.
	reflect.ValueOf(v).Elem().SetZero()
	if err := json.Unmarshal(body, v); err != nil {
		e.t.Fatal(err)
	}
}

// catSetIgnored marks an alias ignored or not, as an admin does (R53).
func (e *env) catSetIgnored(pattern string, ignored bool) error {
	e.t.Helper()
	var id int64
	var name string
	var tpl *int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT id, name, template_id FROM aliases WHERE pattern = $1", pattern).Scan(&id, &name, &tpl); err != nil {
		e.t.Fatal(err)
	}
	return e.cat.EditAlias(e.ctx, id, catalog.AliasInput{Pattern: pattern, Name: name, TemplateID: tpl, Ignored: ignored}, byAdmin)
}

const reportWeek = "from=2026-10-05&to=2026-10-11"

func busiestRows(r server.BusiestReport) string {
	var out []string
	for _, row := range r.Rows {
		who := row.Alias
		if who == "" {
			who = row.Capcode
		}
		out = append(out, fmt.Sprintf("%d %s %d %.0f%% was %d", row.Rank, who, row.Jobs, row.Share*100, row.PreviousJobs))
	}
	return strings.Join(out, "; ")
}

// A91. Busiest capcodes (R38b).
func TestA91BusiestCapcodes(t *testing.T) {
	e := setup(t)
	reportFixture(t, e)
	var r server.BusiestReport
	e.report("busiest", reportWeek, &r)
	if want := "1 Station 2 3 50% was 0; 2 Station 1 2 33% was 1; 2 Station 3 2 33% was 0; 4 Rescue 1 1 17% was 0; 4 0999999 1 17% was 0"; busiestRows(r) != want {
		t.Errorf("busiest: %s\nwant %s", busiestRows(r), want)
	}
	if r.Jobs != 6 || r.PreviousJobs != 1 || r.PreviousFrom != "2026-09-28" || r.PreviousTo != "2026-10-04" || r.Limit != 10 {
		t.Errorf("jobs %d, previous %d (%s to %s), limit %d; want 6, 1 (2026-09-28 to 2026-10-04), 10", r.Jobs, r.PreviousJobs, r.PreviousFrom, r.PreviousTo, r.Limit)
	}
	e.report("busiest", reportWeek+"&agency="+url.QueryEscape("Example Fire Service"), &r)
	if want := "1 Station 2 3 75% was 0; 2 Station 1 2 50% was 1; 2 Station 3 2 50% was 0"; busiestRows(r) != want || r.Jobs != 4 {
		t.Errorf("one agency: %s (%d jobs)\nwant %s (4 jobs)", busiestRows(r), r.Jobs, want)
	}
	e.report("busiest", reportWeek+"&priority=Emergency&event_type=HSF1", &r)
	if want := "1 Station 1 2 100% was 1; 1 Station 2 2 100% was 0; 3 Station 3 1 50% was 0"; busiestRows(r) != want || r.Jobs != 2 {
		t.Errorf("Emergency HSF1: %s (%d jobs)\nwant %s (2 jobs)", busiestRows(r), r.Jobs, want)
	}
	// The filters' values: every agency, and the event types jobs have.
	var o server.ReportOptions
	e.report("options", "", &o)
	var types []string
	for _, t := range o.EventTypes {
		types = append(types, deref(t.Code)+" "+t.Label)
	}
	if strings.Join(o.Agencies, ", ") != "Example Fire Service, Example Rescue Service" || strings.Join(types, ", ") != "GRS3 Grass fire, HSF1 House fire, MED1 Medical assistance" {
		t.Errorf("options: agencies %v, event types %v", o.Agencies, types)
	}
	// An empty range: no rows, no jobs.
	e.report("busiest", "from=2026-01-01&to=2026-01-31", &r)
	if len(r.Rows) != 0 || r.Jobs != 0 {
		t.Errorf("January: %s, %d jobs; want none", busiestRows(r), r.Jobs)
	}
	for _, q := range []string{"", "from=2026-10-05", "from=2026-10-11&to=2026-10-05", "from=5 Oct&to=2026-10-11", reportWeek + "&priority=Urgent", reportWeek + "&limit=7"} {
		if code := e.getJSON("/api/v1/reports/busiest?"+q, nil); code != http.StatusBadRequest {
			t.Errorf("busiest?%s: %d, want 400", q, code)
		}
	}
	// Signed in only (A29).
	e.as = nil
	if code := e.getJSON("/api/v1/reports/busiest?"+reportWeek, nil); code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", code)
	}
}

// A92. Busiest localities (R38c).
func TestA92BusiestLocalities(t *testing.T) {
	e := setup(t)
	reportFixture(t, e)
	var r server.LocalitiesReport
	e.report("localities", reportWeek, &r)
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
	if want := "MADEUPTON 2 33% on the map; SAMPLEBURY 1 17% not on the map; TESTVILLE 1 17% on the map; none 2 33% not on the map"; strings.Join(got, "; ") != want || r.Jobs != 6 {
		t.Errorf("localities: %s (%d jobs)\nwant %s (6 jobs)", strings.Join(got, "; "), r.Jobs, want)
	}
	e.report("localities", reportWeek+"&agency="+url.QueryEscape("Example Rescue Service"), &r)
	if len(r.Rows) != 1 || r.Rows[0].Locality == nil || *r.Rows[0].Locality != "SAMPLEBURY" || r.Jobs != 1 {
		t.Errorf("Example Rescue Service: %+v, want SAMPLEBURY 1", r.Rows)
	}
}

func cells(r server.BusyTimesReport) string {
	days := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	var out []string
	for d := range 7 {
		for h := range 24 {
			if n := r.Cells[d][h]; n > 0 {
				out = append(out, fmt.Sprintf("%s %02d:00 %d", days[d], h, n))
			}
		}
	}
	return strings.Join(out, ", ")
}

// A93. When it's busy (R38d).
func TestA93WhenBusy(t *testing.T) {
	e := setup(t)
	reportFixture(t, e)
	var r server.BusyTimesReport
	e.report("busy-times", reportWeek, &r)
	if want := "Mon 14:00 1, Tue 09:00 1, Wed 19:00 1, Sat 14:00 2, Sun 03:00 1"; cells(r) != want || r.Jobs != 6 {
		t.Errorf("busy times: %s (%d)\nwant %s (6)", cells(r), r.Jobs, want)
	}
	e.report("busy-times", reportWeek+"&alias="+url.QueryEscape("station 1"), &r)
	if want := "Mon 14:00 1, Sat 14:00 1"; cells(r) != want || r.Alias != "Station 1" {
		t.Errorf("Station 1: %s (%q)\nwant %s", cells(r), r.Alias, want)
	}
	e.report("busy-times", reportWeek+"&capcode=999999", &r)
	if want := "Sun 03:00 1"; cells(r) != want || r.Capcode != "0999999" {
		t.Errorf("0999999: %s, want %s", cells(r), want)
	}
	e.report("busy-times", reportWeek+"&event_type=HSF1", &r)
	if want := "Mon 14:00 1, Sat 14:00 1"; cells(r) != want {
		t.Errorf("HSF1: %s, want %s", cells(r), want)
	}
	if code := e.getJSON("/api/v1/reports/busy-times?"+reportWeek+"&alias=Nobody", nil); code != http.StatusNotFound {
		t.Errorf("an alias that isn't there: %d, want 404", code)
	}
}

// A94. Event type mix (R38e).
func TestA94EventTypeMix(t *testing.T) {
	e := setup(t)
	reportFixture(t, e)
	var r server.EventTypesReport
	e.report("event-types", "from=2026-09-01&to=2026-10-31&event_type=GRS3", &r)
	var got []string
	for _, ty := range r.Types {
		code := "none"
		if ty.Code != nil {
			code = *ty.Code + " " + ty.Label + " (" + ty.Category + ")"
		}
		got = append(got, fmt.Sprintf("%s %d %.0f%%", code, ty.Jobs, ty.Share*100))
	}
	if want := "HSF1 House fire (Structure fire) 3 43%; GRS3 Grass fire (Grass fire) 1 14%; MED1 Medical assistance (Medical) 1 14%; none 2 29%"; strings.Join(got, "; ") != want || r.Jobs != 7 || r.EventType != "" {
		t.Errorf("event types: %s (%d jobs, %q)\nwant %s (7 jobs, every event type)", strings.Join(got, "; "), r.Jobs, r.EventType, want)
	}
	var months []string
	for _, m := range r.Months {
		months = append(months, fmt.Sprintf("%s %d %v", m.Month, m.Jobs, m.Types))
	}
	if want := "2026-09 1 [1 0 0 0]; 2026-10 6 [2 1 1 2]"; strings.Join(months, "; ") != want {
		t.Errorf("months: %s\nwant %s", strings.Join(months, "; "), want)
	}
	e.report("event-types", reportWeek+"&priority=Admin", &r)
	if len(r.Types) != 1 || r.Types[0].Code != nil || r.Types[0].Jobs != 1 || r.Types[0].Share != 1 {
		t.Errorf("Admin: %+v, want the training page alone, no event type", r.Types)
	}
}

// A95. Job size (R38f).
func TestA95JobSize(t *testing.T) {
	e := setup(t)
	reportFixture(t, e)
	var r server.JobSizeReport
	e.report("job-size", reportWeek, &r)
	var got []string
	for _, row := range r.Rows {
		code := "all"
		if row.EventType != nil {
			code = "none"
			if row.EventType.Code != nil {
				code = *row.EventType.Code
			}
		}
		got = append(got, fmt.Sprintf("%s %d %v %v %v", code, row.Jobs, row.Pages, row.Capcodes, row.Appliances))
	}
	want := "all 6 {1 1 2 5} {1 1 2 3} {1 0 1 2}; HSF1 2 {2 2 5 5} {2 2 3 3} {1 1 2 2}; GRS3 1 {1 1 1 1} {1 1 1 1} {1 1 1 1}; " +
		"MED1 1 {1 1 1 1} {1 1 1 1} {1 1 1 1}; none 2 {1 1 1 1} {1 1 1 1} {0 0 0 0}"
	if strings.Join(got, "; ") != want {
		t.Errorf("job size: %s\nwant %s", strings.Join(got, "; "), want)
	}
}

// A96. Long-running jobs (R38g).
func TestA96LongJobs(t *testing.T) {
	e := setup(t)
	reportFixture(t, e)
	var r server.LongJobsReport
	e.report("long-jobs", reportWeek, &r)
	var got []string
	for _, j := range r.Rows {
		got = append(got, fmt.Sprintf("%s %s-%s %ds %d pages %s", j.JobNumber, j.First, j.Last, j.Seconds, j.Pages, deref(j.EventType.Code)))
	}
	if want := "EX0000901 2026-10-05T14:10:00Z-2026-10-05T16:40:00Z 9000s 5 pages HSF1; EX0000903 2026-10-10T14:30:00Z-2026-10-10T14:31:00Z 60s 2 pages HSF1"; strings.Join(got, "; ") != want || r.Limit != 20 {
		t.Errorf("long jobs: %s\nwant %s", strings.Join(got, "; "), want)
	}
	e.report("long-jobs", reportWeek+"&agency="+url.QueryEscape("Example Rescue Service"), &r)
	if len(r.Rows) != 0 {
		t.Errorf("Example Rescue Service: %+v, want none", r.Rows)
	}
}

// A97. Appliance activity (R38h).
func TestA97Appliances(t *testing.T) {
	e := setup(t)
	reportFixture(t, e)
	var r server.AppliancesReport
	e.report("appliances", reportWeek, &r)
	var got []string
	for _, a := range r.Rows {
		got = append(got, fmt.Sprintf("%s %d %.0f%%", a.Appliance, a.Jobs, a.Share*100))
	}
	if want := "P1 2 33%; T1 2 33%; R1 1 17%"; strings.Join(got, "; ") != want {
		t.Errorf("appliances: %s, want %s", strings.Join(got, "; "), want)
	}
}

// A98. Large events over time (R38i).
func TestA98LargeEventsOverTime(t *testing.T) {
	e := setup(t)
	at := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2025, month, day, hour, minute, 0, 0, time.UTC)
	}
	for _, p := range []struct {
		job, code string
		at        time.Time
	}{
		{"EX0000911", "DG0801", at(11, 3, 10, 0)},
		{"EX0000912", "DG0802", at(11, 3, 15, 0)},
		{"EX0000913", "DG0803", at(11, 5, 9, 0)},
		{"EX0000914", "DG0804", at(11, 19, 8, 0)},
		{"EX0000915", "DG0805", at(11, 19, 8, 30)},
		{"EX0000916", "DG0806", at(11, 19, 9, 0)},
		{"EX0000917", "DG0807", at(12, 3, 12, 0)},
	} {
		e.mustPost("a", "0100001", dgPage(p.job, p.code, "STN1"), p.at)
	}
	var r server.LargeEventsReport
	e.report("large-events", "from=2025-11-01&to=2025-12-31", &r)
	show := func(r server.LargeEventsReport) string {
		var out []string
		for _, b := range r.Buckets {
			if b.Events > 0 {
				out = append(out, fmt.Sprintf("%s %d %ds %d", b.Start, b.Events, *b.Seconds, *b.Jobs))
			}
		}
		return fmt.Sprintf("%d %ds %d: %s (%d buckets)", r.Events, *r.Seconds, *r.Jobs, strings.Join(out, ", "), len(r.Buckets))
	}
	if r.Seconds == nil || r.Jobs == nil {
		t.Fatalf("no medians: %+v", r)
	}
	if want := "4 0s 1: 2025-11-01 3 3600s 2, 2025-12-01 1 0s 1 (2 buckets)"; show(r) != want {
		t.Errorf("by month: %s\nwant %s", show(r), want)
	}
	e.report("large-events", "from=2025-11-01&to=2025-12-31&by=week", &r)
	if want := "4 0s 1: 2025-11-03 2 0s 1, 2025-11-17 1 3600s 3, 2025-12-01 1 0s 1 (10 buckets)"; show(r) != want {
		t.Errorf("by week: %s\nwant %s", show(r), want)
	}
	e.report("large-events", "from=2025-01-01&to=2025-01-31", &r)
	if r.Events != 0 || r.Seconds != nil || len(r.Buckets) != 1 {
		t.Errorf("January: %+v, want one empty month", r)
	}
}

// A99. Reports follow the pages (R38a): retention, a re-parse that marks an
// alias ignored, and a job's late page, each change the counts the next
// report shows; the report tables hold no page text, and a rebuild gives
// the same counts as keeping them in step.
func TestA99ReportsFollowPages(t *testing.T) {
	e := setup(t)
	reportFixture(t, e)
	var before server.BusiestReport
	e.report("busiest", reportWeek, &before)
	// No page text in the report tables (P1): only counts, keys and the
	// fields reports group by.
	for _, text := range reportPrivate {
		for _, hit := range e.rowsContaining(text) {
			if strings.HasPrefix(hit, "report_") {
				t.Errorf("a report table holds page text: %s", hit)
			}
		}
	}
	// A rebuild counts the same.
	if err := e.srv.Reports.Rebuild(e.ctx); err != nil {
		t.Fatal(err)
	}
	var rebuilt server.BusiestReport
	e.report("busiest", reportWeek, &rebuilt)
	if busiestRows(rebuilt) != busiestRows(before) || rebuilt.Building {
		t.Errorf("after a rebuild: %s (building %v)\nkept in step: %s", busiestRows(rebuilt), rebuilt.Building, busiestRows(before))
	}
	// A late page of EX0000902 to Station 3: Station 3 has 3 jobs.
	e.mustPost("a", "0100003", "@@EX0000902 GRS3 GRASS FIRE @ SAMPLE RD, TESTVILLE UNITS T1 #STN3", reportDay(6, 9, 20))
	var r server.BusiestReport
	e.report("busiest", reportWeek, &r)
	if got := busiestRows(r); !strings.HasPrefix(got, "1 Station 2 3 50% was 0; 1 Station 3 3 50% was 0; 3 Station 1 2") || r.Jobs != 6 {
		t.Errorf("after a late page: %s (%d jobs)", got, r.Jobs)
	}
	// Station 2 marked ignored: after the re-parse its pages count nowhere,
	// and EX0000902's job is Station 3's alone.
	if err := e.catSetIgnored("0100002", true); err != nil {
		t.Fatal(err)
	}
	e.reparseAll()
	e.report("busiest", reportWeek, &r)
	if want := "1 Station 3 3 50% was 0; 2 Station 1 2 33% was 1; 3 Rescue 1 1 17% was 0; 3 0999999 1 17% was 0"; busiestRows(r) != want {
		t.Errorf("Station 2 ignored: %s\nwant %s", busiestRows(r), want)
	}
	// Retention: the pages before 10 October go, and their jobs with them.
	if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM messages WHERE sent_at < $1`, reportDay(10, 0, 0)); err != nil {
		t.Fatal(err)
	}
	e.report("busiest", reportWeek, &r)
	if want := "1 Rescue 1 1 33% was 0; 1 Station 1 1 33% was 0; 1 0999999 1 33% was 0"; busiestRows(r) != want || r.Jobs != 3 {
		t.Errorf("after retention: %s (%d jobs)\nwant %s (3 jobs)", busiestRows(r), r.Jobs, want)
	}
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM report_jobs WHERE first_at < $1`, reportDay(10, 0, 0)).Scan(&n); err != nil || n != 0 {
		t.Errorf("report_jobs before 10 October: %d (%v), want none", n, err)
	}
}

// R35a: a job's day and hour are the deployment's time zone's.
func TestReportsTimeZone(t *testing.T) {
	e := setup(t)
	melbourne, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		t.Fatal(err)
	}
	// 13:30 UTC on Sunday 4 October is 00:30 on Monday 5 October in
	// Melbourne (daylight saving, UTC+11).
	e.mustPost("a", "0100001", "@@EX0000950 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1", time.Date(2026, time.October, 4, 13, 30, 0, 0, time.UTC))
	e.srv.Reports = &reports.Service{Pool: e.st.Pool, Location: melbourne}
	var r server.BusyTimesReport
	e.report("busy-times", reportWeek, &r)
	if !r.Building {
		t.Error("built for UTC, a report in Melbourne's time doesn't say it's being built")
	}
	if err := e.srv.Reports.Rebuild(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.report("busy-times", reportWeek, &r)
	if want := "Mon 00:00 1"; cells(r) != want || r.Building {
		t.Errorf("in Melbourne: %s (building %v), want %s", cells(r), r.Building, want)
	}
}

// a99ReportsSpeed is R38a's speed on A75's pages (ADR 0031), run by
// TestA75SearchSpeed: every report, for the last 30 days, a year and the
// whole 10 years, with and without filters, answers within 1 second, warm,
// and for the whole 10 years also straight after a restart with both caches
// cold.
func a99ReportsSpeed(t *testing.T, e *env, container string) {
	t.Helper()
	day := func(ago int) string { return time.Now().UTC().AddDate(0, 0, -ago).Format(time.DateOnly) }
	ranges := []struct {
		name, query string
		cold        bool
	}{
		{"30 days", "from=" + day(29) + "&to=" + day(0), false},
		{"a year", "from=" + day(364) + "&to=" + day(0), false},
		{"10 years", "from=" + day(3652) + "&to=" + day(0), true},
		{"10 years, filtered", "from=" + day(3652) + "&to=" + day(0) + "&agency=" + url.QueryEscape("Example Fire Service") + "&event_type=HSF1&priority=Emergency", false},
	}
	reports := []struct{ name, extra string }{
		{"busiest", ""}, {"localities", ""}, {"busy-times", ""}, {"busy-times", "&alias=" + url.QueryEscape("Station 5")},
		{"event-types", ""}, {"job-size", ""}, {"long-jobs", ""}, {"appliances", ""}, {"large-events", "&by=week"},
	}
	run := func(path string) (time.Duration, int) {
		t0 := time.Now()
		resp, err := e.as.do(http.MethodGet, e.url+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
		return time.Since(t0), len(body)
	}
	for _, rep := range reports {
		for _, rg := range ranges {
			path := "/api/v1/reports/" + rep.name + "?" + rg.query + rep.extra
			name := rep.name + rep.extra + ", " + rg.name
			cold := ""
			if rg.cold {
				a75ColdStart(t, e, container)
				d, _ := run(path)
				cold = "cold " + d.Round(time.Millisecond).String() + ", "
				if d > time.Second {
					t.Errorf("%s: %s straight after a cold start, over 1 second (R38a)", name, d.Round(time.Millisecond))
				}
			}
			var warm []string
			var slowest time.Duration
			var size int
			for range 3 {
				var d time.Duration
				d, size = run(path)
				slowest = max(slowest, d)
				warm = append(warm, d.Round(time.Millisecond).String())
			}
			t.Logf("reports, %-48s %s warm %s (%d bytes)", name, cold, strings.Join(warm, " "), size)
			if slowest > time.Second {
				t.Errorf("%s: %s warm, over 1 second (R38a)", name, slowest.Round(time.Millisecond))
			}
		}
	}
	var jobs, whos, days int64
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM report_jobs), (SELECT count(*) FROM report_job_whos), (SELECT count(*) FROM report_days)`).Scan(&jobs, &whos, &days); err != nil {
		t.Fatal(err)
	}
	var size string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT pg_size_pretty(pg_total_relation_size('report_jobs') + pg_total_relation_size('report_job_whos') + pg_total_relation_size('report_days'))`).Scan(&size); err != nil {
		t.Fatal(err)
	}
	t.Logf("reports: %d jobs, %d job-alias rows, %d day rows, %s with their indexes", jobs, whos, days, size)
}
