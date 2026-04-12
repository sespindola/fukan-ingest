package ais

import (
	"math"
	"os"
	"testing"

	"github.com/sespindola/fukan-ingest/internal/coord"
	"github.com/sespindola/fukan-ingest/internal/model"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestParseMessage_PositionReport(t *testing.T) {
	event, ok := ParseMessage(loadFixture(t, "position_report.json"), "aisstream")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if event.AssetID != "211234560" {
		t.Errorf("AssetID = %q, want %q", event.AssetID, "211234560")
	}
	if event.AssetType != model.AssetVessel {
		t.Errorf("AssetType = %q, want %q", event.AssetType, model.AssetVessel)
	}
	if event.Callsign != "EVER GIVEN" {
		t.Errorf("Callsign = %q, want %q", event.Callsign, "EVER GIVEN")
	}
	if event.Source != "aisstream" {
		t.Errorf("Source = %q, want %q", event.Source, "aisstream")
	}

	// Timestamp: 2026-04-12T10:00:00Z
	if event.Timestamp != 1775988000000 {
		t.Errorf("Timestamp = %d, want %d", event.Timestamp, 1775988000000)
	}

	// Coordinates scaled.
	if event.Lat != coord.ScaleLat(31.5) {
		t.Errorf("Lat = %d, want %d", event.Lat, coord.ScaleLat(31.5))
	}
	if event.Lon != coord.ScaleLon(32.3) {
		t.Errorf("Lon = %d, want %d", event.Lon, coord.ScaleLon(32.3))
	}

	// Speed in knots (AISStream provides decoded value).
	if math.Abs(float64(event.Speed-12.5)) > 0.01 {
		t.Errorf("Speed = %f, want %f", event.Speed, 12.5)
	}

	// TrueHeading=127 should be used over Cog=125.7.
	if event.Heading != 127.0 {
		t.Errorf("Heading = %f, want %f", event.Heading, 127.0)
	}

	if event.NavStatus != "under_way_using_engine" {
		t.Errorf("NavStatus = %q, want %q", event.NavStatus, "under_way_using_engine")
	}

	if math.Abs(float64(event.RateOfTurn-5.0)) > 0.01 {
		t.Errorf("RateOfTurn = %f, want %f", event.RateOfTurn, 5.0)
	}

	if event.H3Cell == 0 {
		t.Error("H3Cell = 0, want non-zero")
	}

	// Altitude always 0 for vessels.
	if event.Alt != 0 {
		t.Errorf("Alt = %d, want 0", event.Alt)
	}
}

func TestParseMessage_ClassBPosition(t *testing.T) {
	event, ok := ParseMessage(loadFixture(t, "class_b_position_report.json"), "aisstream")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if event.AssetID != "338123456" {
		t.Errorf("AssetID = %q, want %q", event.AssetID, "338123456")
	}
	if event.Callsign != "SAILING VESSEL" {
		t.Errorf("Callsign = %q, want %q", event.Callsign, "SAILING VESSEL")
	}

	if math.Abs(float64(event.Speed-6.2)) > 0.01 {
		t.Errorf("Speed = %f, want %f", event.Speed, 6.2)
	}

	// TrueHeading=511 means "not available", should fall back to Cog=270.5.
	if math.Abs(float64(event.Heading-270.5)) > 0.1 {
		t.Errorf("Heading = %f, want %f (fallback to Cog)", event.Heading, 270.5)
	}

	if event.Lat != coord.ScaleLat(40.7128) {
		t.Errorf("Lat = %d, want %d", event.Lat, coord.ScaleLat(40.7128))
	}
}

func TestParseMessage_ShipStaticData(t *testing.T) {
	event, ok := ParseMessage(loadFixture(t, "ship_static_data.json"), "aisstream")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if event.AssetID != "477123456" {
		t.Errorf("AssetID = %q, want %q", event.AssetID, "477123456")
	}
	if event.Callsign != "MAERSK SEALAND" {
		t.Errorf("Callsign = %q, want %q", event.Callsign, "MAERSK SEALAND")
	}
	if event.IMONumber != 9321483 {
		t.Errorf("IMONumber = %d, want %d", event.IMONumber, 9321483)
	}
	if event.ShipType != "cargo" {
		t.Errorf("ShipType = %q, want %q", event.ShipType, "cargo")
	}
	if event.Destination != "SINGAPORE" {
		t.Errorf("Destination = %q, want %q", event.Destination, "SINGAPORE")
	}
	if math.Abs(float64(event.Draught-14.5)) > 0.01 {
		t.Errorf("Draught = %f, want %f", event.Draught, 14.5)
	}
	if event.DimA != 200 {
		t.Errorf("DimA = %d, want %d", event.DimA, 200)
	}
	if event.DimB != 150 {
		t.Errorf("DimB = %d, want %d", event.DimB, 150)
	}
	if event.DimC != 25 {
		t.Errorf("DimC = %d, want %d", event.DimC, 25)
	}
	if event.DimD != 25 {
		t.Errorf("DimD = %d, want %d", event.DimD, 25)
	}
	if event.ETA != "04-15 06:00" {
		t.Errorf("ETA = %q, want %q", event.ETA, "04-15 06:00")
	}

	// Position from MetaData.
	if event.Lat != coord.ScaleLat(1.2644) {
		t.Errorf("Lat = %d, want %d", event.Lat, coord.ScaleLat(1.2644))
	}
}

func TestParseMessage_UnknownType(t *testing.T) {
	data := []byte(`{"MessageType":"BaseStationReport","MetaData":{"MMSI":1,"latitude":1,"longitude":1,"time_utc":"2026-04-12T10:00:00Z"},"Message":{}}`)
	_, ok := ParseMessage(data, "aisstream")
	if ok {
		t.Error("expected ok=false for unknown message type")
	}
}

func TestParseMessage_MalformedJSON(t *testing.T) {
	_, ok := ParseMessage([]byte(`{invalid`), "aisstream")
	if ok {
		t.Error("expected ok=false for malformed JSON")
	}
}

func TestParseMessage_HeadingFallback(t *testing.T) {
	event, ok := ParseMessage(loadFixture(t, "class_b_position_report.json"), "aisstream")
	if !ok {
		t.Fatal("expected ok=true")
	}
	// TrueHeading=511 in fixture → falls back to Cog=270.5.
	if math.Abs(float64(event.Heading-270.5)) > 0.1 {
		t.Errorf("Heading = %f, want %f (Cog fallback)", event.Heading, 270.5)
	}
}

func TestNavStatusName(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{0, "under_way_using_engine"},
		{1, "at_anchor"},
		{5, "moored"},
		{7, "engaged_in_fishing"},
		{15, "not_defined"},
		{99, "not_defined"},
	}
	for _, tt := range tests {
		got := navStatusName(tt.code)
		if got != tt.want {
			t.Errorf("navStatusName(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestShipTypeName(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{0, ""},
		{30, "fishing"},
		{35, "military"},
		{36, "sailing"},
		{37, "pleasure_craft"},
		{52, "tug"},
		{60, "passenger"},
		{70, "cargo"},
		{80, "tanker"},
		{99, "other"},
	}
	for _, tt := range tests {
		got := shipTypeName(tt.code)
		if got != tt.want {
			t.Errorf("shipTypeName(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}
