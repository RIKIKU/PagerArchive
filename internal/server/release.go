package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// ReleaseSeen is the answer of POST /api/v1/me/release-seen (R77b): the
// version running, and whether the signed-in user is to be told of it now.
type ReleaseSeen struct {
	Version string `json:"version"`
	Show    bool   `json:"show"`
}

// releaseVersion is a release's version, MAJOR.MINOR.PATCH and nothing else
// (12.1); any other build (0.0.0-dev, 1.9.1-dev+abc1234) isn't one.
var releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// releaseSeen tells the app whether to show the signed-in user the "New
// release" notice (R77b), and records the running version as told, in the
// same statement, so a reload, another tab or another device isn't told again.
// It's always about the user asking: a body naming anyone is refused. A
// build that isn't a release is never shown and records nothing; a user with
// nothing recorded yet (a new user, or every user the first time this runs)
// has the version recorded silently.
func (s *Server) releaseSeen(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&struct{}{}); err != nil && !errors.Is(err, io.EOF) {
		problem(w, http.StatusBadRequest, "no body, or an empty one: it's always about you")
		return
	}
	out := ReleaseSeen{Version: s.Version}
	if !releaseVersion.MatchString(s.Version) {
		writeJSON(w, http.StatusOK, out)
		return
	}
	before, err := s.Store.SeeRelease(r.Context(), db.SeeReleaseParams{ID: currentUser(r).ID, Version: s.Version})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Already told: here, or in another tab or browser.
	case err != nil:
		s.Log.ErrorContext(r.Context(), "release seen", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "something went wrong; nothing was changed")
		return
	default:
		out.Show = before != nil
	}
	writeJSON(w, http.StatusOK, out)
}
