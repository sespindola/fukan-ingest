package coord

import (
	"fmt"
	"math"

	"github.com/uber/h3-go/v4"
)

// ScaleLat converts a float64 latitude to Int32 scaled by 10_000_000.
func ScaleLat(lat float64) int32 {
	return int32(math.Round(lat * 10_000_000))
}

// ScaleLon converts a float64 longitude to Int32 scaled by 10_000_000.
func ScaleLon(lon float64) int32 {
	return int32(math.Round(lon * 10_000_000))
}

// ComputeH3 returns the H3 cell index at resolution 7 for the given coordinates.
func ComputeH3(lat, lon float64) (uint64, error) {
	cell, err := h3.LatLngToCell(h3.LatLng{Lat: lat, Lng: lon}, 7)
	if err != nil {
		return 0, fmt.Errorf("h3 cell: %w", err)
	}
	return uint64(cell), nil
}
