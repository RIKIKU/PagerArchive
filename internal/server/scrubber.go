package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/search"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// Mark is one of the scrubber's marks, and how many messages of the search it
// holds (R81b-R81d, ADR 0026). Start and End are in the deployment's time
// zone, so the browser labels them as the list does.
type Mark struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Kind  string    `json:"kind"` // hour, day, week, month, or block (5 minutes)
	Count int64     `json:"count"`
}

// Counts is the scrubber for a search: its marks, newest first, and whether
// they were counted (false when counting would have taken too long; the
// marks still work, without their dots).
type Counts struct {
	Marks   []Mark `json:"marks"`
	Counted bool   `json:"counted"`
}

// messageCounts gives the scrubber's marks for a message list and its search
// (R81b, R81c): the list's own filters, its album (R82) or the view's alias or
// job included, counted in one query (Store.CountMessages). With hour, the
// start of an hour of today or yesterday, it's that hour's twelve 5-minute
// blocks instead (R81d).
func (s *Server) messageCounts(w http.ResponseWriter, r *http.Request) {
	f, ok := s.messageFilter(w, r)
	if !ok {
		return
	}
	now := s.Now()
	var marks []search.Mark
	if v := r.URL.Query().Get("hour"); v != "" {
		hour, err := time.Parse(time.RFC3339, v)
		if err == nil {
			marks, err = search.Zoom(now, s.Location, hour)
		}
		if err != nil {
			problem(w, http.StatusBadRequest, search.ErrNoZoom.Error())
			return
		}
	} else {
		marks = search.Marks(now, s.Location, f.From, f.To)
	}
	out := Counts{Marks: make([]Mark, len(marks)), Counted: true}
	for i, m := range marks {
		out.Marks[i] = Mark{Start: m.Start.In(s.Location), End: m.End.In(s.Location), Kind: m.Kind}
	}
	if len(marks) > 0 {
		p, err := f.Params(s.Ingest.Network, nil, 1)
		var counts []int64
		if err == nil {
			counts, err = s.Store.CountMessages(r.Context(), p, search.Bounds(marks), store.CountsTimeout)
		}
		switch {
		case errors.Is(err, store.ErrCountsTimedOut):
			out.Counted = false
		case err != nil:
			s.Log.Error("message counts", "error", store.SafeError(err))
			problem(w, http.StatusInternalServerError, "counting failed")
			return
		default:
			// Bounds are oldest first; the marks newest first.
			for i := range out.Marks {
				out.Marks[i].Count = counts[len(counts)-1-i]
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
