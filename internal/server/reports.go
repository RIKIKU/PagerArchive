package server

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/reports"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// M24's reports (R38a-R38i, ADR 0031): counts of jobs, never page text. Each
// counts what the report tables hold (internal/reports), after counting the
// jobs whose pages changed since, for up to reportCatchUp.
const reportCatchUp = 300 * time.Millisecond

// ReportBase is what every job report says about itself.
type ReportBase struct {
	From      string `json:"from"` // days, inclusive, deployment time zone
	To        string `json:"to"`
	Agency    string `json:"agency,omitempty"`
	EventType string `json:"event_type,omitempty"`
	Priority  string `json:"priority,omitempty"`
	Jobs      int64  `json:"jobs"` // the jobs the filters hold
	// Pending is how many changed jobs aren't counted yet; Building, that
	// the counts are being made for the first time (or for a new time zone).
	Pending  int64 `json:"pending"`
	Building bool  `json:"building"`
}

// EventType is an event type as reports show it: its code (P3), its label
// from an agency profile (R9) and its category (R5). A null code is a job
// with no event type.
type EventType struct {
	Code     *string `json:"code"`
	Label    string  `json:"label,omitempty"`
	Category string  `json:"category,omitempty"`
}

var reportPriorities = []string{"Emergency", "Non-emergency", "Admin"}

// reportFilter reads a report's filters: from and to (required), agency,
// event_type and priority.
func (s *Server) reportFilter(w http.ResponseWriter, r *http.Request) (reports.Filter, ReportBase, bool) {
	q := r.URL.Query()
	var f reports.Filter
	base := ReportBase{From: q.Get("from"), To: q.Get("to")}
	var err error
	if base.From == "" || base.To == "" {
		problem(w, http.StatusBadRequest, "from and to are required (YYYY-MM-DD)")
		return f, base, false
	}
	if f.From, err = reports.ParseDay(base.From); err == nil {
		f.To, err = reports.ParseDay(base.To)
	}
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return f, base, false
	}
	if f.To.Before(f.From) {
		problem(w, http.StatusBadRequest, "from must not be after to")
		return f, base, false
	}
	if p := q.Get("priority"); p != "" {
		if !slices.Contains(reportPriorities, p) {
			problem(w, http.StatusBadRequest, "priority must be Emergency, Non-emergency or Admin")
			return f, base, false
		}
		f.Priority, base.Priority = p, p
	}
	f.EventType = strings.TrimSpace(q.Get("event_type"))
	base.EventType = f.EventType
	if a := strings.TrimSpace(q.Get("agency")); a != "" {
		ids, err := s.Reports.Templates(r.Context(), a)
		if err != nil {
			s.reportFailed(w, err)
			return f, base, false
		}
		f.Templates, base.Agency = append([]int64{}, ids...), a
	}
	return f, base, true
}

// reportReady counts changed jobs (for up to reportCatchUp), then says how
// many are left and whether the counts are still being built.
func (s *Server) reportReady(r *http.Request, base *ReportBase) error {
	if err := s.Reports.CatchUp(r.Context(), reportCatchUp); err != nil && !errors.Is(err, reports.ErrBusy) {
		return err
	}
	built, err := s.Reports.Built(r.Context())
	if err != nil {
		return err
	}
	base.Building = !built
	base.Pending, err = s.Reports.Pending(r.Context())
	return err
}

func (s *Server) reportFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, errReportNotFound) {
		problem(w, http.StatusNotFound, "there's no alias with that name")
		return
	}
	s.Log.Error("report", "error", store.SafeError(err))
	problem(w, http.StatusInternalServerError, "report failed")
}

var errReportNotFound = errors.New("no such alias")

func (s *Server) eventType(code string) EventType {
	if code == "" {
		return EventType{}
	}
	et := EventType{Code: &code, Category: pipeline.Category(s.Ingest.Network, code)}
	names := make([]string, 0, len(s.Ingest.Agencies))
	for n := range s.Ingest.Agencies {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if l := s.Ingest.Agencies[n].Labels["event_type"][code]; l != "" {
			et.Label = l
			break
		}
	}
	return et
}

func share(n, of int64) float64 {
	if of == 0 {
		return 0
	}
	return float64(n) / float64(of)
}

// ReportOptions are the filters' values (R38a).
type ReportOptions struct {
	Agencies   []string    `json:"agencies"`
	EventTypes []EventType `json:"event_types"`
	Priorities []string    `json:"priorities"`
}

