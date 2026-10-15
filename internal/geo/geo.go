// Package geo holds the coordinate maths for locations (R24-R26): UTM on
// the WGS84 ellipsoid, distances, and completing a 6-digit grid reference
// from the 100 km square its locality lies in. It is pure: no I/O.
package geo

import (
	"math"
	"strings"
)

// Point is a WGS84 longitude and latitude in degrees.
type Point struct{ Lon, Lat float64 }

// Extent is a bounding box in degrees.
type Extent struct{ MinLon, MinLat, MaxLon, MaxLat float64 }

// Centre is the middle of the box.
func (x Extent) Centre() Point { return Point{(x.MinLon + x.MaxLon) / 2, (x.MinLat + x.MaxLat) / 2} }

// Contains reports whether p is inside the box (edges included).
func (x Extent) Contains(p Point) bool {
	return p.Lon >= x.MinLon && p.Lon <= x.MaxLon && p.Lat >= x.MinLat && p.Lat <= x.MaxLat
}

const earthRadius = 6371008.8 // mean radius, metres

// Distance is the great-circle distance in metres. Over the few kilometres
// R26 compares, it is within 0.5 % of the ellipsoidal distance.
func Distance(a, b Point) float64 {
	φ1, φ2 := rad(a.Lat), rad(b.Lat)
	dφ, dλ := φ2-φ1, rad(b.Lon-a.Lon)
	h := math.Sin(dφ/2)*math.Sin(dφ/2) + math.Cos(φ1)*math.Cos(φ2)*math.Sin(dλ/2)*math.Sin(dλ/2)
	return 2 * earthRadius * math.Asin(math.Min(1, math.Sqrt(h)))
}

// distanceToExtent is 0 inside the box, otherwise the distance to its
// nearest edge.
func distanceToExtent(p Point, x Extent) float64 {
	q := Point{math.Max(x.MinLon, math.Min(p.Lon, x.MaxLon)), math.Max(x.MinLat, math.Min(p.Lat, x.MaxLat))}
	return Distance(p, q)
}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

// ZoneOf is the standard UTM zone (1-60) for a longitude. The Norway and
// Svalbard exceptions aren't applied.
func ZoneOf(lon float64) int {
	z := int(math.Floor((lon+180)/6)) + 1
	return min(max(z, 1), 60)
}

// zoneBounds is a zone's longitude range.
func zoneBounds(zone int) (float64, float64) {
	w := float64(zone-1)*6 - 180
	return w, w + 6
}

// WGS84 and UTM constants, and the Krüger series coefficients (Karney 2011)
// to third order in n, which is accurate to well under a millimetre.
const (
	wgsA  = 6378137.0
	wgsF  = 1 / 298.257223563
	utmK0 = 0.9996
	utmE0 = 500000.0
	utmN0 = 10000000.0 // false northing, southern hemisphere
)

var (
	n3   = wgsF / (2 - wgsF)
	bigA = wgsA / (1 + n3) * (1 + n3*n3/4 + n3*n3*n3*n3/64)
	α    = [3]float64{n3/2 - 2*n3*n3/3 + 5*n3*n3*n3/16, 13*n3*n3/48 - 3*n3*n3*n3/5, 61 * n3 * n3 * n3 / 240}
	β    = [3]float64{n3/2 - 2*n3*n3/3 + 37*n3*n3*n3/96, n3*n3/48 + n3*n3*n3/15, 17 * n3 * n3 * n3 / 480}
	δ    = [3]float64{2*n3 - 2*n3*n3/3 - 2*n3*n3*n3, 7*n3*n3/3 - 8*n3*n3*n3/5, 56 * n3 * n3 * n3 / 15}
)

// ToUTM converts p to easting and northing in the given zone. Southern
// latitudes get the 10,000 km false northing.
func ToUTM(p Point, zone int) (e, n float64) {
	λ0 := rad(float64(zone-1)*6 - 180 + 3)
	φ, λ := rad(p.Lat), rad(p.Lon)-λ0
	sq := 2 * math.Sqrt(n3) / (1 + n3)
	t := math.Sinh(math.Atanh(math.Sin(φ)) - sq*math.Atanh(sq*math.Sin(φ)))
	ξʹ := math.Atan2(t, math.Cos(λ))
	ηʹ := math.Atanh(math.Sin(λ) / math.Sqrt(1+t*t))
	ξ, η := ξʹ, ηʹ
	for j := 1; j <= 3; j++ {
		ξ += α[j-1] * math.Sin(2*float64(j)*ξʹ) * math.Cosh(2*float64(j)*ηʹ)
		η += α[j-1] * math.Cos(2*float64(j)*ξʹ) * math.Sinh(2*float64(j)*ηʹ)
	}
	e = utmE0 + utmK0*bigA*η
	n = utmK0 * bigA * ξ
	if p.Lat < 0 {
		n += utmN0
	}
	return e, n
}

