package areas

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// Layer is one layer of areas as the API serves it (R88b-R88e).
type Layer struct {
	ID          string   // the profile's id for it, such as "districts"
	Label       string   // shown before each name, such as "District"
	Attribution string   // the data's credit, shown on the map
	Outline     string   // how the map draws it: solid, dashed or dotted
	Source      string   // the file's name, for admins
	Names       []string // every area's name, in order
	// Version names this set of outlines in the map's URL, so a browser
	// keeps them until the areas change (R88c).
	Version string
	// Map is the outlines and label points as a GeoJSON FeatureCollection,
	// and MapGzip the same gzipped; nil when the layer has none to show.
	Map, MapGzip []byte
	// Error is why the profile's areas file for it didn't load this time
	// (R88d): the layer then shows the areas loaded before, if any (Stale),
	// or none.
	Error string
	Stale bool
}

// Shown reports whether the layer has areas to show.
func (l *Layer) Shown() bool { return l != nil && l.Map != nil }

// Feature is one feature of the map's GeoJSON: the layer's edges, or the
// point an area's label goes at (Label true).
type feature struct {
	Type       string          `json:"type"`
	Properties featureProps    `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

type featureProps struct {
	Name  string `json:"name,omitempty"`
	Label bool   `json:"label,omitempty"`
	Edges bool   `json:"edges,omitempty"`
}

// Start loads the network profile's layers of areas (R88d), as the API does
// before it listens: each one whose file changed is loaded, layers no longer
// named are removed with every page's area of them, and each layer's
// outlines are read for the map. A layer whose file is missing or broken, or
// fails to load, doesn't stop the API: it's logged as an error, kept with the
// areas loaded before (if any), and Administration says so. Only a layer
// loaded is returned with its outlines; every layer the profile names is
// returned, in its order.
func Start(ctx context.Context, st *store.Store, cfgs []*profile.Areas, log *slog.Logger) []*Layer {
	keep := make([]string, len(cfgs))
	for i, c := range cfgs {
		keep[i] = c.ID
	}
	if err := Clear(ctx, st, keep); err != nil {
		log.Error("areas no longer in the profile weren't removed", "error", store.SafeError(err))
	}
	out := make([]*Layer, 0, len(cfgs))
	for _, cfg := range cfgs {
		l := &Layer{ID: cfg.ID, Label: cfg.Label, Attribution: cfg.Attribution, Outline: cfg.Outline, Source: filepath.Base(cfg.Path)}
		out = append(out, l)
		started := time.Now()
		f, err := ReadFile(cfg.Path, cfg.NameProperty)
		if err == nil {
			var stats LoadStats
			if stats, err = Load(ctx, st, cfg.ID, f); err != nil {
				l.Error = "loading them failed: " + store.SafeError(err)
			} else if err = Prepare(ctx, st, l, f.Hash); err != nil {
				l.Error = "reading their outlines failed: " + store.SafeError(err)
			} else if !stats.Skipped {
				log.Info("areas loaded", "layer", cfg.ID, "areas", stats.Areas, "pieces", stats.Parts, "map_tolerance_degrees", stats.Tolerance,
					"map_bytes", len(l.Map), "map_gzip_bytes", len(l.MapGzip), "seconds", int(time.Since(started).Seconds()))
			}
		} else {
			l.Error = err.Error()
		}
		if l.Error == "" {
			continue
		}
		// The ones loaded before, if any, until the file is put right.
		l.Map, l.MapGzip, l.Names = nil, nil, nil
		if h, err := loadedHash(ctx, st, cfg.ID); err == nil && h != "" {
			if err := Prepare(ctx, st, l, h); err == nil {
				l.Stale = true
			}
		}
		log.Error("areas didn't load: the API starts without them", "layer", cfg.ID, "file", l.Source, "error", l.Error, "showing_previous", l.Stale)
	}
	return out
}

// Prepare reads a layer's loaded outlines from the database into l, for the
// map, as the version hash names: the layer's edges as one feature, each
// edge between two areas once, so a dashed line's dashes aren't drawn twice
// over each other out of step (and the map gets half the points); then each
// area's label point, named.
func Prepare(ctx context.Context, st *store.Store, l *Layer, hash string) error {
	l.Version, l.Names = hash[:min(16, len(hash))], []string{}
	rows, err := st.Pool.Query(ctx, `SELECT name, ST_AsGeoJSON(label_point, $1) FROM areas WHERE layer = $2 ORDER BY name`, mapDigits, l.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var labels []feature
	for rows.Next() {
		var name, label string
		if err := rows.Scan(&name, &label); err != nil {
			return err
		}
		l.Names = append(l.Names, name)
		labels = append(labels, feature{Type: "Feature", Properties: featureProps{Name: name, Label: true}, Geometry: json.RawMessage(label)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var edges *string
	if err := st.Pool.QueryRow(ctx, `SELECT ST_AsGeoJSON(ST_LineMerge(ST_Union(ST_Boundary(outline))), $1) FROM areas WHERE layer = $2 AND NOT ST_IsEmpty(outline)`,
		mapDigits, l.ID).Scan(&edges); err != nil {
		return err
	}
	features := []feature{}
	if edges != nil {
		features = append(features, feature{Type: "Feature", Properties: featureProps{Edges: true}, Geometry: json.RawMessage(*edges)})
	}
	fc := struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}{Type: "FeatureCollection", Features: append(features, labels...)}
	m, err := json.Marshal(fc)
	if err != nil {
		return err
	}
	var gz bytes.Buffer
	w, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if _, err := w.Write(m); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	l.Map, l.MapGzip = m, gz.Bytes()
	return nil
}
