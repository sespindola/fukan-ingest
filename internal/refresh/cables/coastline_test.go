package cables

import "testing"

func TestIsLandingCoord(t *testing.T) {
	cases := []struct {
		name string
		lat  float64
		lon  float64
		want bool
	}{
		// Real cable landing locations — must pass.
		{"Singapore", 1.3521, 103.8198, true},
		{"Goonhilly Downs UK", 50.0394, -5.1709, true},
		{"Marseille", 43.2965, 5.3698, true},
		{"Mumbai", 19.076, 72.877, true},
		{"Norden DE", 53.5957, 7.2056, true},
		{"Penmarch FR", 47.812, -4.338, true},

		// Country/region centroids — must fail (the visible bug).
		{"Brazil centroid", -14.0, -53.0, false},
		{"Italy centroid", 42.5, 12.5, false},
		{"UK centroid", 54.6, -2.0, false},
		{"Brasília inland", -15.79, -47.88, false},

		// Mid-ocean — must fail (not near coast).
		{"Mid-Atlantic", 30.0, -40.0, false},
		{"Mid-Pacific", 0.0, -160.0, false},
	}
	for _, tc := range cases {
		got := IsLandingCoord(tc.lat, tc.lon)
		if got != tc.want {
			t.Errorf("IsLandingCoord(%q, %v, %v) = %v, want %v", tc.name, tc.lat, tc.lon, got, tc.want)
		}
	}
}

func TestIsOnLand(t *testing.T) {
	cases := []struct {
		name string
		lat  float64
		lon  float64
		want bool
	}{
		{"Brasília", -15.79, -47.88, true},
		{"Sahara", 25.0, 15.0, true},
		{"Mid-Atlantic", 30.0, -40.0, false},
		{"South Pacific", -30.0, -130.0, false},
		{"Singapore (small island)", 1.3521, 103.8198, true},
	}
	for _, tc := range cases {
		got := IsOnLand(tc.lat, tc.lon)
		if got != tc.want {
			t.Errorf("IsOnLand(%q, %v, %v) = %v, want %v", tc.name, tc.lat, tc.lon, got, tc.want)
		}
	}
}

func TestPolylineClearsLand(t *testing.T) {
	// Pure ocean polyline (mid-Atlantic samples) — must pass.
	mkPoly := func(pairs ...[2]float64) []int32 {
		out := make([]int32, 0, len(pairs)*2)
		for _, p := range pairs {
			out = append(out, ScaleCoord(p[0]), ScaleCoord(p[1]))
		}
		return out
	}

	t.Run("ocean polyline passes", func(t *testing.T) {
		poly := mkPoly(
			[2]float64{40, -50}, // mid-Atlantic
			[2]float64{42, -45},
			[2]float64{44, -40},
			[2]float64{45, -35},
		)
		if !PolylineClearsLand(poly) {
			t.Errorf("expected ocean polyline to pass, got false")
		}
	})

	t.Run("polyline through Brazil interior fails", func(t *testing.T) {
		// Goes from Atlantic landing across Brazil interior to Pacific —
		// classic synth-through-continent shape.
		poly := mkPoly(
			[2]float64{-3.7, -38.5},  // Fortaleza (coast) — endpoint
			[2]float64{-10.0, -50.0}, // INTERIOR — should be on land
			[2]float64{-15.0, -55.0}, // INTERIOR — Mato Grosso
			[2]float64{-12.0, -77.0}, // Lima coast — endpoint
		)
		if PolylineClearsLand(poly) {
			t.Errorf("expected through-continent polyline to fail, got true")
		}
	})

	t.Run("two-coord polyline trivially passes", func(t *testing.T) {
		// Endpoints only — both within LandingApproachKm of themselves;
		// no interior nodes to check.
		poly := mkPoly([2]float64{50, -5}, [2]float64{53, 7})
		if !PolylineClearsLand(poly) {
			t.Errorf("expected 2-coord polyline to pass trivially")
		}
	})
}

func TestSegmentClearsLand(t *testing.T) {
	if !SegmentClearsLand(40, -50, 45, -40) {
		t.Error("mid-Atlantic segment should clear land")
	}
	// Lisbon (38.7, -9.1) → Buenos Aires (-34.6, -58.4) — great circle clips
	// the eastern bulge of Brazil.
	if SegmentClearsLand(38.7, -9.1, -34.6, -58.4) {
		t.Error("Lisbon → Buenos Aires great circle should fail (clips Brazil)")
	}
}
