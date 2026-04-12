package tle

import (
	"testing"
	"time"

	"github.com/sespindola/fukan-ingest/internal/model"
)

func TestPropagateOne_ISS(t *testing.T) {
	omms := loadGPFixture(t)
	tle, err := omms[0].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}

	entry := &satEntry{
		tle:        tle,
		name:       "ISS (ZARYA)",
		noradID:    25544,
		regime:     RegimeLEO,
		confidence: "official",
		epoch:      tle.EpochTime(),
		params:     OrbitalParams(tle),
	}

	// Propagate to a time close to the TLE epoch.
	now := tle.EpochTime().Add(10 * time.Minute)
	event, ok := propagateOne(entry, now)
	if !ok {
		t.Fatal("propagateOne returned false")
	}

	if event.AssetID != "25544" {
		t.Errorf("AssetID = %q, want %q", event.AssetID, "25544")
	}
	if event.AssetType != model.AssetSatellite {
		t.Errorf("AssetType = %q, want %q", event.AssetType, model.AssetSatellite)
	}
	if event.Callsign != "ISS (ZARYA)" {
		t.Errorf("Callsign = %q, want %q", event.Callsign, "ISS (ZARYA)")
	}
	if event.OrbitRegime != "leo" {
		t.Errorf("OrbitRegime = %q, want %q", event.OrbitRegime, "leo")
	}
	if event.Confidence != "official" {
		t.Errorf("Confidence = %q, want %q", event.Confidence, "official")
	}

	// ISS is in LEO, altitude should be ~400 km → ~400,000 meters.
	if event.Alt < 300_000 || event.Alt > 500_000 {
		t.Errorf("Alt = %d meters, want 300000-500000", event.Alt)
	}

	// Lat should be within ISS inclination bounds (-51.6 to 51.6).
	latDeg := float64(event.Lat) / 10_000_000
	if latDeg < -52 || latDeg > 52 {
		t.Errorf("Lat = %f degrees, want within ±52", latDeg)
	}

	if event.H3Cell == 0 {
		t.Error("H3Cell = 0, want non-zero")
	}
	if event.TLEEpoch == 0 {
		t.Error("TLEEpoch = 0, want non-zero")
	}
	if event.PeriodMinutes < 90 || event.PeriodMinutes > 95 {
		t.Errorf("PeriodMinutes = %f, want 90-95", event.PeriodMinutes)
	}
	if event.Source != "celestrak" {
		t.Errorf("Source = %q, want %q", event.Source, "celestrak")
	}
}

func TestPropagateOne_Stale(t *testing.T) {
	omms := loadGPFixture(t)
	tle, err := omms[0].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}

	entry := &satEntry{
		tle:        tle,
		name:       "ISS (ZARYA)",
		noradID:    25544,
		regime:     RegimeLEO,
		confidence: "official",
		epoch:      tle.EpochTime(),
		params:     OrbitalParams(tle),
	}

	// Propagate 20 days after epoch → should be marked stale for LEO (>14 days).
	now := tle.EpochTime().Add(20 * 24 * time.Hour)
	event, ok := propagateOne(entry, now)
	if !ok {
		// SGP4 may fail at 20 days out — that's acceptable for this test.
		t.Skip("SGP4 propagation failed at 20 days, skipping staleness test")
	}
	if event.Confidence != "stale" {
		t.Errorf("Confidence = %q, want %q", event.Confidence, "stale")
	}
}

func TestPropagateOne_CommunityDerived(t *testing.T) {
	omms := loadGPFixture(t)
	tle, err := omms[0].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}

	entry := &satEntry{
		tle:        tle,
		name:       "USA 245",
		noradID:    39384,
		regime:     RegimeLEO,
		confidence: "community_derived",
		epoch:      tle.EpochTime(),
		params:     OrbitalParams(tle),
	}

	now := tle.EpochTime().Add(5 * time.Minute)
	event, ok := propagateOne(entry, now)
	if !ok {
		t.Fatal("propagateOne returned false")
	}
	if event.Source != "classfd" {
		t.Errorf("Source = %q, want %q", event.Source, "classfd")
	}
	if event.Confidence != "community_derived" {
		t.Errorf("Confidence = %q, want %q", event.Confidence, "community_derived")
	}
}

func TestDetectManeuver(t *testing.T) {
	old := Params{PeriodMinutes: 92.0, Inclination: 51.6}

	// No maneuver: small changes.
	noChange := Params{PeriodMinutes: 92.001, Inclination: 51.601}
	if detectManeuver(old, noChange) {
		t.Error("expected no maneuver for tiny change")
	}

	// Mean motion change (period shift).
	periodShift := Params{PeriodMinutes: 91.0, Inclination: 51.6}
	if !detectManeuver(old, periodShift) {
		t.Error("expected maneuver for period shift from 92 to 91 min")
	}

	// Inclination change (plane change).
	planeChange := Params{PeriodMinutes: 92.0, Inclination: 52.0}
	if !detectManeuver(old, planeChange) {
		t.Error("expected maneuver for inclination shift from 51.6 to 52.0")
	}
}

func TestDetectDecay(t *testing.T) {
	omms := loadGPFixture(t)
	tle, err := omms[0].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}

	// ISS has perigee ~410 km, should not be decaying.
	healthy := &satEntry{
		tle:    tle,
		params: OrbitalParams(tle),
	}
	if detectDecay(healthy) {
		t.Errorf("ISS should not be detected as decaying (perigee=%f)", healthy.params.PerigeeKm)
	}

	// Low perigee satellite.
	lowPerigee := &satEntry{
		tle:    tle,
		params: Params{PerigeeKm: 120},
	}
	if !detectDecay(lowPerigee) {
		t.Error("expected decay detection for perigee < 150 km")
	}
}

func TestPropagateOne_Decaying(t *testing.T) {
	omms := loadGPFixture(t)
	tle, err := omms[0].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}

	entry := &satEntry{
		tle:        tle,
		name:       "DEBRIS",
		noradID:    99999,
		regime:     RegimeLEO,
		confidence: "official",
		epoch:      tle.EpochTime(),
		params:     Params{PerigeeKm: 120, ApogeeKm: 300, Inclination: 51.6, PeriodMinutes: 89},
	}

	now := tle.EpochTime().Add(5 * time.Minute)
	event, ok := propagateOne(entry, now)
	if !ok {
		t.Fatal("propagateOne returned false")
	}
	if event.SatStatus != "decaying" {
		t.Errorf("SatStatus = %q, want %q", event.SatStatus, "decaying")
	}
}

func TestPropagateOne_Maneuvering(t *testing.T) {
	omms := loadGPFixture(t)
	tle, err := omms[0].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}

	entry := &satEntry{
		tle:        tle,
		name:       "ISS (ZARYA)",
		noradID:    25544,
		regime:     RegimeLEO,
		confidence: "official",
		status:     "maneuvering", // Set by fetchAll maneuver detection
		epoch:      tle.EpochTime(),
		params:     OrbitalParams(tle),
	}

	now := tle.EpochTime().Add(5 * time.Minute)
	event, ok := propagateOne(entry, now)
	if !ok {
		t.Fatal("propagateOne returned false")
	}
	if event.SatStatus != "maneuvering" {
		t.Errorf("SatStatus = %q, want %q", event.SatStatus, "maneuvering")
	}
}
