package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/events"
	"github.com/RIKIKU/PagerArchive/internal/search"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// LargeEvent is a large event as the app shows it (R87c, ADR 0027). Every
// number is counted from the pages it still holds (R87e).
type LargeEvent struct {
	ID int64 `json:"id"`
	// Start and End are its first and last pages' sent times. Whether it's
	// still open to new pages (R87a) isn't shown: the app can't know whether
	// the incident is over (R87c).
	Start  time.Time    `json:"start"`
	End    time.Time    `json:"end"`
	Levels []EventLevel `json:"levels"` // the levels met, in R87b's order
	Pages  int64        `json:"pages"`
	Codes  int64        `json:"deployment_codes"`
	Jobs   int64        `json:"jobs"`
	Units  int64        `json:"units"`
	// The event's own view only: its codes and job numbers, by first page.
	CodeList []string `json:"deployment_code_list,omitempty"`
	JobList  []string `json:"job_list,omitempty"`
}

// EventLevel is a level an event has met, and when (R87b).
type EventLevel struct {
	Level string    `json:"level"`
	Label string    `json:"label"`
	At    time.Time `json:"at"`
}

// LargeEventPage is one page of the Large events list, newest first.
type LargeEventPage struct {
	Events []LargeEvent `json:"events"`
	Next   string       `json:"next,omitempty"`
}

const eventPageSize = 50

func (s *Server) toEvent(e db.LargeEvent, pages, codes, jobs, units int64) LargeEvent {
	out := LargeEvent{ID: e.ID, Start: e.FirstAt.UTC(), End: e.LastAt.UTC(),
		Levels: []EventLevel{}, Pages: pages, Codes: codes, Jobs: jobs, Units: units}
	for _, l := range []struct {
		level events.Level
		at    *time.Time
	}{{events.Deployment, e.DeploymentAt}, {events.SeveralDeployments, e.SeveralDeploymentsAt}, {events.BigJob, e.BigJobAt}} {
		if l.at != nil {
			out.Levels = append(out.Levels, EventLevel{Level: string(l.level), Label: l.level.Label(), At: l.at.UTC()})
		}
	}
	return out
}

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
		ev := db.LargeEvent{ID: e.ID, FirstAt: e.FirstAt, LastAt: e.LastAt, DeploymentAt: e.DeploymentAt,
			SeveralDeploymentsAt: e.SeveralDeploymentsAt, BigJobAt: e.BigJobAt, CreatedAt: e.CreatedAt}
		out.Events = append(out.Events, s.toEvent(ev, e.Pages, e.Codes, e.Jobs, e.Units))
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
	var codes []db.LargeEventCodeTimesRow
	var jobs []db.LargeEventJobsRow
	var units, pages int64
	if err == nil {
		codes, err = s.Store.LargeEventCodeTimes(ctx, id)
	}
	if err == nil {
		jobs, err = s.Store.LargeEventJobs(ctx, id)
	}
	if err == nil {
		units, err = s.Store.LargeEventUnits(ctx, id)
	}
	if err == nil {
		pages, err = s.Store.LargeEventPages(ctx, id)
	}
	if err != nil {
		s.Log.Error("large event", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := s.toEvent(e, pages, int64(len(codes)), int64(len(jobs)), units)
	out.CodeList, out.JobList = []string{}, []string{}
	for _, c := range codes {
		out.CodeList = append(out.CodeList, c.Code)
	}
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
	Units int `json:"units"` // the big-job threshold: distinct units on one job
}

func (s *Server) getLargeEventSettings(w http.ResponseWriter, r *http.Request) {
	n, err := events.Units(r.Context(), s.Store.Queries)
	if err != nil {
		s.Log.Error("large event settings", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, LargeEventSettings{Units: n})
}

func (s *Server) putLargeEventSettings(w http.ResponseWriter, r *http.Request) {
	var req LargeEventSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := events.SetUnits(r.Context(), s.Store.Queries, req.Units); err != nil {
		if errors.Is(err, events.ErrUnits) {
			problem(w, http.StatusUnprocessableEntity, "The number of units must be a whole number from 2 to 1000.")
			return
		}
		s.Log.Error("large event settings", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't save")
		return
	}
	s.Log.Info("large event threshold changed", "by", currentUser(r).ID, "units", req.Units)
	writeJSON(w, http.StatusOK, req)
}
