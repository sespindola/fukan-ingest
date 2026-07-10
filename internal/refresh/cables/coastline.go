package cables

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sync"
)

// LandingBufferKm: a baseline landing must be within this distance of any
// coastline edge to be accepted. 10 km admits real cable landing stations
// that sit a few km inland (Goonhilly Downs is ~11 km from the beach) while
// rejecting country/region centroids.
const LandingBufferKm = 10.0

// RouteBufferKm: the interior of a cable polyline must stay at least this
// far from land. Cables hug coastlines closely; 20 km is a permissive but
// useful gate against routes drawn straight through continents.
const RouteBufferKm = 20.0

// LandingApproachKm: nodes on a polyline within this distance of either
// endpoint are exempt from the on-land check (the cable is approaching its
// landing PoP and may legitimately cross beach/onshore segments).
const LandingApproachKm = 25.0

//go:embed data/coastline_50m.json
var embeddedCoastlineGeoJSON []byte

// landPolygon is a single Natural Earth land polygon.
type landPolygon struct {
	outer  []float64 // alternating lon, lat (GeoJSON order, degrees)
	holes  [][]float64
	minLon float64
	minLat float64
	maxLon float64
	maxLat float64
}

var (
	coastlinePolys []*landPolygon
	coastlineErr   error
	coastlineOnce  sync.Once
)

func loadCoastline() ([]*landPolygon, error) {
	coastlineOnce.Do(func() {
		coastlinePolys, coastlineErr = parseCoastlineGeoJSON(embeddedCoastlineGeoJSON)
	})
	return coastlinePolys, coastlineErr
}

