package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/RIKIKU/PagerArchive/internal/longjob"
	"github.com/RIKIKU/PagerArchive/internal/redaction"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// getRedaction is GET /api/v1/admin/redaction (R45a): retroactive
// redaction's state, how far it has got, its rate and time left while it
// runs, and whether the rules in use are still to be applied to every page.
// Counts and codes only (P1-P3).
func (s *Server) getRedaction(w http.ResponseWriter, r *http.Request) {
	if s.Redaction == nil {
		problem(w, http.StatusNotFound, "not found")
		return
	}
	st, err := s.Redaction.GetStatus(r.Context())
	if err != nil {
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.ErrorContext(r.Context(), "retroactive redaction status", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// POST /api/v1/admin/redaction/{pause,resume,stop,start} (R45a): an admin
// pauses the running retroactive redaction, carries on a paused one, stops
// one (the pages it did stay done; the next nightly run starts it again), or
// starts it again from the beginning. Each is logged with the admin's ID, as
// other admin actions are.

func (s *Server) pauseRedaction(w http.ResponseWriter, r *http.Request) {
	s.redactionAction(w, r, "pause", (*redaction.Worker).Pause, func(ctx context.Context, args ...any) {
		s.Log.InfoContext(ctx, "retroactive redaction paused", args...)
	})
}

func (s *Server) resumeRedaction(w http.ResponseWriter, r *http.Request) {
	s.redactionAction(w, r, "resume", (*redaction.Worker).Resume, func(ctx context.Context, args ...any) {
		s.Log.InfoContext(ctx, "retroactive redaction resumed", args...)
	})
}

func (s *Server) stopRedaction(w http.ResponseWriter, r *http.Request) {
	s.redactionAction(w, r, "stop", (*redaction.Worker).Stop, func(ctx context.Context, args ...any) {
		s.Log.InfoContext(ctx, "retroactive redaction stopped", args...)
	})
}

func (s *Server) startRedaction(w http.ResponseWriter, r *http.Request) {
	s.redactionAction(w, r, "start", (*redaction.Worker).StartAgain, func(ctx context.Context, args ...any) {
		s.Log.InfoContext(ctx, "retroactive redaction started again", args...)
	})
}

// redactionAction does one: 200 with the status, or 409 with a code when
// the state doesn't allow it.
func (s *Server) redactionAction(w http.ResponseWriter, r *http.Request, op string,
	fn func(*redaction.Worker, context.Context) (redaction.Status, error), logged func(context.Context, ...any)) {
	if s.Redaction == nil {
		problem(w, http.StatusNotFound, "not found")
		return
	}
	st, err := fn(s.Redaction, r.Context())
	var refusal *longjob.Refusal
	switch {
	case errors.As(err, &refusal):
		writeJSON(w, http.StatusConflict, Problem{Code: refusal.Code, Error: refusal.Message})
	case err != nil:
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.ErrorContext(r.Context(), "retroactive redaction change", "op", op, "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "something went wrong; nothing was changed")
	default:
		logged(r.Context(), "by", currentUser(r).ID, "state", string(st.State), "done", st.Done, "total", st.Total)
		writeJSON(w, http.StatusOK, st)
	}
}
