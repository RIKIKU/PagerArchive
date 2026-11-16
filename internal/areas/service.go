package areas

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// Areas are the loaded areas as the API serves them (R88b-R88e).
type Areas struct {
	Label       string   // what one is called, such as "District"
	Attribution string   // the data's credit, shown on the map
	Source      string   // the file's name, for admins
	Names       []string // every area's name, in order
	// Version names this set of outlines in the map's URL, so a browser
	// keeps them until the areas change (R88c).
	Version string
	// Map is the outlines and label points as a GeoJSON FeatureCollection,
	// and MapGzip the same gzipped.
	Map, MapGzip []byte
}

// Feature is one feature of the map's GeoJSON: an area's outline, or the
// point its label goes at (Label true).
type feature struct {
	Type       string          `json:"type"`
	Properties featureProps    `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

type featureProps struct {
	Name  string `json:"name"`
	Label bool   `json:"label,omitempty"`
}

// Prepare reads the loaded areas' outlines from the database, for the map.
func Prepare(ctx context.Context, st *store.Store, cfg *profile.Areas, f *File) (*Areas, error) {
	a := &Areas{Label: cfg.Label, Attribution: cfg.Attribution, Source: filepath.Base(cfg.Path), Version: f.Hash[:16], Names: []string{}}
	rows, err := st.Pool.Query(ctx, `SELECT name, ST_AsGeoJSON(outline, $1), ST_AsGeoJSON(label_point, $1) FROM areas ORDER BY name`, mapDigits)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var outlines, labels []feature
	for rows.Next() {
		var name, outline, label string
		if err := rows.Scan(&name, &outline, &label); err != nil {
			return nil, err
		}
		a.Names = append(a.Names, name)
		outlines = append(outlines, feature{Type: "Feature", Properties: featureProps{Name: name}, Geometry: json.RawMessage(outline)})
		labels = append(labels, feature{Type: "Feature", Properties: featureProps{Name: name, Label: true}, Geometry: json.RawMessage(label)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	fc := struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}{Type: "FeatureCollection", Features: append(outlines, labels...)}
	if a.Map, err = json.Marshal(fc); err != nil {
		return nil, err
	}
	var gz bytes.Buffer
	w, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if _, err := w.Write(a.Map); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	a.MapGzip = gz.Bytes()
	return a, nil
}