// FromUTM converts an easting and northing in a zone back to a point.
func FromUTM(zone int, south bool, e, n float64) Point {
	if south {
		n -= utmN0
	}
	ξ := n / (utmK0 * bigA)
	η := (e - utmE0) / (utmK0 * bigA)
	ξʹ, ηʹ := ξ, η
	for j := 1; j <= 3; j++ {
		ξʹ -= β[j-1] * math.Sin(2*float64(j)*ξ) * math.Cosh(2*float64(j)*η)
		ηʹ -= β[j-1] * math.Cos(2*float64(j)*ξ) * math.Sinh(2*float64(j)*η)
	}
	χ := math.Asin(math.Sin(ξʹ) / math.Cosh(ηʹ))
	φ := χ
	for j := 1; j <= 3; j++ {
		φ += δ[j-1] * math.Sin(2*float64(j)*χ)
	}
	λ0 := float64(zone-1)*6 - 180 + 3
	return Point{Lon: λ0 + deg(math.Atan2(math.Sinh(ηʹ), math.Cos(ξʹ))), Lat: deg(φ)}
}

// GridSystem names how a network's grid references are read (R5).
const GridSystem6InLocalitySquare = "mgrs-6-digit-in-locality-square"

// CompleteGrid reads a 6-digit grid reference (3 digits of easting, then 3
// of northing, in 100 m) without zone or 100 km square, and completes it
// from the zone and 100 km square containing the locality (R25). It gives
// the centre of the 100 m cell (section 14).
//
// The candidate squares are those the locality's extent overlaps, in every
// zone it overlaps. A candidate whose point falls outside its own zone is
// discarded (MGRS squares stop at zone edges). If more than one remains,
// the one nearest the extent wins, then the one nearest its centre.
func CompleteGrid(ref string, loc Extent) (Point, bool) {
	ref = strings.Join(strings.Fields(ref), "")
	if len(ref) != 6 || strings.Trim(ref, "0123456789") != "" {
		return Point{}, false
	}
	de := float64(atoi(ref[:3]))*100 + 50
	dn := float64(atoi(ref[3:]))*100 + 50
	centre := loc.Centre()
	south := centre.Lat < 0
	corners := []Point{{loc.MinLon, loc.MinLat}, {loc.MinLon, loc.MaxLat}, {loc.MaxLon, loc.MinLat}, {loc.MaxLon, loc.MaxLat}}

	var best Point
	found, bestEdge, bestCentre := false, 0.0, 0.0
	for zone := ZoneOf(loc.MinLon); zone <= ZoneOf(loc.MaxLon); zone++ {
		minE, minN, maxE, maxN := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, c := range corners {
			e, n := ToUTM(c, zone)
			if (c.Lat < 0) != south { // keep one hemisphere's northings
				if south {
					n += utmN0
				} else {
					n -= utmN0
				}
			}
			minE, maxE = math.Min(minE, e), math.Max(maxE, e)
			minN, maxN = math.Min(minN, n), math.Max(maxN, n)
		}
		west, east := zoneBounds(zone)
		for be := math.Floor(minE / 1e5); be <= math.Floor(maxE/1e5); be++ {
			for bn := math.Floor(minN / 1e5); bn <= math.Floor(maxN/1e5); bn++ {
				p := FromUTM(zone, south, be*1e5+de, bn*1e5+dn)
				if p.Lon < west || p.Lon >= east {
					continue
				}
				edge, mid := distanceToExtent(p, loc), Distance(p, centre)
				if !found || edge < bestEdge || (edge == bestEdge && mid < bestCentre) {
					best, found, bestEdge, bestCentre = p, true, edge, mid
				}
			}
		}
	}
	return best, found
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}
