package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/RIKIKU/PagerArchive/internal/areas"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// MetaAreas tells the browser about the network profile's areas (R88): what
// one is called, whose data they are, and the version of their outlines.
type MetaAreas struct {
	Label       string `json:"label"`
	Attribution string `json:"attribution"`
	// Version goes in the outlines' URL (GET /api/v1/map/areas?v=…), so the
	// browser keeps them until the areas change (R88c).
	Version string `json:"version"`
	Count   int    `json:"count"`
}

func (s *Server) metaAreas() *MetaAreas {
	if s.Areas == nil {
		return nil
	}
	return &MetaAreas{Label: s.Areas.Label, Attribution: s.Areas.Attribution, Version: s.Areas.Version, Count: len(s.Areas.Names)}
}

// addAreas gives each message the area it's in (R88b), in one query. With no
// areas, it does nothing.
func (s *Server) addAreas(ctx context.Context, msgs []Message) error {
	if s.Areas == nil || len(msgs) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		if m.Location != nil {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.Store.MessageAreas(ctx, ids)
	if err != nil {
		return errors.New(store.SafeError(err))
	}
	in := make(map[int64]string, len(rows))
	for _, r := range rows {
		in[r.MessageID] = r.Area
	}
	for i := range msgs {
		if a, ok := in[msgs[i].ID]; ok {
			msgs[i].Area = &a
		}
	}
	return nil
}

// JobAreas is the reply to GET /api/v1/jobs/{job}/areas: the areas a job's
// pages are in, by name (R88b).
type JobAreas struct {
	Areas []string `json:"areas"`
}

func (s *Server) getJobAreas(w http.ResponseWriter, r *http.Request) {
	out := JobAreas{Areas: []string{}}
	if s.Areas != nil {
		names, err := s.Store.JobAreas(r.Context(), r.PathValue("job"))
		if err != nil {
			s.Log.Error("job areas", "error", store.SafeError(err))
			problem(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		out.Areas = append(out.Areas, names...)
	}
	writeJSON(w, http.StatusOK, out)
}

// mapAreas serves the areas' outlines and label points for the map, as
// GeoJSON (R88c), gzipped for a browser that takes it. Asked for by its
// version (?v=, from meta), it's kept for a year: a change of areas changes
// the version. Asked for without it, the browser checks again each time.
func (s *Server) mapAreas(w http.ResponseWriter, r *http.Request) {
	if s.Areas == nil {
		problem(w, http.StatusNotFound, "the network profile names no areas")
		return
	}
	etag := `"` + s.Areas.Version + `"`
	h := w.Header()
	h.Set("Content-Type", "application/geo+json")
	h.Set("ETag", etag)
	h.Set("Vary", "Accept-Encoding")
	if r.URL.Query().Get("v") == s.Areas.Version {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := s.Areas.Map
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = s.Areas.MapGzip
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// AdminAreas is what Administration shows of the areas loaded (R88e).
type AdminAreas struct {
	Loaded      bool     `json:"loaded"`
	Label       string   `json:"label,omitempty"`
	Attribution string   `json:"attribution,omitempty"`
	Source      string   `json:"source,omitempty"` // the file's name
	Names       []string `json:"names"`
	// Pending: stored pages' areas are still being worked out (R88a).
	Pending bool `json:"pending"`
}

func (s *Server) adminAreas(w http.ResponseWriter, r *http.Request) {
	out := AdminAreas{Names: []string{}}
	if a := s.Areas; a != nil {
		pending, err := areas.Pending(r.Context(), s.Store)
		if err != nil {
			s.Log.Error("areas", "error", store.SafeError(err))
			problem(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		out = AdminAreas{Loaded: true, Label: a.Label, Attribution: a.Attribution, Source: a.Source, Names: a.Names, Pending: pending}
	}
	writeJSON(w, http.StatusOK, out)
}
