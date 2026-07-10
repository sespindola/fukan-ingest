package cables

import "math"

// ComputeBBox returns the [min_lat, min_lon, max_lat, max_lon] of a flat
// Int32 lat/lon polyline in CoordScale units. Used by the OSM parser to
// stamp each way's bbox.
func ComputeBBox(coords []int32) (minLat, minLon, maxLat, maxLon int32) {
	if len(coords) < 2 {
		return 0, 0, 0, 0
	}
	minLat = int32(math.MaxInt32)
	minLon = int32(math.MaxInt32)
	maxLat = int32(math.MinInt32)
	maxLon = int32(math.MinInt32)
	for i := 0; i+1 < len(coords); i += 2 {
		lat, lon := coords[i], coords[i+1]
		if lat < minLat {
			minLat = lat
		}
		if lat > maxLat {
			maxLat = lat
		}
		if lon < minLon {
			minLon = lon
		}
		if lon > maxLon {
			maxLon = lon
		}
	}
	return
}
