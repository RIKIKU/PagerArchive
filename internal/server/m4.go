package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/nightly"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/search"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

const (
	// mapLimit caps the points one map request returns.
	mapLimit = 2000
	// mapDefaultWindow is the map's default time range (section 14).
	mapDefaultWindow = 24 * time.Hour
	// NotReceivingAfter is R39's silence: no message of any kind.
	NotReceivingAfter = 30 * time.Minute
)

// MapPage is the located messages matching a filter (R35).
type MapPage struct {
	Messages  []Message `json:"messages"`
	Truncated bool      `json:"truncated"` // more matched than mapLimit; newest kept
	From      *string   `json:"from"`      // the range used, RFC 3339
	To        *string   `json:"to"`
}

func (s *Server) mapMessages(w http.ResponseWriter, r *http.Request) {
	admin := s.isAdmin(r)
	f, err := search.Parse(r.URL.Query(), admin, s.Location)
	if err == nil {
		_, err = f.Params(s.Ingest.Network, nil, 1)
	}
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	if f.From == nil && f.To == nil {
		from := s.Now().Add(-mapDefaultWindow)
		f.From = &from
	}
	f.Located = true
	rows, err := s.search(r, f, nil, mapLimit+1, nil)
	if err != nil {
		s.Log.Error("map", "error", err.Error())
		problem(w, http.StatusInternalServerError, "search failed")
		return
	}
	out := MapPage{Messages: []Message{}}
	if len(rows) > mapLimit {
		out.Truncated, rows = true, rows[:mapLimit]
	}
	for _, m := range rows {
		out.Messages = append(out.Messages, s.toMessage(m, admin))
	}
	stamp := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		v := t.UTC().Format(time.RFC3339)
		return &v
	}
	out.From, out.To = stamp(f.From), stamp(f.To)
	writeJSON(w, http.StatusOK, out)
}

// mapTiles serves the deployment's PMTiles extract with range requests, so
// no tile server is needed and no third party sees what's viewed (R35,
// ADR 0001).
func (s *Server) mapTiles(w http.ResponseWriter, r *http.Request) {
	if s.MapTiles == "" {
		problem(w, http.StatusNotFound, "no map tiles configured (MAP_TILES)")
		return
	}
	f, err := os.Open(s.MapTiles)
	if err != nil {
		s.Log.Error("map tiles", "error", "can't open MAP_TILES")
		problem(w, http.StatusNotFound, "map tiles unavailable")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		problem(w, http.StatusNotFound, "map tiles unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.pmtiles")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "map.pmtiles", st.ModTime(), f)
}

// Report is R38's capcode report.
type Report struct {
	Capcode    string           `json:"capcode"`
	From       string           `json:"from"` // dates, inclusive, deployment time zone
	To         string           `json:"to"`
	Categories []ReportCategory `json:"categories"` // network order, then Other; empty ones left out
	Total      int64            `json:"total"`
}

// ReportCategory counts one category. Other lists its raw codes, one row
// each (section 14); a null code is a page with no event type, such as a
// parse failure.
type ReportCategory struct {
	Category string       `json:"category"`
	Count    int64        `json:"count"`
	Codes    []ReportCode `json:"codes,omitempty"`
}

type ReportCode struct {
	Code  *string `json:"code"`
	Count int64   `json:"count"`
}

func (s *Server) capcodeReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if q.Get("capcode") == "" || from == "" || to == "" {
		problem(w, http.StatusBadRequest, "capcode, from and to are required")
		return
	}
	// Only the capcode and the date range: the same parsing as search.
	f, err := search.Parse(url.Values{"capcode": {q.Get("capcode")}, "from": {from}, "to": {to}}, false, s.Location)
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	if !f.From.Before(*f.To) {
		problem(w, http.StatusBadRequest, "from must not be after to")
		return
	}
	rows, err := s.Store.CapcodeReport(r.Context(), db.CapcodeReportParams{Capcode: f.Capcode, SentFrom: *f.From, SentTo: *f.To})
	if err != nil {
		s.Log.Error("report", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "report failed")
		return
	}
	out := Report{Capcode: f.Capcode, From: from, To: to, Categories: []ReportCategory{}}
	counts := map[string]*ReportCategory{}
	for _, row := range rows {
		cat := "Other"
		if row.EventType != nil {
			cat = pipeline.Category(s.Ingest.Network, *row.EventType)
		}
		c := counts[cat]
		if c == nil {
			c = &ReportCategory{Category: cat}
			counts[cat] = c
		}
		c.Count += row.Messages
		if cat == "Other" {
			c.Codes = append(c.Codes, ReportCode{Code: row.EventType, Count: row.Messages})
		}
		out.Total += row.Messages
	}
	for _, c := range s.Ingest.Network.Categories {
		if rc := counts[c.Name]; rc != nil {
			out.Categories = append(out.Categories, *rc)
		}
	}
	if rc := counts["Other"]; rc != nil {
		sort.SliceStable(rc.Codes, func(i, j int) bool { return rc.Codes[i].Count > rc.Codes[j].Count })
		out.Categories = append(out.Categories, *rc)
	}
	writeJSON(w, http.StatusOK, out)
}

