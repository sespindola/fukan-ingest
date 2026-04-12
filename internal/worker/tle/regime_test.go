package tle

import (
	"math"
	"os"
	"testing"

	"github.com/akhenakh/sgp4"
)

func loadGPFixture(t *testing.T) []sgp4.OMM {
	t.Helper()
	data, err := os.ReadFile("testdata/celestrak_gp.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	omms, err := sgp4.ParseOMMs(data)
	if err != nil {
		t.Fatalf("parse OMMs: %v", err)
	}
	return omms
}

func TestClassifyRegime_LEO(t *testing.T) {
	omms := loadGPFixture(t)
	// ISS: mean motion ~15.49 rev/day → LEO
	tle, err := omms[0].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}
	regime := ClassifyRegime(tle)
	if regime != RegimeLEO {
		t.Errorf("ISS regime = %q, want %q", regime, RegimeLEO)
	}
}

func TestClassifyRegime_GEO(t *testing.T) {
	omms := loadGPFixture(t)
	// TDRS 3: mean motion ~1.003 rev/day → GEO
	tle, err := omms[1].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}
	regime := ClassifyRegime(tle)
	if regime != RegimeGEO {
		t.Errorf("TDRS 3 regime = %q, want %q", regime, RegimeGEO)
	}
}

func TestClassifyRegime_MEO(t *testing.T) {
	omms := loadGPFixture(t)
	// GPS: mean motion ~2.006 rev/day → MEO
	tle, err := omms[2].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}
	regime := ClassifyRegime(tle)
	if regime != RegimeMEO {
		t.Errorf("GPS regime = %q, want %q", regime, RegimeMEO)
	}
}

func TestClassifyRegime_HEO(t *testing.T) {
	omms := loadGPFixture(t)
	// Molniya: eccentricity 0.74 → HEO
	tle, err := omms[3].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}
	regime := ClassifyRegime(tle)
	if regime != RegimeHEO {
		t.Errorf("Molniya regime = %q, want %q", regime, RegimeHEO)
	}
}

func TestOrbitalParams_ISS(t *testing.T) {
	omms := loadGPFixture(t)
	tle, err := omms[0].ToTLE()
	if err != nil {
		t.Fatalf("ToTLE: %v", err)
	}

	params := OrbitalParams(tle)

	// ISS period ~93 minutes
	if math.Abs(params.PeriodMinutes-93.0) > 2.0 {
		t.Errorf("PeriodMinutes = %f, want ~93", params.PeriodMinutes)
	}
	// ISS perigee ~410 km
	if params.PerigeeKm < 350 || params.PerigeeKm > 450 {
		t.Errorf("PerigeeKm = %f, want 350-450", params.PerigeeKm)
	}
	// ISS inclination ~51.6°
	if math.Abs(params.Inclination-51.6326) > 0.01 {
		t.Errorf("Inclination = %f, want ~51.6326", params.Inclination)
	}
}
