package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// getReparse is GET /api/v1/admin/reparse (R23c): the re-parse's state, how
// far it has got, its rate and time left while it runs, and the requests
// waiting. Counts and codes only.
func (s *Server) getReparse(w http.ResponseWriter, r *http.Request) {
	st, err := reparse.GetStatus(r.Context(), s.Store, s.Reparse)
	if err != nil {
		s.Log.ErrorContext(r.Context(), "re-parse status", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// POST /api/v1/admin/reparse/{pause,resume,stop,start} (R23c): an admin
// pauses the running re-parse, carries on a paused one, stops one (the rest
// of the pages stay as they were parsed before), or starts it again from the
// beginning. Each is logged with the admin's ID, as other admin actions are.

func (s *Server) pauseReparse(w http.ResponseWriter, r *http.Request) {
	s.reparseAction(w, r, "pause", reparse.Pause, func(ctx context.Context, args ...any) { s.Log.InfoContext(ctx, "re-parse paused", args...) })
}

func (s *Server) resumeReparse(w http.ResponseWriter, r *http.Request) {
	s.reparseAction(w, r, "resume", reparse.Resume, func(ctx context.Context, args ...any) { s.Log.InfoContext(ctx, "re-parse resumed", args...) })
}

func (s *Server) stopReparse(w http.ResponseWriter, r *http.Request) {
	s.reparseAction(w, r, "stop", reparse.Stop, func(ctx context.Context, args ...any) { s.Log.InfoContext(ctx, "re-parse stopped", args...) })
}

func (s *Server) startReparse(w http.ResponseWriter, r *http.Request) {
	s.reparseAction(w, r, "start", reparse.StartAgain, func(ctx context.Context, args ...any) { s.Log.InfoContext(ctx, "re-parse started again", args...) })
}

// reparseAction does one: 200 with the status, or 409 with a code when the
// re-parse's state doesn't allow it.
func (s *Server) reparseAction(w http.ResponseWriter, r *http.Request, op string,
	fn func(context.Context, *store.Store, *reparse.Progress) (reparse.Status, error), logged func(context.Context, ...any)) {
	st, err := fn(r.Context(), s.Store, s.Reparse)
	var refusal *reparse.Refusal
	switch {
	case errors.As(err, &refusal):
		writeJSON(w, http.StatusConflict, Problem{Code: refusal.Code, Error: refusal.Message})
	case err != nil:
		s.Log.ErrorContext(r.Context(), "re-parse change", "op", op, "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "something went wrong; nothing was changed")
	default:
		logged(r.Context(), "by", currentUser(r).ID, "state", string(st.State), "done", st.Done, "total", st.Total)
		writeJSON(w, http.StatusOK, st)
	}
}
