package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// A120 (R38a, R38d, R38h): the jobs behind a report's number, on SPEC.md's
// report fixture. Each list holds as many jobs as the number it's behind.

// listed is a list's jobs, newest first, as "job first pages event-type
// locality".
func listed(r server.JobsReport) string {
	var out []string
	for _, j := range r.Rows {
		loc := "-"
		if j.Locality != nil {
			loc = *j.Locality
		}
		out = append(out, fmt.Sprintf("%s %s %d %s %s", j.JobNumber, j.First, j.Pages, deref(j.EventType.Code), loc))
	}
	return strings.Join(out, "; ")
}

// numbers is a list's job numbers.
func numbers(r server.JobsReport) string {
	var out []string
	for _, j := range r.Rows {
		out = append(out, j.JobNumber)
	}
	return strings.Join(out, ", ")
}

// A120. The jobs behind a number.
func TestA120JobsBehindANumber(t *testing.T) {
	e := setup(t)
	reportFixture(t, e)
	everyListed := map[string]bool{}
	// Every cell of A93's grids, for each of A93's filters: its list holds
	// as many jobs as it says, each once.
	for _, filter := range []string{"", "&alias=" + url.QueryEscape("station 1"), "&capcode=999999", "&event_type=HSF1"} {
		var grid server.BusyTimesReport
		e.report("busy-times", reportWeek+filter, &grid)
		var all int64
		for d := range 7 {
			for h := range 24 {
				var list server.JobsReport
				e.report("jobs", fmt.Sprintf("%s%s&weekday=%d&hour=%d", reportWeek, filter, d+1, h), &list)
				seen := map[string]bool{}
				for _, j := range list.Rows {
					if seen[j.JobNumber] {
						t.Errorf("%q weekday %d hour %d lists %s twice", filter, d+1, h, j.JobNumber)
					}
					seen[j.JobNumber], everyListed[j.JobNumber] = true, true
				}
				if list.Jobs != grid.Cells[d][h] || int64(len(list.Rows)) != grid.Cells[d][h] {
					t.Errorf("%q weekday %d hour %d: the cell says %d, the list %d jobs (%d rows)", filter, d+1, h, grid.Cells[d][h], list.Jobs, len(list.Rows))
				}
				all += list.Jobs
			}
		}
		if all != grid.Jobs {
			t.Errorf("%q: the cells' lists hold %d jobs, the grid %d", filter, all, grid.Jobs)
		}
	}

	var r server.JobsReport
	e.report("jobs", reportWeek+"&weekday=6&hour=14", &r)
	if want := "EX0000904 2026-10-10T14:50:00Z 1 MED1 SAMPLEBURY; EX0000903 2026-10-10T14:30:00Z 2 HSF1 MADEUPTON"; listed(r) != want || r.Jobs != 2 || r.Page != 1 || r.PageSize != 50 {
		t.Errorf("Saturday 14:00: %s (%d jobs, page %d of %d)\nwant %s (2 jobs, page 1 of 50)", listed(r), r.Jobs, r.Page, r.PageSize, want)
	}
	if r.Weekday == nil || *r.Weekday != 6 || r.Hour == nil || *r.Hour != 14 || r.From != "2026-10-05" || r.To != "2026-10-11" {
		t.Errorf("Saturday 14:00 says weekday %v, hour %v, %s to %s", r.Weekday, r.Hour, r.From, r.To)
	}
	e.report("jobs", reportWeek+"&weekday=1&hour=14", &r)
	if want := "EX0000901 2026-10-05T14:10:00Z 4 HSF1 MADEUPTON"; listed(r) != want {
		t.Errorf("Monday 14:00: %s\nwant %s", listed(r), want)
	}
	for _, c := range []struct{ query, want string }{
		{"&alias=" + url.QueryEscape("station 1") + "&weekday=6&hour=14", "EX0000903"},
		{"&event_type=HSF1&weekday=6&hour=14", "EX0000903"},
		{"&capcode=999999&weekday=7&hour=3", "EX0000905"},
		{"&hour=0", ""},
	} {
		if e.report("jobs", reportWeek+c.query, &r); numbers(r) != c.want {
			t.Errorf("%s: %q, want %q", c.query, numbers(r), c.want)
		}
	}
	e.report("jobs", reportWeek+"&alias="+url.QueryEscape("station 1")+"&weekday=6&hour=14", &r)
	if r.Alias != "Station 1" {
		t.Errorf("alias %q, want Station 1", r.Alias)
	}

	// A97's appliances: each list is as many jobs as the report says.
	var a server.AppliancesReport
	e.report("appliances", reportWeek, &a)
	want := map[string]string{"P1": "EX0000903, EX0000901", "T1": "EX0000902, EX0000901", "R1": "EX0000904"}
	if len(a.Rows) != len(want) {
		t.Errorf("appliances: %+v", a.Rows)
	}
	for _, row := range a.Rows {
		e.report("jobs", reportWeek+"&appliance="+url.QueryEscape(row.Appliance), &r)
		if r.Jobs != row.Jobs || int64(len(r.Rows)) != row.Jobs || numbers(r) != want[row.Appliance] || r.Appliance != row.Appliance {
			t.Errorf("%s: the report says %d, the list %d (%s), want %s", row.Appliance, row.Jobs, r.Jobs, numbers(r), want[row.Appliance])
		}
		for _, j := range r.Rows {
			everyListed[j.JobNumber] = true
		}
	}
	// The ignored alias's page's appliance, and its job, are in no list.
	if e.report("jobs", reportWeek+"&appliance=X9", &r); r.Jobs != 0 || len(r.Rows) != 0 {
		t.Errorf("X9: %s, want none", numbers(r))
	}
	if e.report("jobs", reportWeek, &r); r.Jobs != 5 {
		t.Errorf("the week: %d jobs (%s), want 5", r.Jobs, numbers(r))
	}
	for _, j := range r.Rows {
		everyListed[j.JobNumber] = true
	}
	if everyListed["EX0000906"] {
		t.Error("the ignored alias's job EX0000906 is listed")
	}
	rescue := reportWeek + "&agency=" + url.QueryEscape("Example Rescue Service")
	if e.report("jobs", rescue+"&appliance=P1", &r); r.Jobs != 0 || r.Agency != "Example Rescue Service" {
		t.Errorf("Example Rescue Service's P1: %s (%q), want none", numbers(r), r.Agency)
	}
	if e.report("jobs", rescue+"&appliance=R1", &r); numbers(r) != "EX0000904" {
		t.Errorf("Example Rescue Service's R1: %s, want EX0000904", numbers(r))
	}

	// Refused.
	for _, q := range []string{"weekday=0", "weekday=8", "weekday=sat", "hour=24", "hour=-1", "page=0", "page=two", "capcode=12345678"} {
		if code := e.getJSON("/api/v1/reports/jobs?"+reportWeek+"&"+q, nil); code != http.StatusBadRequest {
			t.Errorf("jobs?%s: %d, want 400", q, code)
		}
	}
	if code := e.getJSON("/api/v1/reports/jobs?from=2026-10-11&to=2026-10-05", nil); code != http.StatusBadRequest {
		t.Errorf("a reversed range: %d, want 400", code)
	}
	if code := e.getJSON("/api/v1/reports/jobs?"+reportWeek+"&alias=Nobody", nil); code != http.StatusNotFound {
		t.Errorf("an alias that isn't there: %d, want 404", code)
	}
	e.as = nil
	if code := e.getJSON("/api/v1/reports/jobs?"+reportWeek, nil); code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", code)
	}
}