type geoJSON struct {
	Type     string `json:"type"`
	Features []struct {
		Geometry struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

func parseCoastlineGeoJSON(raw []byte) ([]*landPolygon, error) {
	var doc geoJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse coastline GeoJSON: %w", err)
	}
	out := make([]*landPolygon, 0, len(doc.Features))
	for _, f := range doc.Features {
		switch f.Geometry.Type {
		case "Polygon":
			var rings [][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &rings); err != nil {
				return nil, fmt.Errorf("parse polygon: %w", err)
			}
			out = append(out, buildLandPolygon(rings))
		case "MultiPolygon":
			var polys [][][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &polys); err != nil {
				return nil, fmt.Errorf("parse multipolygon: %w", err)
			}
			for _, rings := range polys {
				out = append(out, buildLandPolygon(rings))
			}
		}
	}
	return out, nil
}

func buildLandPolygon(rings [][][2]float64) *landPolygon {
	p := &landPolygon{
		minLon: math.Inf(1),
		minLat: math.Inf(1),
		maxLon: math.Inf(-1),
		maxLat: math.Inf(-1),
	}
	for i, ring := range rings {
		flat := make([]float64, 0, len(ring)*2)
		for _, pt := range ring {
			lon, lat := pt[0], pt[1]
			flat = append(flat, lon, lat)
			if i == 0 {
				if lon < p.minLon {
					p.minLon = lon
				}
				if lon > p.maxLon {
					p.maxLon = lon
				}
				if lat < p.minLat {
					p.minLat = lat
				}
				if lat > p.maxLat {
					p.maxLat = lat
				}
			}
		}
		if i == 0 {
			p.outer = flat
		} else {
			p.holes = append(p.holes, flat)
		}
	}
	return p
}

// IsLandingCoord reports whether (lat, lon) is within LandingBufferKm of any
// coastline edge. Real cable landing stations qualify; country centroids do
// not.
func IsLandingCoord(lat, lon float64) bool {
	polys, err := loadCoastline()
	if err != nil {
		// Fail-safe: if coastline data isn't available, accept everything.
		// The Python builder's filters are the primary defense; this is
		// belt-and-suspenders.
		return true
	}
	bufDeg := kmToLatDeg(LandingBufferKm) + 0.05 // padding for bbox prefilter
	for _, poly := range polys {
		if !poly.bboxIntersectsCircle(lat, lon, bufDeg) {
			continue
		}
		if minDistanceToPolyEdgeKm(lat, lon, poly) <= LandingBufferKm {
			return true
		}
	}
	return false
}

// IsOnLand reports whether (lat, lon) lies inside any land polygon (with
// hole subtraction for inland water bodies).
func IsOnLand(lat, lon float64) bool {
	polys, err := loadCoastline()
	if err != nil {
		return false
	}
	for _, poly := range polys {
		if !poly.bboxContains(lat, lon) {
			continue
		}
		if pointInRing(lat, lon, poly.outer) {
			inHole := false
			for _, hole := range poly.holes {
				if pointInRing(lat, lon, hole) {
					inHole = true
					break
				}
			}
			if !inHole {
				return true
			}
		}
	}
	return false
}

// PolylineClearsLand validates that a cable polyline (alternating Int32 lat,
// lon scaled by CoordScale) stays off land, except within LandingApproachKm
// of either endpoint where the cable approaches its landing PoP.
//
// Returns true if every interior node is in the ocean (or within
// LandingApproachKm of an endpoint). Returns false if any non-approach node
// falls inside a land polygon.
func PolylineClearsLand(coords []int32) bool {
	if len(coords) < 4 {
		return false
	}
	n := len(coords) / 2
	lat0 := DescaleCoord(coords[0])
	lon0 := DescaleCoord(coords[1])
	latN := DescaleCoord(coords[2*(n-1)])
	lonN := DescaleCoord(coords[2*(n-1)+1])

	for i := 0; i < n; i++ {
		lat := DescaleCoord(coords[2*i])
		lon := DescaleCoord(coords[2*i+1])
		// Endpoint approach buffers — cables legitimately cross land here
		// to reach landing stations.
		if haversineKm(lat, lon, lat0, lon0) <= LandingApproachKm {
			continue
		}
		if haversineKm(lat, lon, latN, lonN) <= LandingApproachKm {
			continue
		}
		if IsOnLand(lat, lon) {
			return false
		}
	}
	return true
}

// SegmentClearsLand reports whether the great-circle segment between two
// points stays off land except within LandingApproachKm of either endpoint.
// Used by tests; the production path uses PolylineClearsLand directly on the
// densely-sampled OSM coordinate list.
func SegmentClearsLand(lat1, lon1, lat2, lon2 float64) bool {
	distKm := haversineKm(lat1, lon1, lat2, lon2)
	if distKm <= 0 {
		return true
	}
	// Sample roughly every ~10 km, with a minimum of 20 samples.
	nSamples := int(math.Max(20, distKm/10))
	for i := 1; i < nSamples; i++ {
		f := float64(i) / float64(nSamples)
		lat, lon := interpolateGreatCircleDeg(lat1, lon1, lat2, lon2, f)
		if haversineKm(lat, lon, lat1, lon1) <= LandingApproachKm {
			continue
		}
		if haversineKm(lat, lon, lat2, lon2) <= LandingApproachKm {
			continue
		}
		if IsOnLand(lat, lon) {
			return false
		}
	}
	return true
}

// CoastlinePolygonCount returns how many parsed land polygons the coastline
// data contains (used by load-time logging).
func CoastlinePolygonCount() int {
	polys, err := loadCoastline()
	if err != nil {
		return 0
	}
	return len(polys)
}

// --- geometry helpers ---

func (p *landPolygon) bboxContains(lat, lon float64) bool {
	return lon >= p.minLon && lon <= p.maxLon && lat >= p.minLat && lat <= p.maxLat
}

func (p *landPolygon) bboxIntersectsCircle(lat, lon, padDeg float64) bool {
	return lon >= p.minLon-padDeg && lon <= p.maxLon+padDeg &&
		lat >= p.minLat-padDeg && lat <= p.maxLat+padDeg
}

// pointInRing implements ray-casting point-in-polygon for a flat
// [lon0, lat0, lon1, lat1, ...] ring (GeoJSON order).
func pointInRing(lat, lon float64, ring []float64) bool {
	n := len(ring) / 2
	if n < 3 {
		return false
	}
	inside := false
	j := n - 1
	for i := 0; i < n; i++ {
		yi := ring[2*i+1]
		xi := ring[2*i]
		yj := ring[2*j+1]
		xj := ring[2*j]
		if ((yi > lat) != (yj > lat)) &&
			lon < (xj-xi)*(lat-yi)/(yj-yi+1e-12)+xi {
			inside = !inside
		}
		j = i
	}
	return inside
}

// minDistanceToPolyEdgeKm computes the shortest haversine distance from
// (lat, lon) to any edge of the polygon's outer ring (holes are inland
// water — not "coastline" for the purpose of accepting landings).
func minDistanceToPolyEdgeKm(lat, lon float64, poly *landPolygon) float64 {
	best := math.Inf(1)
	ring := poly.outer
	n := len(ring) / 2
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		lat1 := ring[2*i+1]
		lon1 := ring[2*i]
		lat2 := ring[2*j+1]
		lon2 := ring[2*j]
		d := pointToSegmentKm(lat, lon, lat1, lon1, lat2, lon2)
		if d < best {
			best = d
		}
	}
	return best
}

