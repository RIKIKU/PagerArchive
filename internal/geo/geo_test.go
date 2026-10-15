package geo

import (
	"math"
	"testing"
)

// Reference values from PROJ (pyproj, WGS84 to EPSG:326xx/327xx) and the
// mgrs package, generated 2026-10-13. Neutral places only: open ocean,
// Greenwich, the equator and the antimeridian.
var utmCases = []struct {
	lat, lon float64
	zone     int
	e, n     float64
	mgrs     string
}{
	{-34.81, -140.865, 7, 512347.385, 6148018.579, "07HEB123480"},
	{-35.0, -138.01, 7, 772885.150, 6122870.706, "07HGB728228"},
	{-35.0, -137.99, 8, 227114.850, 6122870.706, "08HKG271228"},
	{51.4778, -0.0014, 30, 708220.893, 5707224.827, "30UYC082072"},
	{0.5, 10.0, 32, 611276.441, 55273.512, "32NPF112552"},
	{-60.2, -170.3, 2, 538807.983, 3326109.206, "02ENU388261"},
	{45.0, 179.9, 60, 728564.486, 4987042.307, "60TYQ285870"},
}

func TestUTMRoundTrip(t *testing.T) {
	for _, c := range utmCases {
		p := Point{c.lon, c.lat}
		if z := ZoneOf(c.lon); z != c.zone {
			t.Errorf("ZoneOf(%v) = %d, want %d", c.lon, z, c.zone)
		}
		e, n := ToUTM(p, c.zone)
		if math.Abs(e-c.e) > 0.01 || math.Abs(n-c.n) > 0.01 {
			t.Errorf("ToUTM(%v) = %.3f %.3f, want %.3f %.3f", p, e, n, c.e, c.n)
		}
		back := FromUTM(c.zone, c.lat < 0, c.e, c.n)
		if d := Distance(back, p); d > 0.01 {
			t.Errorf("FromUTM(%d, %.3f, %.3f) = %v, %.4f m from %v", c.zone, c.e, c.n, back, d, p)
		}
	}
}

func TestDistance(t *testing.T) {
	// One degree of latitude is about 111.2 km on the mean sphere.
	if d := Distance(Point{0, 0}, Point{0, 1}); math.Abs(d-111195) > 5 {
		t.Errorf("1 degree = %.0f m", d)
	}
	if d := Distance(Point{10, 20}, Point{10, 20}); d != 0 {
		t.Errorf("same point = %v", d)
	}
}

func box(p Point, half float64) Extent {
	return Extent{p.Lon - half, p.Lat - half, p.Lon + half, p.Lat + half}
}

func TestCompleteGrid(t *testing.T) {
	for _, c := range utmCases {
		p := Point{c.lon, c.lat}
		ref := c.mgrs[5:] // the 6 digits, without zone and square
		got, ok := CompleteGrid(ref, box(p, 0.01))
		if !ok {
			t.Errorf("%s: not completed", c.mgrs)
			continue
		}
		// The centre of the 100 m cell is at most ~71 m from any point in it.
		if d := Distance(got, p); d > 71 {
			t.Errorf("%s: %v is %.0f m from %v", c.mgrs, got, d, p)
		}
	}
}

func TestCompleteGridAcrossZoneBoundary(t *testing.T) {
	// A locality straddling the zone 7/8 boundary at 138° W: each reference
	// resolves in the zone where its point actually lies.
	loc := Extent{-138.02, -35.01, -137.98, -34.99}
	for ref, want := range map[string]Point{"728228": {-138.01, -35.0}, "271228": {-137.99, -35.0}} {
		got, ok := CompleteGrid(ref, loc)
		if !ok || Distance(got, want) > 71 {
			t.Errorf("%s: got %v %v, want near %v", ref, got, ok, want)
		}
	}
}

func TestCompleteGridFarSideOfSquare(t *testing.T) {
	// A reference far from the locality still resolves in the locality's
	// square (R25), so a disagreeing address can be flagged (R26).
	p := Point{-140.865, -34.81} // 07HEB123480
	got, ok := CompleteGrid("999999", box(p, 0.01))
	e, n := ToUTM(got, 7)
	if !ok || math.Abs(e-599950) > 0.5 || math.Abs(n-6199950) > 0.5 {
		t.Errorf("got %v (%.1f %.1f) %v, want E 599950 N 6199950", got, e, n, ok)
	}
}

func TestCompleteGridRejectsMalformed(t *testing.T) {
	loc := box(Point{-140.865, -34.81}, 0.01)
	for _, ref := range []string{"", "12345", "1234567", "12A456", "123 45"} {
		if _, ok := CompleteGrid(ref, loc); ok {
			t.Errorf("%q completed", ref)
		}
	}
	if _, ok := CompleteGrid("123 480", loc); !ok {
		t.Error(`"123 480" (inner space) not completed`)
	}
}