func (s *Server) reportOptions(w http.ResponseWriter, r *http.Request) {
	// The event types of the jobs counted so far, after those just changed.
	if err := s.Reports.CatchUp(r.Context(), reportCatchUp); err != nil && !errors.Is(err, reports.ErrBusy) {
		s.reportFailed(w, err)
		return
	}
	o, err := s.Reports.FilterOptions(r.Context())
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	out := ReportOptions{Agencies: o.Agencies, EventTypes: []EventType{}, Priorities: reportPriorities}
	if out.Agencies == nil {
		out.Agencies = []string{}
	}
	for _, c := range o.EventTypes {
		out.EventTypes = append(out.EventTypes, s.eventType(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// BusiestRow is one alias, or a capcode with no alias, and its jobs (R38b).
type BusiestRow struct {
	Rank         int     `json:"rank"`
	AliasID      *int64  `json:"alias_id"`
	Alias        string  `json:"alias,omitempty"`
	Pattern      string  `json:"pattern,omitempty"`
	Capcode      string  `json:"capcode,omitempty"`
	Agency       string  `json:"agency,omitempty"`
	Jobs         int64   `json:"jobs"`
	Share        float64 `json:"share"`
	PreviousJobs int64   `json:"previous_jobs"`
}

type BusiestReport struct {
	ReportBase
	Limit        int          `json:"limit"`
	PreviousFrom string       `json:"previous_from"`
	PreviousTo   string       `json:"previous_to"`
	PreviousJobs int64        `json:"previous_jobs"`
	Rows         []BusiestRow `json:"rows"`
}

var busiestLimits = []int{10, 25, 50}

func reportLimit(r *http.Request, allowed []int) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return allowed[0], true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && slices.Contains(allowed, n)
}

func (s *Server) busiestReport(w http.ResponseWriter, r *http.Request) {
	f, base, ok := s.reportFilter(w, r)
	if !ok {
		return
	}
	limit, ok := reportLimit(r, busiestLimits)
	if !ok {
		problem(w, http.StatusBadRequest, "limit must be 10, 25 or 50")
		return
	}
	ctx := r.Context()
	if err := s.reportReady(r, &base); err != nil {
		s.reportFailed(w, err)
		return
	}
	prev := f.Previous()
	out := BusiestReport{ReportBase: base, Limit: limit, PreviousFrom: prev.From.Format(time.DateOnly), PreviousTo: prev.To.Format(time.DateOnly), Rows: []BusiestRow{}}
	whos, err := s.Reports.Whos(ctx, f, nil)
	if err == nil {
		out.Jobs, err = s.Reports.Total(ctx, f)
	}
	if err == nil {
		out.PreviousJobs, err = s.Reports.Total(ctx, prev)
	}
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	// The aliases' names, for the order: most jobs, then by name (a capcode
	// with no alias by its capcode, after the names).
	var ids []int64
	for _, c := range whos {
		if id, err := strconv.ParseInt(strings.TrimPrefix(c.Key, "a"), 10, 64); err == nil && strings.HasPrefix(c.Key, "a") {
			ids = append(ids, id)
		}
	}
	aliases, err := s.Reports.Aliases(ctx, ids)
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	rows := make([]BusiestRow, 0, len(whos))
	for _, c := range whos {
		row := BusiestRow{Jobs: c.Jobs, Share: share(c.Jobs, out.Jobs)}
		if strings.HasPrefix(c.Key, "a") {
			id, _ := strconv.ParseInt(c.Key[1:], 10, 64)
			row.AliasID = &id
			if a, ok := aliases[id]; ok {
				row.Alias, row.Pattern, row.Agency = a.Name, a.Pattern, a.Agency
			} else {
				row.Alias = "Deleted alias"
			}
		} else {
			row.Capcode = c.Key[1:]
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Jobs != b.Jobs {
			return a.Jobs > b.Jobs
		}
		if (a.AliasID == nil) != (b.AliasID == nil) {
			return a.AliasID != nil
		}
		if a.Alias != b.Alias {
			return strings.ToLower(a.Alias) < strings.ToLower(b.Alias)
		}
		return a.Pattern+a.Capcode < b.Pattern+b.Capcode
	})
	for i := range rows {
		rows[i].Rank = i + 1
		if i > 0 && rows[i].Jobs == rows[i-1].Jobs {
			rows[i].Rank = rows[i-1].Rank
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	keys := make([]string, len(rows))
	for i, row := range rows {
		if row.AliasID != nil {
			keys[i] = "a" + strconv.FormatInt(*row.AliasID, 10)
		} else {
			keys[i] = "c" + row.Capcode
		}
	}
	before, err := s.Reports.Whos(ctx, prev, keys)
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	was := map[string]int64{}
	for _, c := range before {
		was[c.Key] = c.Jobs
	}
	for i := range rows {
		rows[i].PreviousJobs = was[keys[i]]
	}
	out.Rows = rows
	writeJSON(w, http.StatusOK, out)
}

// LocalityRow is one locality's jobs (R38c), and where the map draws it
// when the address dataset has it.
type LocalityRow struct {
	Locality *string  `json:"locality"` // null: jobs with no locality
	Jobs     int64    `json:"jobs"`
	Share    float64  `json:"share"`
	Lon      *float64 `json:"lon,omitempty"`
	Lat      *float64 `json:"lat,omitempty"`
}

type LocalitiesReport struct {
	ReportBase
	Rows []LocalityRow `json:"rows"`
}

func (s *Server) localitiesReport(w http.ResponseWriter, r *http.Request) {
	f, base, ok := s.reportFilter(w, r)
	if !ok {
		return
	}
	if err := s.reportReady(r, &base); err != nil {
		s.reportFailed(w, err)
		return
	}
	ctx := r.Context()
	counts, err := s.Reports.Localities(ctx, f)
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	names := []string{}
	for _, c := range counts {
		base.Jobs += c.Jobs
		if c.Key != "" {
			names = append(names, c.Key)
		}
	}
	points, err := s.Reports.LocalityPoints(ctx, names)
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	out := LocalitiesReport{ReportBase: base, Rows: []LocalityRow{}}
	for _, c := range counts {
		row := LocalityRow{Jobs: c.Jobs, Share: share(c.Jobs, base.Jobs)}
		if c.Key != "" {
			name := c.Key
			row.Locality = &name
			if p, ok := points[strings.ToUpper(c.Key)]; ok {
				row.Lon, row.Lat = &p.Lon, &p.Lat
			}
		}
		out.Rows = append(out.Rows, row)
	}
	// Jobs with no locality last.
	sort.SliceStable(out.Rows, func(i, j int) bool { return out.Rows[i].Locality != nil && out.Rows[j].Locality == nil })
	writeJSON(w, http.StatusOK, out)
}

// BusyTimesReport is R38d: jobs by the hour they started, Monday to Sunday.
type BusyTimesReport struct {
	ReportBase
	Alias   string        `json:"alias,omitempty"`
	Capcode string        `json:"capcode,omitempty"`
	Aliases []ReportAlias `json:"aliases,omitempty"`
	// Cells[d][h]: jobs that started on weekday d (0 Monday) in hour h.
	Cells [7][24]int64 `json:"cells"`
}

var capcodeDigits = regexp.MustCompile(`^[0-9]{1,7}$`)

func (s *Server) busyTimesReport(w http.ResponseWriter, r *http.Request) {
	f, base, ok := s.reportFilter(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	out := BusyTimesReport{}
	var whos []string
	q := r.URL.Query()
	if a := strings.TrimSpace(q.Get("alias")); a != "" {
		named, err := s.Store.AliasesNamed(ctx, a)
		if err != nil {
			s.reportFailed(w, err)
			return
		}
		if len(named) == 0 {
			problem(w, http.StatusNotFound, "there's no alias with that name")
			return
		}
		out.Alias = named[0].Name
		for _, n := range named {
			whos = append(whos, "a"+strconv.FormatInt(n.ID, 10))
			out.Aliases = append(out.Aliases, ReportAlias{ID: n.ID, Pattern: n.Pattern})
		}
	} else if c := strings.TrimSpace(q.Get("capcode")); c != "" {
		if !capcodeDigits.MatchString(c) {
			problem(w, http.StatusBadRequest, "capcode must be 1 to 7 digits")
			return
		}
		out.Capcode = decoder.PadCapcode(c)
		whos = []string{"c" + out.Capcode}
		// A capcode with its own alias is counted as that alias.
		var id int64
		if err := s.Store.Pool.QueryRow(ctx, `SELECT id FROM aliases WHERE pattern_key = $1`, out.Capcode).Scan(&id); err == nil {
			whos = append(whos, "a"+strconv.FormatInt(id, 10))
		}
	}
	if err := s.reportReady(r, &base); err != nil {
		s.reportFailed(w, err)
		return
	}
	cells, err := s.Reports.Hours(ctx, f, whos)
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	if len(whos) > 0 {
		base.Agency = "" // an alias's agency is its own
	}
	for _, c := range cells {
		if c.Weekday >= 1 && c.Weekday <= 7 && c.Hour >= 0 && c.Hour < 24 {
			out.Cells[c.Weekday-1][c.Hour] += c.Jobs
			base.Jobs += c.Jobs
		}
	}
	out.ReportBase = base
	writeJSON(w, http.StatusOK, out)
}

// EventTypeShare is one event type's jobs over the range (R38e).
type EventTypeShare struct {
	EventType
	Jobs  int64   `json:"jobs"`
	Share float64 `json:"share"`
}

// EventTypeMonth is one calendar month's jobs, and each event type's, in
// the order of Types.
type EventTypeMonth struct {
	Month string  `json:"month"` // YYYY-MM
	Jobs  int64   `json:"jobs"`
	Types []int64 `json:"types"`
}

type EventTypesReport struct {
	ReportBase
	Types  []EventTypeShare `json:"types"`
	Months []EventTypeMonth `json:"months"`
}

func (s *Server) eventTypesReport(w http.ResponseWriter, r *http.Request) {
	f, base, ok := s.reportFilter(w, r)
	if !ok {
		return
	}
	base.EventType = "" // every event type is counted
	if err := s.reportReady(r, &base); err != nil {
		s.reportFailed(w, err)
		return
	}
	counts, err := s.Reports.EventTypeMonths(r.Context(), f)
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	totals := map[string]int64{}
	for _, c := range counts {
		totals[c.EventType] += c.Jobs
		base.Jobs += c.Jobs
	}
	codes := make([]string, 0, len(totals))
	for c := range totals {
		codes = append(codes, c)
	}
	// Most jobs first, then by code; no event type last.
	sort.Slice(codes, func(i, j int) bool {
		a, b := codes[i], codes[j]
		if (a == "") != (b == "") {
			return b == ""
		}
		if totals[a] != totals[b] {
			return totals[a] > totals[b]
		}
		return a < b
	})
	out := EventTypesReport{ReportBase: base, Types: []EventTypeShare{}, Months: []EventTypeMonth{}}
	index := map[string]int{}
	for i, c := range codes {
		index[c] = i
		out.Types = append(out.Types, EventTypeShare{EventType: s.eventType(c), Jobs: totals[c], Share: share(totals[c], base.Jobs)})
	}
	// Every month the range touches, empty ones too.
	byMonth := map[string]*EventTypeMonth{}
	for m := time.Date(f.From.Year(), f.From.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(f.To); m = m.AddDate(0, 1, 0) {
		out.Months = append(out.Months, EventTypeMonth{Month: m.Format("2006-01"), Types: make([]int64, len(codes))})
	}
	for i := range out.Months {
		byMonth[out.Months[i].Month] = &out.Months[i]
	}
	for _, c := range counts {
		if m := byMonth[c.Month.Format("2006-01")]; m != nil {
			m.Jobs += c.Jobs
			m.Types[index[c.EventType]] += c.Jobs
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// SizeStat is the typical size and spread of jobs (R38f): the median, the
// middle half (25th to 75th percentile) and the largest, each by nearest
// rank.
type SizeStat struct {
	Median int64 `json:"median"`
	Low    int64 `json:"low"`
	High   int64 `json:"high"`
	Max    int64 `json:"max"`
}

type JobSizeRow struct {
	EventType  *EventType `json:"event_type"` // null: every event type together
	Jobs       int64      `json:"jobs"`
	Pages      SizeStat   `json:"pages"`
	Capcodes   SizeStat   `json:"capcodes"`
	Appliances SizeStat   `json:"appliances"`
}

type JobSizeReport struct {
	ReportBase
	Rows []JobSizeRow `json:"rows"`
}

// percentile is the p-th percentile of a histogram (size: jobs), by nearest
// rank: the smallest size with at least p% of the jobs at or below it.
func percentile(h map[int64]int64, p float64) int64 {
	var n int64
	sizes := make([]int64, 0, len(h))
	for size, jobs := range h {
		n += jobs
		sizes = append(sizes, size)
	}
	if n == 0 {
		return 0
	}
	slices.Sort(sizes)
	rank := int64(p / 100 * float64(n))
	if float64(rank) < p/100*float64(n) {
		rank++
	}
	rank = max(rank, 1)
	var seen int64
	for _, size := range sizes {
		if seen += h[size]; seen >= rank {
			return size
		}
	}
	return sizes[len(sizes)-1]
}

func stat(h map[int64]int64) SizeStat {
	return SizeStat{Median: percentile(h, 50), Low: percentile(h, 25), High: percentile(h, 75), Max: percentile(h, 100)}
}

func (s *Server) jobSizeReport(w http.ResponseWriter, r *http.Request) {
	f, base, ok := s.reportFilter(w, r)
	if !ok {
		return
	}
	if err := s.reportReady(r, &base); err != nil {
		s.reportFailed(w, err)
		return
	}
	sizes, err := s.Reports.Sizes(r.Context(), f)
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	type hists struct{ p, c, n map[int64]int64 }
	per := map[string]*hists{}
	all := &hists{map[int64]int64{}, map[int64]int64{}, map[int64]int64{}}
	for _, sc := range sizes {
		h := per[sc.EventType]
		if h == nil {
			h = &hists{map[int64]int64{}, map[int64]int64{}, map[int64]int64{}}
			per[sc.EventType] = h
		}
		for _, x := range []*hists{h, all} {
			switch sc.Kind {
			case "p":
				x.p[sc.Size] += sc.Jobs
			case "c":
				x.c[sc.Size] += sc.Jobs
			case "n":
				x.n[sc.Size] += sc.Jobs
			}
		}
	}
	jobs := func(h *hists) (n int64) {
		for _, v := range h.p {
			n += v
		}
		return n
	}
	base.Jobs = jobs(all)
	out := JobSizeReport{ReportBase: base, Rows: []JobSizeRow{}}
	if base.Jobs > 0 {
		out.Rows = append(out.Rows, JobSizeRow{Jobs: base.Jobs, Pages: stat(all.p), Capcodes: stat(all.c), Appliances: stat(all.n)})
	}
	codes := make([]string, 0, len(per))
	for c := range per {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool {
		a, b := codes[i], codes[j]
		if (a == "") != (b == "") {
			return b == ""
		}
		if ja, jb := jobs(per[a]), jobs(per[b]); ja != jb {
			return ja > jb
		}
		return a < b
	})
	for _, c := range codes {
		h := per[c]
		et := s.eventType(c)
		out.Rows = append(out.Rows, JobSizeRow{EventType: &et, Jobs: jobs(h), Pages: stat(h.p), Capcodes: stat(h.c), Appliances: stat(h.n)})
	}
	writeJSON(w, http.StatusOK, out)
}

// LongJob is one long-running job (R38g).
type LongJob struct {
	JobNumber string    `json:"job_number"`
	First     string    `json:"first"` // RFC 3339
	Last      string    `json:"last"`
	Seconds   int64     `json:"seconds"`
	Pages     int64     `json:"pages"`
	EventType EventType `json:"event_type"`
	Locality  *string   `json:"locality"`
}

type LongJobsReport struct {
	ReportBase
	Limit int       `json:"limit"`
	Rows  []LongJob `json:"rows"`
}

var longJobLimits = []int{20, 50, 100}

func (s *Server) longJobsReport(w http.ResponseWriter, r *http.Request) {
	f, base, ok := s.reportFilter(w, r)
	if !ok {
		return
	}
	limit, ok := reportLimit(r, longJobLimits)
	if !ok {
		problem(w, http.StatusBadRequest, "limit must be 20, 50 or 100")
		return
	}
	if err := s.reportReady(r, &base); err != nil {
		s.reportFailed(w, err)
		return
	}
	ctx := r.Context()
	jobs, err := s.Reports.LongJobs(ctx, f, limit)
	if err == nil {
		base.Jobs, err = s.Reports.Total(ctx, f)
	}
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	out := LongJobsReport{ReportBase: base, Limit: limit, Rows: []LongJob{}}
	for _, j := range jobs {
		lj := LongJob{JobNumber: j.JobNumber, First: j.First.UTC().Format(time.RFC3339), Last: j.Last.UTC().Format(time.RFC3339),
			Seconds: int64(j.Last.Sub(j.First).Seconds()), Pages: j.Pages, EventType: s.eventType(j.EventType)}
		if j.Locality != "" {
			l := j.Locality
			lj.Locality = &l
		}
		out.Rows = append(out.Rows, lj)
	}
	writeJSON(w, http.StatusOK, out)
}

// ApplianceRow is one appliance (R8's units, shown as Appliances) and the
// jobs naming it (R38h).
type ApplianceRow struct {
	Appliance string  `json:"appliance"`
	Jobs      int64   `json:"jobs"`
	Share     float64 `json:"share"`
}

type AppliancesReport struct {
	ReportBase
	Rows []ApplianceRow `json:"rows"`
}

func (s *Server) appliancesReport(w http.ResponseWriter, r *http.Request) {
	f, base, ok := s.reportFilter(w, r)
	if !ok {
		return
	}
	if err := s.reportReady(r, &base); err != nil {
		s.reportFailed(w, err)
		return
	}
	ctx := r.Context()
	counts, err := s.Reports.Appliances(ctx, f)
	if err == nil {
		base.Jobs, err = s.Reports.Total(ctx, f)
	}
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	out := AppliancesReport{ReportBase: base, Rows: []ApplianceRow{}}
	for _, c := range counts {
		out.Rows = append(out.Rows, ApplianceRow{Appliance: c.Key, Jobs: c.Jobs, Share: share(c.Jobs, base.Jobs)})
	}
	writeJSON(w, http.StatusOK, out)
}

// LargeEventsBucket is one week (from Monday) or calendar month (R38i).
type LargeEventsBucket struct {
	Start   string `json:"start"` // YYYY-MM-DD
	Events  int64  `json:"events"`
	Seconds *int64 `json:"median_seconds"` // null: no events
	Jobs    *int64 `json:"median_jobs"`
}

type LargeEventsReport struct {
	From    string              `json:"from"`
	To      string              `json:"to"`
	By      string              `json:"by"` // week or month
	Events  int64               `json:"events"`
	Seconds *int64              `json:"median_seconds"`
	Jobs    *int64              `json:"median_jobs"`
	Buckets []LargeEventsBucket `json:"buckets"`
}

// median is the middle value by nearest rank (the lower middle of an even
// number), or nil for none.
func median(v []int64) *int64 {
	if len(v) == 0 {
		return nil
	}
	v = slices.Clone(v)
	slices.Sort(v)
	m := v[(len(v)-1)/2]
	return &m
}

func (s *Server) largeEventsReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out := LargeEventsReport{From: q.Get("from"), To: q.Get("to"), By: q.Get("by"), Buckets: []LargeEventsBucket{}}
	if out.By == "" {
		out.By = "month"
	}
	if out.By != "week" && out.By != "month" {
		problem(w, http.StatusBadRequest, "by must be week or month")
		return
	}
	if out.From == "" || out.To == "" {
		problem(w, http.StatusBadRequest, "from and to are required (YYYY-MM-DD)")
		return
	}
	from, err := reports.ParseDay(out.From)
	var to time.Time
	if err == nil {
		to, err = reports.ParseDay(out.To)
	}
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	if to.Before(from) {
		problem(w, http.StatusBadRequest, "from must not be after to")
		return
	}
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	local := func(d time.Time) time.Time { return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc) }
	events, err := s.Reports.LargeEvents(r.Context(), local(from), local(to.AddDate(0, 0, 1)))
	if err != nil {
		s.reportFailed(w, err)
		return
	}
	start := func(d time.Time) time.Time {
		if out.By == "month" {
			return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
		day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	}
	next := func(d time.Time) time.Time {
		if out.By == "month" {
			return d.AddDate(0, 1, 0)
		}
		return d.AddDate(0, 0, 7)
	}
	type acc struct{ seconds, jobs []int64 }
	buckets := map[string]*acc{}
	var keys []string
	for b := start(from); !b.After(to); b = next(b) {
		k := b.Format(time.DateOnly)
		keys = append(keys, k)
		buckets[k] = &acc{}
	}
	var allSeconds, allJobs []int64
	for _, e := range events {
		l := e.First.In(loc)
		k := start(time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)).Format(time.DateOnly)
		secs := int64(e.Last.Sub(e.First).Seconds())
		if a := buckets[k]; a != nil {
			a.seconds = append(a.seconds, secs)
			a.jobs = append(a.jobs, e.Jobs)
		}
		allSeconds, allJobs = append(allSeconds, secs), append(allJobs, e.Jobs)
	}
	out.Events = int64(len(events))
	out.Seconds, out.Jobs = median(allSeconds), median(allJobs)
	for _, k := range keys {
		a := buckets[k]
		out.Buckets = append(out.Buckets, LargeEventsBucket{Start: k, Events: int64(len(a.seconds)), Seconds: median(a.seconds), Jobs: median(a.jobs)})
	}
	writeJSON(w, http.StatusOK, out)
}
