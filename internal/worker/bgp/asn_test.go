package bgp

import (
	"os"
	"testing"
)

// These tests require a real GeoLite2-ASN.mmdb on disk. Point
// FUKAN_TEST_GEOIP_ASN_DB at the file to run; otherwise the tests skip
// so CI stays green without shipping a binary blob.
func loadTestASN(t *testing.T) *ASNLookup {
	t.Helper()
	path := os.Getenv("FUKAN_TEST_GEOIP_ASN_DB")
	if path == "" {
		t.Skip("FUKAN_TEST_GEOIP_ASN_DB not set; skipping MMDB-backed tests")
	}
	a, err := NewASNLookup(path)
	if err != nil {
		t.Fatalf("open ASN MMDB at %q: %v", path, err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestASNLookup_EmptyPathFails(t *testing.T) {
	if _, err := NewASNLookup(""); err == nil {
		t.Error("want error for empty path")
	}
}

func TestASNLookup_MissingFileFails(t *testing.T) {
	if _, err := NewASNLookup("/nonexistent/path/GeoLite2-ASN.mmdb"); err == nil {
		t.Error("want error for missing file")
	}
}

func TestASNLookup_PublicPrefixResolves(t *testing.T) {
	a := loadTestASN(t)
	// Cloudflare's 1.1.1.0/24 — well-covered by any recent GeoLite2-ASN.
	asn, org, ok := a.LookupPrefix("1.1.1.0/24")
	if !ok {
		t.Fatal("1.1.1.0/24 did not resolve")
	}
	if asn != 13335 {
		t.Errorf("asn = %d, want 13335 (Cloudflare)", asn)
	}
	if org == "" {
		t.Error("org is empty")
	}
}

func TestASNLookup_PrivatePrefixMisses(t *testing.T) {
	a := loadTestASN(t)
	cases := []string{"10.0.0.0/8", "192.168.0.0/16", "fc00::/7"}
	for _, p := range cases {
		if _, _, ok := a.LookupPrefix(p); ok {
			t.Errorf("%s unexpectedly resolved", p)
		}
	}
}

func TestASNLookup_MalformedPrefixMisses(t *testing.T) {
	a := loadTestASN(t)
	cases := []string{"not-a-prefix", "", "999.999.999.0/24", "1.2.3.4"}
	for _, p := range cases {
		if _, _, ok := a.LookupPrefix(p); ok {
			t.Errorf("%q unexpectedly resolved", p)
		}
	}
}
