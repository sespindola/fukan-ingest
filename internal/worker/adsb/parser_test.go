package adsb

import (
	"math"
	"os"
	"testing"

	"github.com/sespindola/fukan-ingest/internal/coord"
	"github.com/sespindola/fukan-ingest/internal/model"
)

func loadFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/opensky_response.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestParseStates(t *testing.T) {
	events, err := ParseStates(loadFixture(t), "opensky")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fixture has 4 states: index 1 is on-ground, index 2 has null coords → 2 events.
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	e := events[0]

	// AssetID uppercased.
	if e.AssetID != "3C6757" {
		t.Errorf("AssetID = %q, want %q", e.AssetID, "3C6757")
	}
	if e.AssetType != model.AssetAircraft {
		t.Errorf("AssetType = %q, want %q", e.AssetType, model.AssetAircraft)
	}
	if e.Source != "opensky" {
		t.Errorf("Source = %q, want %q", e.Source, "opensky")
	}

	// Timestamp: 1700000000 seconds → milliseconds.
	if e.Timestamp != 1700000000000 {
		t.Errorf("Timestamp = %d, want %d", e.Timestamp, 1700000000000)
	}

	// Coordinates scaled.
	if e.Lat != coord.ScaleLat(51.5074) {
		t.Errorf("Lat = %d, want %d", e.Lat, coord.ScaleLat(51.5074))
	}
	if e.Lon != coord.ScaleLon(-0.1278) {
		t.Errorf("Lon = %d, want %d", e.Lon, coord.ScaleLon(-0.1278))
	}

	// Altitude in meters (OpenSky already uses meters).
	if e.Alt != 10000 {
		t.Errorf("Alt = %d, want %d", e.Alt, 10000)
	}

	// Speed: 250 m/s → knots.
	wantSpeed := float32(250.0 * 1.94384)
	if math.Abs(float64(e.Speed-wantSpeed)) > 0.01 {
		t.Errorf("Speed = %f, want %f", e.Speed, wantSpeed)
	}

	// Heading.
	if e.Heading != 180.0 {
		t.Errorf("Heading = %f, want %f", e.Heading, 180.0)
	}

	// H3 cell computed.
	if e.H3Cell == 0 {
		t.Error("H3Cell = 0, want non-zero")
	}

	// Squawk metadata.
	if e.Metadata != `{"squawk":"7700"}` {
		t.Errorf("Metadata = %q, want %q", e.Metadata, `{"squawk":"7700"}`)
	}
}

func TestParseStates_NoSquawk(t *testing.T) {
	events, err := ParseStates(loadFixture(t), "opensky")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("got %d events, want at least 2", len(events))
	}
	// Second event (index 3 in fixture) has no squawk.
	if events[1].Metadata != "" {
		t.Errorf("Metadata = %q, want empty", events[1].Metadata)
	}
}

func TestParseStates_EmptyStates(t *testing.T) {
	body := []byte(`{"time":1700000000,"states":[]}`)
	events, err := ParseStates(body, "opensky")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}

func TestParseStates_NullStates(t *testing.T) {
	body := []byte(`{"time":1700000000,"states":null}`)
	events, err := ParseStates(body, "opensky")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}
