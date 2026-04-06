package adsb

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sespindola/fukan-ingest/internal/coord"
	"github.com/sespindola/fukan-ingest/internal/model"
)

// openSkyResponse is the top-level JSON structure from OpenSky /states/all.
type openSkyResponse struct {
	Time   int64             `json:"time"`
	States []json.RawMessage `json:"states"`
}

// ParseStates parses an OpenSky /states/all response body into FukanEvents.
// Aircraft that are on_ground or have null lat/lon are skipped.
func ParseStates(body []byte, source string) ([]model.FukanEvent, error) {
	var resp openSkyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse opensky response: %w", err)
	}

	tsMillis := resp.Time * 1000

	events := make([]model.FukanEvent, 0, len(resp.States))
	for _, raw := range resp.States {
		var state []any
		if err := json.Unmarshal(raw, &state); err != nil {
			continue
		}
		if len(state) < 17 {
			continue
		}

		// Index 8: on_ground — skip grounded aircraft.
		if onGround, ok := state[8].(bool); ok && onGround {
			continue
		}

		// Index 6: latitude, Index 5: longitude — skip null positions.
		lat, latOK := toFloat64(state[6])
		lon, lonOK := toFloat64(state[5])
		if !latOK || !lonOK {
			continue
		}

		// Index 0: icao24.
		icao24, _ := state[0].(string)
		if icao24 == "" {
			continue
		}

		// Index 7: baro_altitude (meters).
		alt, _ := toFloat64(state[7])

		// Index 9: velocity (m/s → knots).
		velocity, _ := toFloat64(state[9])
		speedKnots := float32(velocity * 1.94384)

		// Index 10: true_track (degrees).
		heading, _ := toFloat64(state[10])

		// Index 14: squawk.
		var metadata string
		if squawk, ok := state[14].(string); ok && squawk != "" {
			b, _ := json.Marshal(map[string]string{"squawk": squawk})
			metadata = string(b)
		}

		h3cell, err := coord.ComputeH3(lat, lon)
		if err != nil {
			continue
		}

		event := model.FukanEvent{
			Timestamp: tsMillis,
			AssetID:   strings.ToUpper(icao24),
			AssetType: model.AssetAircraft,
			Lat:       coord.ScaleLat(lat),
			Lon:       coord.ScaleLon(lon),
			Alt:       int32(alt),
			Speed:     speedKnots,
			Heading:   float32(heading),
			H3Cell:    h3cell,
			Source:    source,
			Metadata:  metadata,
		}

		if err := model.Validate(event); err != nil {
			continue
		}

		events = append(events, event)
	}
	return events, nil
}

// toFloat64 safely extracts a float64 from a JSON-decoded any value.
// JSON numbers decode as float64 in Go; returns (0, false) for nil.
func toFloat64(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}
