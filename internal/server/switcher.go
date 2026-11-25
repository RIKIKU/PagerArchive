package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// The app switcher (R77e) and who sees it (R77f, ADR 0051): with
// RADIOARCHIVE_URL set, the frame shows a switcher to RadioArchive to the
// users an admin marked, or to everyone once "Show the switcher to
// everyone" is on. It only hides the switcher: it isn't access control.

// switcherEveryone is the settings row for "Show the switcher to everyone":
// "true" or "false"; none is false, a new install's default.
const switcherEveryone = "switcher_everyone"

// SwitcherSettings is the body of GET and PUT /api/v1/admin/switcher.
type SwitcherSettings struct {
	Everyone bool `json:"everyone"`
}

// SeesSwitcherRequest is the body of PUT /api/v1/admin/users/{id}/switcher.
type SeesSwitcherRequest struct {
	SeesSwitcher bool `json:"sees_switcher"`
}

func (s *Server) switcherForEveryone(ctx context.Context) (bool, error) {
	v, err := s.Store.GetSetting(ctx, switcherEveryone)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	on, _ := strconv.ParseBool(v)
	return on, nil
}

// showSwitcher is whether the frame shows u the switcher, worked out here
// rather than in the browser: RadioArchive is set, and u is marked or it's
// shown to everyone.
func (s *Server) showSwitcher(ctx context.Context, u db.User) (bool, error) {
	if s.RadioArchiveURL == "" {
		return false, nil
	}
	if u.SeesSwitcher {
		return true, nil
	}
	return s.switcherForEveryone(ctx)
}

func (s *Server) getSwitcher(w http.ResponseWriter, r *http.Request) {
	on, err := s.switcherForEveryone(r.Context())
	if err != nil {
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.Error("switcher settings", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, SwitcherSettings{Everyone: on})
}

func (s *Server) putSwitcher(w http.ResponseWriter, r *http.Request) {
	var req SwitcherSettings
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.Store.SetSetting(r.Context(), db.SetSettingParams{Key: switcherEveryone, Value: strconv.FormatBool(req.Everyone)}); err != nil {
		if s.abandoned(w, r, err) {
			return
		}
		s.Log.Error("switcher settings", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "couldn't save")
		return
	}
	// No audit trail of settings exists: logged, as the large event threshold is.
	s.Log.Info("switcher for everyone changed", "by", currentUser(r).ID, "everyone", req.Everyone)
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) setSeesSwitcher(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req SeesSwitcherRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.Accounts.SetSeesSwitcher(r.Context(), currentUser(r), id, req.SeesSwitcher); err != nil {
		s.accountsError(w, r, "set switcher", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
