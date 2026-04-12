package tle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestFetchCelesTrak(t *testing.T) {
	fixture, err := os.ReadFile("testdata/celestrak_gp.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	omms, err := FetchCelesTrak(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchCelesTrak: %v", err)
	}
	if len(omms) != 4 {
		t.Errorf("got %d OMMs, want 4", len(omms))
	}
	if omms[0].ObjectName != "ISS (ZARYA)" {
		t.Errorf("ObjectName = %q, want %q", omms[0].ObjectName, "ISS (ZARYA)")
	}
}

func TestFetchCelesTrak_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	_, err := FetchCelesTrak(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 403")
	}
}

func TestFetchClassified(t *testing.T) {
	fixture, err := os.ReadFile("testdata/classfd.tle")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture)
	}))
	defer srv.Close()

	tles, err := FetchClassified(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchClassified: %v", err)
	}
	if len(tles) != 1 {
		t.Fatalf("got %d TLEs, want 1", len(tles))
	}
	if tles[0].Name != "USA 245 (NOSS 3-6 C)" {
		t.Errorf("Name = %q, want %q", tles[0].Name, "USA 245 (NOSS 3-6 C)")
	}
	if tles[0].SatelliteNumber != 39384 {
		t.Errorf("SatelliteNumber = %d, want %d", tles[0].SatelliteNumber, 39384)
	}
}

func TestParseTLEBatch(t *testing.T) {
	data := `ISS (ZARYA)
1 25544U 98067A   26100.94100183  .00005402  00000-0  10657-3 0  9998
2 25544  51.6326 267.9482 0006452 301.5882  58.4476 15.48877690561480
NOAA 15
1 25338U 98030A   26100.50000000  .00000100  00000-0  10000-3 0  9990
2 25338  98.7000  50.0000 0010000  90.0000 270.0000 14.26000000100005
`
	tles, err := parseTLEBatch(data)
	if err != nil {
		t.Fatalf("parseTLEBatch: %v", err)
	}
	if len(tles) != 2 {
		t.Fatalf("got %d TLEs, want 2", len(tles))
	}
	if tles[0].Name != "ISS (ZARYA)" {
		t.Errorf("first TLE name = %q, want %q", tles[0].Name, "ISS (ZARYA)")
	}
	if tles[1].Name != "NOAA 15" {
		t.Errorf("second TLE name = %q, want %q", tles[1].Name, "NOAA 15")
	}
}
