package bgp

import (
	"os"
	"testing"
)

// These tests require a real GeoLite2-City.mmdb on disk. Point
// FUKAN_TEST_GEOIP_DB at the file to run; otherwise the tests skip so CI
// can stay green without shipping a 70 MB binary blob.
func loadTestCity(t *testing.T) *CityLookup {
	t.Helper()
	path := os.Getenv("FUKAN_TEST_GEOIP_DB")
	if path == "" {
		t.Skip("FUKAN_TEST_GEOIP_DB not set; skipping MMDB-backed tests")
	}
	c, err := NewCityLookup(path)
	if err != nil {
		t.Fatalf("open MMDB at %q: %v", path, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestCityLookup_EmptyPathFails(t *testing.T) {
	if _, err := NewCityLookup(""); err == nil {
		t.Error("want error for empty path")
	}
}

func TestCityLookup_MissingFileFails(t *testing.T) {
	if _, err := NewCityLookup("/nonexistent/path/GeoLite2-City.mmdb"); err == nil {
		t.Error("want error for missing file")
	}
}

func TestCityLookup_PublicPrefixResolves(t *testing.T) {
	c := loadTestCity(t)
	// Cloudflare's public resolver prefix — well-covered by any recent
	// GeoLite2-City build.
	lat, lon, ok := c.LookupPrefix("1.1.1.0/24")
	if !ok {
		t.Fatal("1.1.1.0/24 did not resolve")
	}
	if lat == 0 || lon == 0 {
		t.Errorf("got null-island coords lat=%f lon=%f", lat, lon)
	}
}

func TestCityLookup_PrivatePrefixMisses(t *testing.T) {
	c := loadTestCity(t)
	cases := []string{"10.0.0.0/8", "192.168.0.0/16", "fc00::/7"}
	for _, p := range cases {
		if _, _, ok := c.LookupPrefix(p); ok {
			t.Errorf("%s unexpectedly resolved", p)
		}
	}
}

func TestCityLookup_MalformedPrefixMisses(t *testing.T) {
	c := loadTestCity(t)
	cases := []string{"not-a-prefix", "", "999.999.999.0/24", "1.2.3.4"}
	for _, p := range cases {
		if _, _, ok := c.LookupPrefix(p); ok {
			t.Errorf("%q unexpectedly resolved", p)
		}
	}
}
