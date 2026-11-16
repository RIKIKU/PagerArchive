package areas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadExample(t *testing.T) {
	f, err := ReadFile("../../profiles/example/example-areas.geojson", "zone")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Areas) != 2 || f.Areas[0].Name != "A" || f.Areas[1].Name != "B" || len(f.Hash) != 64 {
		t.Errorf("the example's areas: %+v", f.Areas)
	}
	// Another name property is another set of areas: worked out again.
	if _, err := ReadFile("../../profiles/example/example-areas.geojson", "district"); err == nil {
		t.Error("a name property no feature has was accepted")
	}
}

// R88: what an areas file may be, and what's refused, naming the file and
// where.
func TestReadFileChecks(t *testing.T) {
	square := `{"type": "Polygon", "coordinates": [[[1, 1], [2, 1], [2, 2], [1, 2], [1, 1]]]}`
	feature := func(props, geom string) string {
		return `{"type": "Feature", "properties": ` + props + `, "geometry": ` + geom + `}`
	}
	collection := func(features ...string) string {
		return `{"type": "FeatureCollection", "features": [` + strings.Join(features, ",") + `]}`
	}
	dir := t.TempDir()
	read := func(doc string) (*File, error) {
		t.Helper()
		path := filepath.Join(dir, "zones.geojson")
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		return ReadFile(path, "zone")
	}

	// A number names an area as the file writes it; features of one name are
	// one area; a crs naming WGS84 is fine.
	f, err := read(`{"type": "FeatureCollection", "crs": {"type": "name", "properties": {"name": "urn:ogc:def:crs:EPSG::4326"}}, "features": [` +
		feature(`{"zone": 8}`, square) + "," + feature(`{"zone": " north "}`, square) + "," + feature(`{"zone": "north"}`, square) + `]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Areas) != 2 || f.Areas[0].Name != "8" || f.Areas[1].Name != "north" || len(f.Areas[1].Geometries) != 2 {
		t.Errorf("areas: %+v", f.Areas)
	}

	for doc, want := range map[string]string{
		`[]`:                  "not valid JSON",
		`{"type": "Feature"}`: `zones.geojson: at /type: must be "FeatureCollection"`,
		collection():          "at /features: has no features",
		`{"type": "FeatureCollection", "crs": {"properties": {"name": "EPSG:28355"}}, "features": [` + feature(`{"zone": "a"}`, square) + `]}`: "at /crs: coordinates must be WGS84",
		collection(feature(`{"name": "a"}`, square)):                                                                                   "at /features/0/properties/zone: must be a string or a number",
		collection(feature(`{"zone": ""}`, square)):                                                                                    "at /features/0/properties/zone: is empty",
		collection(feature(`{"zone": "`+strings.Repeat("x", MaxName+1)+`"}`, square)):                                                  "is longer than 40 characters",
		collection(feature(`{"zone": true}`, square)):                                                                                  "must be a string or a number",
		collection(feature(`{"zone": "a"}`, `{"type": "LineString", "coordinates": [[1, 1], [2, 2]]}`)):                                `at /features/0/geometry: must be a Polygon or MultiPolygon, not "LineString"`,
		collection(feature(`{"zone": "a"}`, `null`)):                                                                                   "must be a Polygon or MultiPolygon",
		collection(feature(`{"zone": "a"}`, `{"type": "Polygon", "coordinates": [[[1, 1], [2, 1], [1, 1]]]}`)):                         "fewer than 4 positions",
		collection(feature(`{"zone": "a"}`, `{"type": "Polygon", "coordinates": [[[145, -38], [146, -38], [146, 100], [145, -38]]]}`)): "outside WGS84",
		collection(feature(`{"zone": "a"}`, square), feature(`{"zone": "b"}`, `{"type": "MultiPolygon", "coordinates": []}`)):          "at /features/1/geometry: has no polygons",
	} {
		_, err := read(doc)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", doc, err, want)
		}
	}
}
