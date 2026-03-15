package model

import "testing"

func validEvent() FukanEvent {
	return FukanEvent{
		Timestamp: 1710000000000,
		AssetID:   "A1B2C3",
		AssetType: AssetAircraft,
		Lat:       515074000,
		Lon:       -1278000,
		Alt:       10000,
		H3Cell:    1234567890,
		Source:    "test",
	}
}

func TestValidateValid(t *testing.T) {
	if err := Validate(validEvent()); err != nil {
		t.Fatalf("expected valid event, got: %v", err)
	}
}

func TestValidateEmptyAssetID(t *testing.T) {
	e := validEvent()
	e.AssetID = ""
	if err := Validate(e); err == nil {
		t.Fatal("expected error for empty asset_id")
	}
}

func TestValidateEmptyAssetType(t *testing.T) {
	e := validEvent()
	e.AssetType = ""
	if err := Validate(e); err == nil {
		t.Fatal("expected error for empty asset_type")
	}
}

func TestValidateEmptySource(t *testing.T) {
	e := validEvent()
	e.Source = ""
	if err := Validate(e); err == nil {
		t.Fatal("expected error for empty source")
	}
}

func TestValidateNullIsland(t *testing.T) {
	e := validEvent()
	e.Lat = 0
	e.Lon = 0
	if err := Validate(e); err == nil {
		t.Fatal("expected error for null island")
	}
}

func TestValidateLatOutOfRange(t *testing.T) {
	e := validEvent()
	e.Lat = 900_000_001
	if err := Validate(e); err == nil {
		t.Fatal("expected error for lat out of range")
	}
	e.Lat = -900_000_001
	if err := Validate(e); err == nil {
		t.Fatal("expected error for negative lat out of range")
	}
}

func TestValidateLonOutOfRange(t *testing.T) {
	e := validEvent()
	e.Lon = 1_800_000_001
	if err := Validate(e); err == nil {
		t.Fatal("expected error for lon out of range")
	}
	e.Lon = -1_800_000_001
	if err := Validate(e); err == nil {
		t.Fatal("expected error for negative lon out of range")
	}
}

func TestValidateInvalidTimestamp(t *testing.T) {
	e := validEvent()
	e.Timestamp = 0
	if err := Validate(e); err == nil {
		t.Fatal("expected error for zero timestamp")
	}
	e.Timestamp = -1
	if err := Validate(e); err == nil {
		t.Fatal("expected error for negative timestamp")
	}
}
