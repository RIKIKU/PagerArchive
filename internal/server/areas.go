package server

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/RIKIKU/PagerArchive/internal/areas"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// AreaRef is one area a message or a job is in (R88b): its layer's id and
// its name. The browser shows it with the layer's label ("District 08").
type AreaRef struct {
	Layer string `json:"layer"`
	Name  string `json:"name"`
	// DispatchChannel is the area's dispatch channel (R92f), when its
	// layer in the network profile gives it one; left out when it has none.
	DispatchChannel *int32 `json:"dispatch_channel,omitempty"`
}

// MetaAreas tells the browser about one layer of the network profile's
// areas (R88): what one is called, whose data they are, how the map outlines
// them, and the version of their outlines.
type MetaAreas struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Attribution string `json:"attribution"`
	Outline     string `json:"outline"`
	// Version goes in the outlines' URL (GET /api/v1/map/areas/{id}?v=…),
	// so the browser keeps them until the areas change (R88c).
	Version string `json:"version"`
	Count   int    `json:"count"`
}

// shownAreas is the layers with areas to show, in the profile's order.
func (s *Server) shownAreas() []*areas.Layer {
	var out []*areas.Layer
	for _, l := range s.Areas {
		if l.Shown() {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) metaAreas() []MetaAreas {
	var out []MetaAreas
	for _, l := range s.shownAreas() {
		out = append(out, MetaAreas{ID: l.ID, Label: l.Label, Attribution: l.Attribution, Outline: l.Outline, Version: l.Version, Count: len(l.Names)})
	}
	return out
}

// sortRefs puts areas in the order the profile lists their layers, then by
// name, leaving out layers that aren't shown (a row from a layer removed
// since, until it's gone).
func (s *Server) sortRefs(refs []AreaRef) []AreaRef {
	order := map[string]int{}
	layers := s.shownAreas()
	for i, l := range layers {
		order[l.ID] = i
	}
	out := refs[:0]
	for _, r := range refs {
		if i, ok := order[r.Layer]; ok {
			// R92f: its dispatch channel, from the profile, as it is now.
			if n, ok := layers[i].Dispatch.Channel(r.Name); ok {
				r.DispatchChannel = &n
			}
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := order[out[i].Layer], order[out[j].Layer]; a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// addAreas gives each message the areas it's in (R88b), in one query. With
// no areas, it does nothing.
func (s *Server) addAreas(ctx context.Context, msgs []Message) error {
	if len(s.shownAreas()) == 0 || len(msgs) == 0 {
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
		return store.SafeErr("", err)
	}
	in := make(map[int64][]AreaRef, len(rows))
	for _, r := range rows {
		in[r.MessageID] = append(in[r.MessageID], AreaRef{Layer: r.Layer, Name: r.Area})
	}
	for i := range msgs {
		if refs := s.sortRefs(in[msgs[i].ID]); len(refs) > 0 {
			msgs[i].Areas = refs
		}
	}
	return nil
}

// JobAreas is the reply to GET /api/v1/jobs/{job}/areas: the areas a job's
// pages are in, each layer's, in the profile's order and by name (R88b).
type JobAreas struct {
	Areas []AreaRef `json:"areas"`
}

func (s *Server) getJobAreas(w http.ResponseWriter, r *http.Request) {
	out := JobAreas{Areas: []AreaRef{}}
	if len(s.shownAreas()) > 0 {
		rows, err := s.Store.JobAreas(r.Context(), r.PathValue("job"))
		if err != nil {
			if s.abandoned(w, r, err) {
				return
			}
			s.Log.Error("job areas", "error", store.SafeError(err))
			problem(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		for _, row := range rows {
			out.Areas = append(out.Areas, AreaRef{Layer: row.Layer, Name: row.Area})
		}
		out.Areas = s.sortRefs(out.Areas)
	}
	writeJSON(w, http.StatusOK, out)
}

// mapAreas serves one layer's outlines and label points for the map, as
// GeoJSON (R88c), gzipped for a browser that takes it. Asked for by its
// version (?v=, from meta), it's kept for a year: a change of areas changes
// the version. Asked for without it, the browser checks again each time.
func (s *Server) mapAreas(w http.ResponseWriter, r *http.Request) {
	var l *areas.Layer
	for _, x := range s.shownAreas() {
		if x.ID == r.PathValue("layer") {
			l = x
		}
	}
	if l == nil {
		problem(w, http.StatusNotFound, "the network profile names no such areas")
		return
	}
	etag := `"` + l.Version + `"`
	h := w.Header()
	h.Set("Content-Type", "application/geo+json")
	h.Set("ETag", etag)
	h.Set("Vary", "Accept-Encoding")
	if r.URL.Query().Get("v") == l.Version {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := l.Map
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = l.MapGzip
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// AdminAreas is what Administration shows of the areas (R88e): each layer
// the profile names, in its order.
type AdminAreas struct {
	Layers []AdminAreaLayer `json:"layers"`
}

// AdminAreaLayer is one layer: what's loaded, and why it didn't load if it
// didn't (R88d).
type AdminAreaLayer struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Attribution string   `json:"attribution"`
	Outline     string   `json:"outline"`
	Source      string   `json:"source"` // the file's name
	Loaded      bool     `json:"loaded"` // it has areas to show
	Names       []string `json:"names"`
	// Pending: stored pages' areas of it are still being worked out (R88a).
	Pending bool `json:"pending"`
	// Error: its file didn't load at the last start; Stale: the areas
	// loaded before are shown meanwhile.
	Error string `json:"error,omitempty"`
	Stale bool   `json:"stale,omitempty"`
}

func (s *Server) adminAreas(w http.ResponseWriter, r *http.Request) {
	out := AdminAreas{Layers: []AdminAreaLayer{}}
	for _, l := range s.Areas {
		pending, err := areas.Pending(r.Context(), s.Store, l.ID)
		if err != nil {
			if s.abandoned(w, r, err) {
				return
			}
			s.Log.Error("areas", "error", store.SafeError(err))
			problem(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		names := l.Names
		if names == nil {
			names = []string{}
		}
		out.Layers = append(out.Layers, AdminAreaLayer{ID: l.ID, Label: l.Label, Attribution: l.Attribution, Outline: l.Outline, Source: l.Source,
			Loaded: l.Shown(), Names: names, Pending: pending && l.Shown(), Error: l.Error, Stale: l.Stale})
	}
	writeJSON(w, http.StatusOK, out)
}
