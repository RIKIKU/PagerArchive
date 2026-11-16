// Package areas is a network profile's areas (R88, ADR 0035): named
// polygons, such as the districts a network's agencies are organised in,
// read from a GeoJSON file, loaded into PostGIS, outlined on the map, and the
// area each located page is in.
package areas

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// version changes when what Load makes of a file changes (its pieces or
// outlines), so every install loads its areas again.
const version = "1"

// File is an areas file, read and checked.
type File struct {
	Path  string
	Areas []Area // by name; features with the same name are one area
	// Hash decides when the areas are loaded again and every located page's
	// area worked out again (R88d): the file's bytes, the property naming
	// each area, and version.
	Hash string
}

// Area is one named area: its features' geometries, as GeoJSON.
type Area struct {
	Name       string
	Geometries []json.RawMessage
}

// MaxName is the longest name an area may have, in characters.
const MaxName = 40

// Problem is what's wrong with an areas file, where (a JSON Pointer).
type Problem struct {
	File, Path, Msg string
}

func (p *Problem) Error() string {
	if p.Path == "" {
		return p.File + ": " + p.Msg
	}
	return p.File + ": at " + p.Path + ": " + p.Msg
}

// The coordinate systems a file may say it's in: WGS84 longitude and
// latitude, as GeoJSON (RFC 7946) has it. An older file's "crs" member may
// name it; any other is refused rather than read wrongly.
var wgs84 = map[string]bool{
	"urn:ogc:def:crs:OGC:1.3:CRS84": true,
	"urn:ogc:def:crs:OGC::CRS84":    true,
	"urn:ogc:def:crs:EPSG::4326":    true,
	"EPSG:4326":                     true,
}

// ReadFile reads and checks an areas file (R88): a GeoJSON FeatureCollection
// of Polygon and MultiPolygon features in WGS84 longitude and latitude, each
// named by its nameProperty (a string or a number), every name at most
// MaxName characters. Every problem is returned, joined.
func ReadFile(path, nameProperty string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	var doc struct {
		Type string `json:"type"`
		CRS  *struct {
			Properties struct {
				Name string `json:"name"`
			} `json:"properties"`
		} `json:"crs"`
		Features []struct {
			Type       string                     `json:"type"`
			Geometry   *json.RawMessage           `json:"geometry"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, &Problem{File: name, Msg: "not valid JSON: " + err.Error()}
	}
	if doc.Type != "FeatureCollection" {
		return nil, &Problem{File: name, Path: "/type", Msg: `must be "FeatureCollection"`}
	}
	if doc.CRS != nil && !wgs84[doc.CRS.Properties.Name] {
		return nil, &Problem{File: name, Path: "/crs", Msg: fmt.Sprintf("coordinates must be WGS84 longitude and latitude (EPSG:4326), not %q", doc.CRS.Properties.Name)}
	}
	if len(doc.Features) == 0 {
		return nil, &Problem{File: name, Path: "/features", Msg: "has no features"}
	}
	var errs []error
	fail := func(p, msg string) { errs = append(errs, &Problem{File: name, Path: p, Msg: msg}) }
	byName := map[string]*Area{}
	for i, f := range doc.Features {
		p := fmt.Sprintf("/features/%d", i)
		v, ok := f.Properties[nameProperty]
		var n string
		if ok {
			n, ok = nameOf(v)
		}
		switch {
		case !ok:
			fail(p+"/properties/"+nameProperty, "must be a string or a number naming the area")
		case n == "":
			fail(p+"/properties/"+nameProperty, "is empty")
		case len([]rune(n)) > MaxName:
			fail(p+"/properties/"+nameProperty, fmt.Sprintf("is longer than %d characters", MaxName))
		}
		if f.Geometry == nil {
			fail(p+"/geometry", "must be a Polygon or MultiPolygon")
			continue
		}
		if msg := checkGeometry(*f.Geometry); msg != "" {
			fail(p+"/geometry", msg)
			continue
		}
		if n == "" || !ok {
			continue
		}
		a := byName[n]
		if a == nil {
			a = &Area{Name: n}
			byName[n] = a
		}
		a.Geometries = append(a.Geometries, *f.Geometry)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	out := &File{Path: path}
	for _, a := range byName {
		out.Areas = append(out.Areas, *a)
	}
	sort.Slice(out.Areas, func(i, j int) bool { return out.Areas[i].Name < out.Areas[j].Name })
	h := sha256.New()
	h.Write(data)
	fmt.Fprintf(h, "\nname property %q\nversion %s\n", nameProperty, version)
	out.Hash = hex.EncodeToString(h.Sum(nil))
	return out, nil
}

// nameOf is a name property's value as text: a string trimmed, or a
// number as the file writes it.
func nameOf(v json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return strings.TrimSpace(s), true
	}
	var n json.Number
	d := json.NewDecoder(bytes.NewReader(v))
	d.UseNumber()
	if err := d.Decode(&n); err == nil {
		return n.String(), true
	}
	return "", false
}

// checkGeometry says what's wrong with a feature's geometry, or "": a
// Polygon or MultiPolygon whose rings each have 4 or more positions, every
// one a longitude from -180 to 180 and a latitude from -90 to 90.
func checkGeometry(raw json.RawMessage) string {
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return "must be a Polygon or MultiPolygon"
	}
	var polygons [][][][]float64
	switch g.Type {
	case "Polygon":
		var p [][][]float64
		if err := json.Unmarshal(g.Coordinates, &p); err != nil {
			return "has coordinates that aren't a Polygon's"
		}
		polygons = [][][][]float64{p}
	case "MultiPolygon":
		if err := json.Unmarshal(g.Coordinates, &polygons); err != nil {
			return "has coordinates that aren't a MultiPolygon's"
		}
	default:
		return fmt.Sprintf("must be a Polygon or MultiPolygon, not %q", g.Type)
	}
	if len(polygons) == 0 {
		return "has no polygons"
	}
	for _, p := range polygons {
		if len(p) == 0 {
			return "has a polygon with no rings"
		}
		for _, ring := range p {
			if len(ring) < 4 {
				return "has a ring of fewer than 4 positions"
			}
			for _, pos := range ring {
				if len(pos) < 2 {
					return "has a position without a longitude and latitude"
				}
				if pos[0] < -180 || pos[0] > 180 || pos[1] < -90 || pos[1] > 90 {
					return fmt.Sprintf("has a position (%g, %g) outside WGS84 longitude and latitude", pos[0], pos[1])
				}
			}
		}
	}
	return ""
}
