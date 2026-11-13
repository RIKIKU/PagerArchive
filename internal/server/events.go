package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/search"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// LargeEvent is a large event as the app shows it (R87c, ADR 0027): simply a
// large event, with no levels and no count or list of the codes that opened
// it (owner, 2026-11-13). Every number is counted from the pages it still
// holds (R87e).
type LargeEvent struct {
	ID int64 `json:"id"`
	// Start and End are its first and last pages' sent times. Whether it's
	// still open to new pages (R87a) isn't shown: the app can't know whether
	// the incident is over (R87c).
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// EmergencyPages is how many pages it holds, all Emergency pages that
	// count (R87). No appliance count is shown (owner, 2026-11-13).
	EmergencyPages int64 `json:"emergency_pages"`
	Jobs           int64 `json:"jobs"`
	// The event's own view only: its job numbers, by first page.
	JobList []string `json:"job_list,omitempty"`
}

// LargeEventPage is one page of the Large events list, newest first.
type LargeEventPage struct {
	Events []LargeEvent `json:"events"`
	Next   string       `json:"next,omitempty"`
}

const eventPageSize = 50

// listLargeEvents is the Large events page (R87c): every signed-in user's.
func (s *Server) listLargeEvents(w http.ResponseWriter, r *http.Request) {
	before, err := search.DecodeCursor(r.URL.Query().Get("before"))
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	p := db.ListLargeEventsParams{MaxRows: eventPageSize + 1}
	if before != nil {
		p.BeforeFirst, p.BeforeID = &before.SentAt, &before.ID
	}
	rows, err := s.Store.ListLargeEvents(r.Context(), p)
	if err != nil {
		s.Log.Error("large events", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := LargeEventPage{Events: []LargeEvent{}}
	if len(rows) > eventPageSize {
		rows = rows[:eventPageSize]
		last := rows[len(rows)-1]
		out.Next = search.EncodeCursor(search.Cursor{SentAt: last.FirstAt, ID: last.ID})
	}
	for _, e := range rows {
		out.Events = append(out.Events, LargeEvent{ID: e.ID, Start: e.FirstAt.UTC(), End: e.LastAt.UTC(), EmergencyPages: e.Pages, Jobs: e.Jobs})
	}
	writeJSON(w, http.StatusOK, out)
}

// getLargeEvent is one event, for its view (R87c).
func (s *Server) getLargeEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		problem(w, http.StatusNotFound, "no such large event")
		return
	}
	ctx := r.Context()
	e, err := s.Store.LargeEventByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "no such large event")
		return
	}
	var jobs []db.LargeEventJobsRow
	var pages int64
	if err == nil {
		jobs, err = s.Store.LargeEventJobs(ctx, id)
	}
	if err == nil {
		pages, err = s.Store.LargeEventPages(ctx, id)
	}
	if err != nil {
		s.Log.Error("large event", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := LargeEvent{ID: e.ID, Start: e.FirstAt.UTC(), End: e.LastAt.UTC(), EmergencyPages: pages, Jobs: int64(len(jobs)), JobList: []string{}}
	for _, j := range jobs {
		out.JobList = append(out.JobList, j.JobNumber)
	}
	writeJSON(w, http.StatusOK, out)
}

// eventParam reads a message list's large event (event=<id>), which any
// signed-in user may list (R87c). ok is false when it answered the request.
func (s *Server) eventParam(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	v := r.URL.Query().Get("event")
	if v == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err == nil && id > 0 {
		_, err = s.Store.LargeEventByID(r.Context(), id)
	} else {
		err = pgx.ErrNoRows
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		problem(w, http.StatusNotFound, "no such large event")
		return nil, false
	case err != nil:
		s.Log.Error("large event", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return nil, false
	}
	return &id, true
}

// eventRange is the days a large event's pages were sent on, in the
// deployment's time zone: the scrubber's range in its view when no dates are
// chosen (R81b), so an old event's days are on it.
func (s *Server) eventRange(r *http.Request, id int64) (from, to *time.Time, err error) {
	e, err := s.Store.LargeEventByID(r.Context(), id)
	if err != nil {
		return nil, nil, err
	}
	day := func(t time.Time) time.Time {
		t = t.In(s.Location)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, s.Location)
	}
	f, t := day(e.FirstAt), day(e.LastAt).AddDate(0, 0, 1)
	return &f, &t, nil
}

// LargeEventSettings are the admins' settings for large events (R87).
type LargeEventSettings struct {
	// JobPages is the job threshold: Emergency pages on one job that open a
	// large event (R87; owner, 2026-11-13).
	JobPages int `json:"job_pages"`
}

func (s *Server) getLargeEventSettings(w http.ResponseWriter, r *http.Request) {
	n, err := events.Threshold(r.Context(), s.Store.Queries)
	if err != nil {
		s.Log.Error("large event settings", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, LargeEventSettings{JobPages: n})
}

func (s *Server) putLargeEventSettings(w http.ResponseWriter, r *http.Request) {
	var req LargeEventSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := events.SetThreshold(r.Context(), s.Store.Queries, req.JobPages); err != nil {
		if errors.Is(err, events.ErrThreshold) {
			problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("The number of Emergency pages must be a whole number from %d to %d.", events.MinPages, events.MaxPages))
			return
		}
		s.Log.Error("large event settings", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't save")
		return
	}
	s.Log.Info("large event threshold changed", "by", currentUser(r).ID, "job_pages", req.JobPages)
	writeJSON(w, http.StatusOK, req)
}
