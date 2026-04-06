package coord

import "testing"

func TestScaleLat(t *testing.T) {
	tests := []struct {
		input float64
		want  int32
	}{
		{51.5074, 515074000},
		{-33.8688, -338688000},
		{0.0001, 1000},
		{90.0, 900000000},
		{-90.0, -900000000},
	}
	for _, tt := range tests {
		got := ScaleLat(tt.input)
		if got != tt.want {
			t.Errorf("ScaleLat(%f) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestScaleLon(t *testing.T) {
	tests := []struct {
		input float64
		want  int32
	}{
		{-0.1278, -1278000},
		{151.2093, 1512093000},
		{180.0, 1800000000},
		{-180.0, -1800000000},
	}
	for _, tt := range tests {
		got := ScaleLon(tt.input)
		if got != tt.want {
			t.Errorf("ScaleLon(%f) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestComputeH3(t *testing.T) {
	h3cell, err := ComputeH3(51.5074, -0.1278)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h3cell == 0 {
		t.Fatal("expected non-zero H3 cell")
	}

	// Same coordinates should produce same cell.
	h3cell2, err := ComputeH3(51.5074, -0.1278)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h3cell != h3cell2 {
		t.Fatal("expected deterministic H3 cell")
	}

	// Different coordinates should produce different cell.
	h3cell3, err := ComputeH3(-33.8688, 151.2093)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h3cell == h3cell3 {
		t.Fatal("expected different H3 cell for different coordinates")
	}
}
