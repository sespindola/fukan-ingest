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

// openskyCategory maps the OpenSky integer category (index 16) to a label.
// See: https://openskynetwork.github.io/opensky-api/rest.html#all-state-vectors
var openskyCategory = [...]string{
	0:  "none",
	1:  "none",
	2:  "light",
	3:  "small",
	4:  "large",
	5:  "high_vortex_large",
	6:  "heavy",
	7:  "high_performance",
	8:  "rotorcraft",
	9:  "glider",
	10: "lighter_than_air",
	11: "parachutist",
	12: "ultralight",
	13: "reserved",
	14: "uav",
	15: "space",
	16: "surface_emergency",
	17: "surface_service",
}

// ParseStates parses an OpenSky /states/all response body into FukanEvents.
// Aircraft with null lat/lon are skipped.
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

		// Index 1: callsign.
		callsign, _ := state[1].(string)
		callsign = strings.TrimSpace(callsign)

		// Index 2: origin_country.
		origin, _ := state[2].(string)

		// Index 7: baro_altitude (meters).
		alt, _ := toFloat64(state[7])

		// Index 9: velocity (m/s → knots).
		velocity, _ := toFloat64(state[9])
		speedKnots := float32(velocity * 1.94384)

		// Index 10: true_track (degrees).
		heading, _ := toFloat64(state[10])

		// Index 11: vertical_rate (m/s).
		verticalRate, _ := toFloat64(state[11])

		// Index 14: squawk.
		var squawk string
		if s, ok := state[14].(string); ok {
			squawk = s
		}

		// Index 16: category.
		var category string
		if catVal, ok := toFloat64(state[16]); ok {
			idx := int(catVal)
			if idx >= 0 && idx < len(openskyCategory) {
				category = openskyCategory[idx]
			}
		}

		h3cell, err := coord.ComputeH3(lat, lon)
		if err != nil {
			continue
		}

		event := model.FukanEvent{
			Timestamp:    tsMillis,
			AssetID:      strings.ToUpper(icao24),
			AssetType:    model.AssetAircraft,
			Callsign:     callsign,
			Origin:       origin,
			Category:     category,
			Lat:          coord.ScaleLat(lat),
			Lon:          coord.ScaleLon(lon),
			Alt:          int32(alt),
			Speed:        speedKnots,
			Heading:       float32(heading),
			VerticalRate: float32(verticalRate),
			H3Cell:       h3cell,
			Source:        source,
			Squawk:       squawk,
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
