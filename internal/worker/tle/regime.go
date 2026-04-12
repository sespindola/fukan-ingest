package tle

import (
	"math"

	"github.com/akhenakh/sgp4"
)

const (
	RegimeLEO = "leo"
	RegimeMEO = "meo"
	RegimeGEO = "geo"
	RegimeHEO = "heo"

	earthRadiusKm   = 6371.0
	geoSemiMajorKm  = 42164.0
	geoToleranceKm  = 200.0
	leoAltitudeKm   = 2000.0
	heoEccentricity = 0.25
)

// ClassifyRegime returns the orbital regime for a TLE based on its elements.
func ClassifyRegime(t *sgp4.TLE) string {
	params := OrbitalParams(t)

	if params.Eccentricity > heoEccentricity {
		return RegimeHEO
	}

	sma := params.SemiMajorAxisKm
	if math.Abs(sma-geoSemiMajorKm) < geoToleranceKm {
		return RegimeGEO
	}
	if sma < earthRadiusKm+leoAltitudeKm {
		return RegimeLEO
	}
	return RegimeMEO
}

// Params holds derived orbital parameters from a TLE.
type Params struct {
	PeriodMinutes   float64
	ApogeeKm        float64
	PerigeeKm       float64
	Inclination     float64
	Eccentricity    float64
	SemiMajorAxisKm float64
}

// OrbitalParams derives orbital parameters from a TLE's mean motion and eccentricity.
func OrbitalParams(t *sgp4.TLE) Params {
	// Mean motion is in revolutions per day.
	meanMotion := t.MeanMotion
	eccentricity := t.Eccentricity
	inclination := t.Inclination

	// Period in minutes from mean motion (rev/day).
	periodMin := 1440.0 / meanMotion

	// Semi-major axis from Kepler's third law: a = (GM * T^2 / 4π²)^(1/3)
	// Using the simplified form: a = (μ / (2πn)^2)^(1/3)
	// where μ = 398600.4418 km³/s² and n is in rad/s.
	mu := 398600.4418 // km³/s²
	nRadSec := meanMotion * 2.0 * math.Pi / 86400.0
	sma := math.Cbrt(mu / (nRadSec * nRadSec))

	perigeeKm := sma*(1-eccentricity) - earthRadiusKm
	apogeeKm := sma*(1+eccentricity) - earthRadiusKm

	return Params{
		PeriodMinutes:   periodMin,
		ApogeeKm:        apogeeKm,
		PerigeeKm:       perigeeKm,
		Inclination:     inclination,
		Eccentricity:    eccentricity,
		SemiMajorAxisKm: sma,
	}
}