// pointToSegmentKm approximates the great-circle distance from a point to a
// great-circle segment. For the buffer scales we care about (10-25 km),
// projecting to a local equirectangular plane is accurate enough.
func pointToSegmentKm(lat, lon, lat1, lon1, lat2, lon2 float64) float64 {
	const earthR = 6371.0
	// Convert to radians and project on a local plane centered at lat1, lon1.
	cosLat := math.Cos(lat1 * math.Pi / 180)
	x0 := (lon - lon1) * cosLat
	y0 := lat - lat1
	x1 := 0.0
	y1 := 0.0
	x2 := (lon2 - lon1) * cosLat
	y2 := lat2 - lat1
	dx := x2 - x1
	dy := y2 - y1
	denom := dx*dx + dy*dy
	if denom == 0 {
		// Degenerate segment — fall back to haversine to the endpoint.
		return haversineKm(lat, lon, lat1, lon1)
	}
	t := ((x0-x1)*dx + (y0-y1)*dy) / denom
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	px := x1 + t*dx
	py := y1 + t*dy
	deg := math.Sqrt((x0-px)*(x0-px) + (y0-py)*(y0-py))
	return deg * math.Pi / 180 * earthR
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthR = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(phi1)*math.Cos(phi2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthR * c
}

func kmToLatDeg(km float64) float64 {
	return km / 111.0
}

// interpolateGreatCircleDeg returns the point at fraction f along the
// great-circle arc from (lat1, lon1) to (lat2, lon2). f=0 → endpoint 1,
// f=1 → endpoint 2.
func interpolateGreatCircleDeg(lat1, lon1, lat2, lon2, f float64) (float64, float64) {
	phi1 := lat1 * math.Pi / 180
	lambda1 := lon1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	lambda2 := lon2 * math.Pi / 180

	d := 2 * math.Asin(math.Sqrt(
		math.Sin((phi2-phi1)/2)*math.Sin((phi2-phi1)/2)+
			math.Cos(phi1)*math.Cos(phi2)*
				math.Sin((lambda2-lambda1)/2)*math.Sin((lambda2-lambda1)/2),
	))
	if d == 0 {
		return lat1, lon1
	}
	A := math.Sin((1-f)*d) / math.Sin(d)
	B := math.Sin(f*d) / math.Sin(d)
	x := A*math.Cos(phi1)*math.Cos(lambda1) + B*math.Cos(phi2)*math.Cos(lambda2)
	y := A*math.Cos(phi1)*math.Sin(lambda1) + B*math.Cos(phi2)*math.Sin(lambda2)
	z := A*math.Sin(phi1) + B*math.Sin(phi2)
	phi := math.Atan2(z, math.Sqrt(x*x+y*y))
	lambda := math.Atan2(y, x)
	return phi * 180 / math.Pi, lambda * 180 / math.Pi
}
