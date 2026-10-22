package server

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/icon"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// Template icons (R65, R66; ADR 0011). An icon is uploaded as the request's
// body, from the browser or the PowerShell module, and the API does every
// check and the conversion (package icon). No endpoint takes a URL: the
// server never fetches anything from another site.

// iconURL is where a template's icon is served: its URL names its content,
// so it changes when the icon does. nil for no icon.
func iconURL(sum *string) *string {
	if sum == nil || *sum == "" {
		return nil
	}
	u := "/api/v1/icons/" + *sum + ".png"
	return &u
}

// IconSet is the reply to a stored icon.
type IconSet struct {
	Icon *string `json:"icon"`
}

func (s *Server) putTemplateIcon(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// The size is limited before the body is read: a declared length over
	// the limit is refused at once, and the reader stops one byte past it.
	// The Content-Type and any file name are never looked at.
	if r.ContentLength > icon.MaxBytes {
		s.iconRefused(w, r, id, http.StatusRequestEntityTooLarge, icon.TooLarge())
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, icon.MaxBytes))
	if tooBig := (*http.MaxBytesError)(nil); errors.As(err, &tooBig) {
		s.iconRefused(w, r, id, http.StatusRequestEntityTooLarge, icon.TooLarge())
		return
	}
	if err != nil {
		problem(w, http.StatusBadRequest, "couldn't read the upload")
		return
	}
	png, err := icon.Convert(data)
	if refusal := (*icon.Refusal)(nil); errors.As(err, &refusal) {
		s.iconRefused(w, r, id, http.StatusUnprocessableEntity, refusal)
		return
	}
	if err != nil {
		s.Log.Error("convert icon", "template", id, "error", err.Error())
		problem(w, http.StatusInternalServerError, "couldn't convert the icon; nothing was changed")
		return
	}
	sum, err := s.Catalog.SetTemplateIcon(r.Context(), id, png)
	if err != nil {
		s.catalogError(w, "set template icon", err)
		return
	}
	s.Log.Info("template icon set", "template", id, "by", currentUser(r).ID, "uploaded_bytes", len(data), "stored_bytes", len(png))
	writeJSON(w, http.StatusOK, IconSet{Icon: iconURL(&sum)})
}

func (s *Server) iconRefused(w http.ResponseWriter, r *http.Request, id int64, status int, refusal *icon.Refusal) {
	s.Log.Info("template icon refused", "template", id, "by", currentUser(r).ID, "code", refusal.Code)
	writeJSON(w, status, Problem{Error: refusal.Msg, Code: refusal.Code})
}

func (s *Server) deleteTemplateIcon(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.Catalog.SetTemplateIcon(r.Context(), id, nil); err != nil {
		s.catalogError(w, "remove template icon", err)
		return
	}
	s.Log.Info("template icon removed", "template", id, "by", currentUser(r).ID)
	w.WriteHeader(http.StatusNoContent)
}

var iconFile = regexp.MustCompile(`^([0-9a-f]{64})\.png$`)

// getIcon serves an icon to any signed-in user (ADR 0011). Its URL never
// changes for other content, so browsers keep it for a year and fetch it
// once (R66); "private" keeps it out of shared caches.
func (s *Server) getIcon(w http.ResponseWriter, r *http.Request) {
	m := iconFile.FindStringSubmatch(r.PathValue("file"))
	if m == nil {
		problem(w, http.StatusNotFound, "not found")
		return
	}
	png, err := s.Store.IconBySHA256(r.Context(), &m[1])
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		s.Log.Error("icon", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Content-Length", strconv.Itoa(len(png)))
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

// agencyProfiles lists the agency profiles loaded at start-up, for the
// template screens' choice (R64).
func (s *Server) agencyProfiles(w http.ResponseWriter, r *http.Request) {
	names := s.profileNames()
	slices.Sort(names)
	writeJSON(w, http.StatusOK, names)
}