// A120: 51 jobs in one hour are listed 50 to a page, newest first.
func TestA120JobsPaged(t *testing.T) {
	e := setup(t)
	for i := range 51 {
		at := time.Date(2026, time.October, 1, 8, i, 0, 0, time.UTC) // a Thursday
		e.mustPost("a", "0100001", fmt.Sprintf("@@EX%07d HSF1 SMOKE SEEN @ 1 FICTION ST, MADEUPTON UNITS P1 #STN1", 1000+i), at)
	}
	var grid server.BusyTimesReport
	e.report("busy-times", "from=2026-10-01&to=2026-10-01", &grid)
	if grid.Cells[3][8] != 51 {
		t.Fatalf("Thursday 08:00: %d jobs, want 51", grid.Cells[3][8])
	}
	var r server.JobsReport
	e.report("jobs", "from=2026-10-01&to=2026-10-01&weekday=4&hour=8", &r)
	if r.Jobs != 51 || len(r.Rows) != 50 || r.Rows[0].JobNumber != "EX0001050" || r.Rows[49].JobNumber != "EX0001001" {
		t.Errorf("page 1: %d jobs, %d rows (%s)", r.Jobs, len(r.Rows), numbers(r))
	}
	e.report("jobs", "from=2026-10-01&to=2026-10-01&weekday=4&hour=8&page=2", &r)
	if r.Jobs != 51 || numbers(r) != "EX0001000" || r.Page != 2 {
		t.Errorf("page 2: %d jobs, %s (page %d), want EX0001000", r.Jobs, numbers(r), r.Page)
	}
	e.report("jobs", "from=2026-10-01&to=2026-10-01&weekday=4&hour=8&page=3", &r)
	if r.Jobs != 51 || len(r.Rows) != 0 {
		t.Errorf("page 3: %s, want none", numbers(r))
	}
}
