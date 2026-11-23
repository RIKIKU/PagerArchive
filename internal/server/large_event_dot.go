package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/live"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// NewEventFor is how long a large event is new for, from its start (R77c:
// fixed, not a setting).
const NewEventFor = 4 * time.Hour

// NewLargeEvent is whether the side menu shows the signed-in user the
// new-large-event dot (R77c): a large event started (its first page's sent
// time) within the last 4 hours and opened after they last opened the Large
// events page. It's the answer of GET /api/v1/me/new-large-event, of
// POST /api/v1/me/large-events-seen and of each state event of
// GET /api/v1/me/new-large-event/stream.
type NewLargeEvent struct {
	New bool `json:"new"`
	// Until is when the dot goes by itself, 4 hours after the start of the
	// newest new event; Now is the server's clock, so the browser can time
	// it whatever its own clock says.
	Until *time.Time `json:"until,omitempty"`
	Now   time.Time  `json:"now"`
}

// newLargeEventOf is the user's answer now: one indexed query.
func (s *Server) newLargeEventOf(ctx context.Context, userID int64) (NewLargeEvent, error) {
	now := s.Now()
	out := NewLargeEvent{Now: now.UTC()}
	start, err := s.Store.NewLargeEventStart(ctx, db.NewLargeEventStartParams{Since: now.Add(-NewEventFor), UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	until := start.Add(NewEventFor).UTC()
	out.New, out.Until = true, &until
	return out, nil
}

func (s *Server) getNewLargeEvent(w http.ResponseWriter, r *http.Request) {
	out, err := s.newLargeEventOf(r.Context(), currentUser(r).ID)
	if err != nil {
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.ErrorContext(r.Context(), "new large event", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// largeEventsSeen records that the signed-in user opened the Large events
// page (R77c), on the server, so the dot goes in their other tabs, browsers
// and devices too; their open ones are told at once. As release-seen
// (R77b), it's always about the user asking: a body naming anyone is
// refused.
func (s *Server) largeEventsSeen(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&struct{}{}); err != nil && !errors.Is(err, io.EOF) {
		problem(w, http.StatusBadRequest, "no body, or an empty one: it's always about you")
		return
	}
	ctx, u := r.Context(), currentUser(r)
	err := s.Store.SeeLargeEvents(ctx, db.SeeLargeEventsParams{ID: u.ID, At: s.Now()})
	var out NewLargeEvent
	if err == nil {
		if s.Hub != nil {
			s.Hub.SeenEvents(u.ID)
		}
		out, err = s.newLargeEventOf(ctx, u.ID)
	}
	if err != nil {
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.ErrorContext(ctx, "large events seen", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "something went wrong; nothing was changed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// newLargeEventStream sends the user's dot as Server-Sent Events (R77c): a
// state event at once, and again whenever a large event opens (live
// ingest's, or one a rebuild made from pages in no event) and whenever the
// user opens the Large events page anywhere. It ends with a reconnect
// event, as the message stream does, when the user's status or role
// changes, and for a bearer token when the token expires.
func (s *Server) newLargeEventStream(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), currentUser(r)
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n: connected\n\n")
	if err := rc.Flush(); err != nil {
		return
	}
	reconnect := func(reason string) {
		data, _ := json.Marshal(map[string]string{"reason": reason})
		fmt.Fprintf(w, "event: reconnect\ndata: %s\n\n", data)
		_ = rc.Flush()
	}
	watch := s.Hub.WatchEvents(u.ID)
	defer s.Hub.Unwatch(watch)
	// As the message stream: a change of status or role before the watch
	// was added found nothing to close.
	if now, err := s.Store.UserByID(ctx, u.ID); err != nil || now.Status != "active" || now.Role != u.Role {
		if err != nil {
			if s.noteAbandoned(r, err) {
				return
			}
			s.Log.Error("new large event stream: user", "error", store.SafeError(err))
		}
		reconnect(live.Account)
		return
	}
	var expired <-chan time.Time
	if exp, ok := tokenExpiry(r); ok {
		t := time.NewTimer(exp.Sub(s.Now()))
		defer t.Stop()
		expired = t.C
	}
	send := func() bool {
		out, err := s.newLargeEventOf(ctx, u.ID)
		if err != nil {
			if !s.noteAbandoned(r, err) {
				s.Log.Error("new large event stream", "error", store.SafeError(err))
			}
			return false
		}
		data, _ := json.Marshal(out)
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
		return rc.Flush() == nil
	}
	if !send() {
		return
	}
	// Set up: its span ends now, not when the tab closes (R72).
	s.http.EndRequest(r, http.StatusOK)
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watch.Dropped:
			reconnect(live.Account)
			return
		case <-expired:
			reconnect("expired")
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			if rc.Flush() != nil {
				return
			}
		case <-watch.C:
			if !send() {
				return
			}
		}
	}
}