// receiving is R39: a message of any kind within the last 30 minutes. A
// client that has never sent one counts from when it was added.
func receiving(c db.ListClientsRow, now time.Time) bool {
	since := c.CreatedAt
	if c.LastSeenAt != nil {
		since = *c.LastSeenAt
	}
	return now.Sub(since) <= NotReceivingAfter
}

// Retention is the admin retention screen's state (R45, R46).
type Retention struct {
	Days        int             `json:"days"`
	DefaultDays int             `json:"default_days"`
	MinDays     int             `json:"min_days"`
	MaxDays     int             `json:"max_days"`
	NextRun     time.Time       `json:"next_run"`
	Cutoff      string          `json:"cutoff"` // YYYY-MM-DD: messages before this date go at the next run
	LastRun     *nightly.Result `json:"last_run"`
}

func (s *Server) retentionState(r *http.Request) (Retention, error) {
	days, err := s.Nightly.Days(r.Context())
	if err != nil {
		return Retention{}, err
	}
	last, err := s.Nightly.LastResult(r.Context())
	if err != nil {
		return Retention{}, err
	}
	next := s.Nightly.NextRun(s.Now())
	return Retention{
		Days: days, DefaultDays: nightly.DefaultDays, MinDays: nightly.MinDays, MaxDays: nightly.MaxDays,
		NextRun: next.UTC(), Cutoff: s.Nightly.Cutoff(next, days).Format(time.DateOnly), LastRun: last,
	}, nil
}

func (s *Server) getRetention(w http.ResponseWriter, r *http.Request) {
	st, err := s.retentionState(r)
	if err != nil {
		s.Log.Error("retention", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// RetentionChange is the body of PUT /api/v1/admin/retention. Shortening
// the period needs ConfirmCutoff set to the cut-off date the admin was
// shown (R46).
type RetentionChange struct {
	Days          int    `json:"days"`
	ConfirmCutoff string `json:"confirm_cutoff"`
}

// RetentionConfirm asks the admin to confirm a shorter period (R46). No
// row count is given.
type RetentionConfirm struct {
	Problem
	Cutoff  string `json:"cutoff"`
	Message string `json:"message"`
}

// CodeConfirmCutoff: shortening retention needs the cut-off confirmed.
const CodeConfirmCutoff = "confirm_cutoff"

func (s *Server) putRetention(w http.ResponseWriter, r *http.Request) {
	var req RetentionChange
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Days < nightly.MinDays || req.Days > nightly.MaxDays {
		problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("retention must be %d to %d days", nightly.MinDays, nightly.MaxDays))
		return
	}
	cur, err := s.retentionState(r)
	if err != nil {
		s.Log.Error("retention", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if req.Days < cur.Days {
		cutoff := s.Nightly.Cutoff(s.Nightly.NextRun(s.Now()), req.Days)
		if req.ConfirmCutoff != cutoff.Format(time.DateOnly) {
			writeJSON(w, http.StatusConflict, RetentionConfirm{
				Problem: Problem{Error: "shortening retention needs the cut-off date confirmed", Code: CodeConfirmCutoff},
				Cutoff:  cutoff.Format(time.DateOnly),
				Message: fmt.Sprintf("All messages older than %s will be deleted at the next nightly run. Confirm?", cutoff.Format("2 January 2006")),
			})
			return
		}
	}
	if err := s.Nightly.SetDays(r.Context(), req.Days); err != nil {
		s.Log.Error("retention", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't save")
		return
	}
	s.Log.Info("retention changed", "user", currentUser(r).ID, "days", req.Days)
	s.getRetention(w, r)
}
