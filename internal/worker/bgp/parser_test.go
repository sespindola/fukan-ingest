package bgp

import (
	"fmt"
	"testing"
)

func newTestDeps() (*Geo, *PrefixState) {
	return NewGeo(), NewPrefixState(1024)
}

func risMsg(pathMsg string) []byte {
	return []byte(fmt.Sprintf(`{"type":"ris_message","data":%s}`, pathMsg))
}

func TestParseAnnouncement_NewPrefixBecomesAnnouncementEvent(t *testing.T) {
	geo, state := newTestDeps()
	data := risMsg(`{
		"timestamp": 1712345678.5,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)

	events, err := ParseMessage(data, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Category != "announcement" {
		t.Errorf("category = %q, want announcement", e.Category)
	}
	if e.Prefix != "198.51.100.0/24" {
		t.Errorf("prefix = %q", e.Prefix)
	}
	if e.OriginAS != 15169 {
		t.Errorf("origin = %d", e.OriginAS)
	}
	if e.Collector != "rrc00" {
		t.Errorf("collector = %q", e.Collector)
	}
	if e.Timestamp != 1712345678500 {
		t.Errorf("ts = %d", e.Timestamp)
	}
	// Single-hop path: path_coords should contain exactly one lat/lon pair.
	if len(e.PathCoords) != 2 {
		t.Errorf("path_coords len = %d, want 2", len(e.PathCoords))
	}
}

func TestParseAnnouncement_ReannouncementIsDropped(t *testing.T) {
	geo, state := newTestDeps()
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)

	if _, err := ParseMessage(data, "ris-live", geo, state, nil, nil); err != nil {
		t.Fatal(err)
	}
	events, err := ParseMessage(data, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("want 0 events on re-announce, got %d", len(events))
	}
}

func TestParseAnnouncement_OriginChangeIsHijack(t *testing.T) {
	geo, state := newTestDeps()
	first := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	if _, err := ParseMessage(first, "ris-live", geo, state, nil, nil); err != nil {
		t.Fatal(err)
	}

	second := risMsg(`{
		"timestamp": 1712345679.0,
		"peer_asn": "13335",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [13335],
		"announcements": [{"next_hop": "5.6.7.8", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(second, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Category != "hijack" {
		t.Fatalf("want hijack, got %+v", events)
	}
	if events[0].OriginAS != 13335 {
		t.Errorf("origin = %d", events[0].OriginAS)
	}
}

func TestParseWithdrawal_KnownPrefixEmitsEvent(t *testing.T) {
	geo, state := newTestDeps()
	ann := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	if _, err := ParseMessage(ann, "ris-live", geo, state, nil, nil); err != nil {
		t.Fatal(err)
	}

	wd := risMsg(`{
		"timestamp": 1712345690.0,
		"peer_asn": "15169",
		"host": "rrc01",
		"type": "UPDATE",
		"withdrawals": ["198.51.100.0/24"]
	}`)
	events, err := ParseMessage(wd, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Category != "withdrawal" {
		t.Fatalf("want withdrawal, got %+v", events)
	}
	if state.Known("198.51.100.0/24") {
		t.Error("state should have forgotten the prefix")
	}
}

func TestParseWithdrawal_UnknownPrefixDropped(t *testing.T) {
	geo, state := newTestDeps()
	wd := risMsg(`{
		"timestamp": 1712345690.0,
		"peer_asn": "15169",
		"host": "rrc01",
		"type": "UPDATE",
		"withdrawals": ["203.0.113.0/24"]
	}`)
	events, err := ParseMessage(wd, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("want 0 events, got %d", len(events))
	}
}

func TestParseLeakDetection(t *testing.T) {
	geo, state := newTestDeps()
	// Non-Tier-1 (14061 DigitalOcean) appears before Tier-1 (174 Cogent) in
	// the path — Tier-1 downstream of non-Tier-1 is a leak.
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "14061",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [14061, 174, 15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Category != "leak" {
		t.Fatalf("want leak, got %+v", events)
	}
	// All three hops (14061, 174, 15169) are in the seed table — expect a
	// fully-resolved path: 3 hops × 2 int32 coords = 6 entries.
	if got := len(events[0].PathCoords); got != 6 {
		t.Errorf("path_coords len = %d, want 6 (3 hops × lat/lon)", got)
	}
}

func TestParseAnnouncement_UnknownHopsSkippedInPathCoords(t *testing.T) {
	geo, state := newTestDeps()
	// Mix of known (15169 Google) and unknown (4200000001 private-use) ASNs.
	// Unknown hops are silently dropped from path_coords — shorter polyline
	// is better than false positions at null-island.
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [4200000001, 15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	// Only the known hop (15169) contributes: 1 hop × 2 = 2 entries.
	if got := len(events[0].PathCoords); got != 2 {
		t.Errorf("path_coords len = %d, want 2 (one known hop)", got)
	}
}

func TestParseWithdrawal_HasNoPathCoords(t *testing.T) {
	geo, state := newTestDeps()
	ann := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	if _, err := ParseMessage(ann, "ris-live", geo, state, nil, nil); err != nil {
		t.Fatal(err)
	}

	wd := risMsg(`{
		"timestamp": 1712345690.0,
		"peer_asn": "15169",
		"host": "rrc01",
		"type": "UPDATE",
		"withdrawals": ["198.51.100.0/24"]
	}`)
	events, err := ParseMessage(wd, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	// Withdrawals carry no AS path → no path_coords.
	if got := len(events[0].PathCoords); got != 0 {
		t.Errorf("path_coords len = %d, want 0 for withdrawal", got)
	}
}

func TestParseNonUpdateMessage(t *testing.T) {
	geo, state := newTestDeps()
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "OPEN"
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("want 0 events for non-UPDATE, got %d", len(events))
	}
}

func TestParseUnknownASNIsDropped(t *testing.T) {
	geo, state := newTestDeps()
	// AS 4200000000 is in private-use range and not in seedASN — geo.Lookup
	// will miss, and the synchronous parse must drop the event rather than
	// emit with null-island coordinates.
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "4200000000",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [4200000000],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("want 0 events for unknown ASN, got %d", len(events))
	}
}

func TestParsePathWithASSet(t *testing.T) {
	geo, state := newTestDeps()
	// RIS Live emits AS_SET elements as nested arrays. Here the terminal
	// element is an AS_SET containing two Google-owned ASes — flattenPath
	// should expand it in-order, making the last ASN in the set the origin.
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "14061",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [14061, 174, [15169, 13335]],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, nil, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	// Flattened path is [14061, 174, 15169, 13335]; origin is the last.
	if got := events[0].OriginAS; got != 13335 {
		t.Errorf("origin = %d, want 13335 (last AS in flattened AS_SET)", got)
	}
	// All four ASes are in the seed table, so path_coords should carry
	// 4 hops × 2 ints = 8 entries.
	if got := len(events[0].PathCoords); got != 8 {
		t.Errorf("path_coords len = %d, want 8", got)
	}
}

func TestParseInvalidJSON(t *testing.T) {
	geo, state := newTestDeps()
	if _, err := ParseMessage([]byte("not json"), "ris-live", geo, state, nil, nil); err == nil {
		t.Error("want error for invalid JSON")
	}
}

// fakeCity implements cityLookup with a canned response per prefix. Any
// prefix not in the map returns ok=false, exercising the fallback branch.
type fakeCity struct {
	hits map[string]struct{ lat, lon float64 }
}

func (f *fakeCity) LookupPrefix(prefix string) (float64, float64, bool) {
	c, ok := f.hits[prefix]
	if !ok {
		return 0, 0, false
	}
	return c.lat, c.lon, true
}

// MMDB hit on an announcement must win over the ASN-centroid path lookup.
// With a known ASN (15169) in the seed table, geoFromPath would otherwise
// return US-centroid coords; the fake puts the prefix in São Paulo. Post-
// scaling (Int32 = degrees × 1e7), a positive-lat result from São Paulo
// (-23.5) reads as a large negative Lat — distinct from the seed's.
func TestParseAnnouncement_MMDBTakesPrecedenceOverGeoFromPath(t *testing.T) {
	geo, state := newTestDeps()
	city := &fakeCity{hits: map[string]struct{ lat, lon float64 }{
		"198.51.100.0/24": {lat: -23.55, lon: -46.63},
	}}
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, city, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	// coord.ScaleLat(-23.55) = -235500000. If MMDB was ignored and
	// geoFromPath returned the US centroid, Lat would be positive.
	if events[0].Lat >= 0 {
		t.Errorf("Lat = %d, want MMDB coords (negative, São Paulo)", events[0].Lat)
	}
}

// MMDB miss falls back to geoFromPath (existing behavior preserved).
func TestParseAnnouncement_MMDBMissFallsBackToGeoFromPath(t *testing.T) {
	geo, state := newTestDeps()
	city := &fakeCity{hits: map[string]struct{ lat, lon float64 }{}} // no hits
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, city, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event via Geo fallback, got %d", len(events))
	}
}

// Both MMDB and Geo missing → drop.
func TestParseAnnouncement_BothMissesDropsEvent(t *testing.T) {
	geo, state := newTestDeps()
	city := &fakeCity{hits: map[string]struct{ lat, lon float64 }{}}
	// AS 4200000001 is private-use, not in seedASN.
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "4200000001",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [4200000001],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, city, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("want 0 events when both MMDB and Geo miss, got %d", len(events))
	}
}

// Withdrawals should prefer the MMDB lookup of the withdrawn prefix over
// peer-ASN centroid — "prefix X went dark" belongs at prefix X's location.
func TestParseWithdrawal_MMDBTakesPrecedenceOverPeerASN(t *testing.T) {
	geo, state := newTestDeps()
	city := &fakeCity{hits: map[string]struct{ lat, lon float64 }{
		"198.51.100.0/24": {lat: -23.55, lon: -46.63},
	}}
	ann := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	if _, err := ParseMessage(ann, "ris-live", geo, state, city, nil); err != nil {
		t.Fatal(err)
	}

	wd := risMsg(`{
		"timestamp": 1712345690.0,
		"peer_asn": "15169",
		"host": "rrc01",
		"type": "UPDATE",
		"withdrawals": ["198.51.100.0/24"]
	}`)
	events, err := ParseMessage(wd, "ris-live", geo, state, city, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 withdrawal event, got %d", len(events))
	}
	if events[0].Lat >= 0 {
		t.Errorf("Lat = %d, want MMDB coords for withdrawn prefix", events[0].Lat)
	}
}

// fakeASN implements asnLookup with canned responses per prefix. Any
// prefix not in the map returns ok=false, leaving PrefixAS/PrefixOrg
// at their zero values on the event (but not dropping).
type fakeASN struct {
	hits map[string]struct {
		asn uint32
		org string
	}
}

func (f *fakeASN) LookupPrefix(prefix string) (uint32, string, bool) {
	h, ok := f.hits[prefix]
	if !ok {
		return 0, "", false
	}
	return h.asn, h.org, true
}

// Announcement with an MMDB hit should stamp PrefixAS + PrefixOrg.
func TestParseAnnouncement_ASNEnrichmentStamped(t *testing.T) {
	geo, state := newTestDeps()
	asn := &fakeASN{hits: map[string]struct {
		asn uint32
		org string
	}{
		"198.51.100.0/24": {asn: 15169, org: "GOOGLE"},
	}}
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, nil, asn)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].PrefixAS != 15169 || events[0].PrefixOrg != "GOOGLE" {
		t.Errorf("PrefixAS=%d PrefixOrg=%q, want 15169/GOOGLE", events[0].PrefixAS, events[0].PrefixOrg)
	}
}

// An ASN-MMDB miss must not drop the event — it just leaves the org
// fields at their zero values so the frontend can fall back to AS#.
func TestParseAnnouncement_ASNMissLeavesEmptyFields(t *testing.T) {
	geo, state := newTestDeps()
	asn := &fakeASN{hits: map[string]struct {
		asn uint32
		org string
	}{}}
	data := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(data, "ris-live", geo, state, nil, asn)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event (miss should not drop), got %d", len(events))
	}
	if events[0].PrefixAS != 0 || events[0].PrefixOrg != "" {
		t.Errorf("PrefixAS=%d PrefixOrg=%q, want zero values on miss", events[0].PrefixAS, events[0].PrefixOrg)
	}
}

// Hijack: origin_as (hijacker from RIS Live) diverges from prefix_as
// (registered holder from MMDB). The divergence is the OSINT signal.
func TestParseAnnouncement_HijackShowsHolderDivergence(t *testing.T) {
	geo, state := newTestDeps()
	asn := &fakeASN{hits: map[string]struct {
		asn uint32
		org string
	}{
		"198.51.100.0/24": {asn: 13335, org: "CLOUDFLARENET"},
	}}
	// First announcement: legitimate holder (13335) announces its own prefix.
	first := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "13335",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [13335],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	if _, err := ParseMessage(first, "ris-live", geo, state, nil, asn); err != nil {
		t.Fatal(err)
	}

	// Hijack: a different AS (15169) announces Cloudflare's prefix.
	second := risMsg(`{
		"timestamp": 1712345679.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "5.6.7.8", "prefixes": ["198.51.100.0/24"]}]
	}`)
	events, err := ParseMessage(second, "ris-live", geo, state, nil, asn)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Category != "hijack" {
		t.Fatalf("want hijack event, got %+v", events)
	}
	if events[0].OriginAS != 15169 {
		t.Errorf("OriginAS=%d, want 15169 (hijacker from RIS Live)", events[0].OriginAS)
	}
	if events[0].PrefixAS != 13335 {
		t.Errorf("PrefixAS=%d, want 13335 (registered holder from MMDB)", events[0].PrefixAS)
	}
	if events[0].PrefixOrg != "CLOUDFLARENET" {
		t.Errorf("PrefixOrg=%q, want CLOUDFLARENET", events[0].PrefixOrg)
	}
	if events[0].OriginAS == events[0].PrefixAS {
		t.Error("expected OriginAS and PrefixAS to diverge on hijack")
	}
}

// Withdrawals also stamp prefix-org (the legitimate holder of the dark
// prefix is still useful context).
func TestParseWithdrawal_ASNEnrichmentStamped(t *testing.T) {
	geo, state := newTestDeps()
	asn := &fakeASN{hits: map[string]struct {
		asn uint32
		org string
	}{
		"198.51.100.0/24": {asn: 15169, org: "GOOGLE"},
	}}
	ann := risMsg(`{
		"timestamp": 1712345678.0,
		"peer_asn": "15169",
		"host": "rrc00",
		"type": "UPDATE",
		"path": [15169],
		"announcements": [{"next_hop": "1.2.3.4", "prefixes": ["198.51.100.0/24"]}]
	}`)
	if _, err := ParseMessage(ann, "ris-live", geo, state, nil, asn); err != nil {
		t.Fatal(err)
	}

	wd := risMsg(`{
		"timestamp": 1712345690.0,
		"peer_asn": "15169",
		"host": "rrc01",
		"type": "UPDATE",
		"withdrawals": ["198.51.100.0/24"]
	}`)
	events, err := ParseMessage(wd, "ris-live", geo, state, nil, asn)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 withdrawal event, got %d", len(events))
	}
	if events[0].PrefixAS != 15169 || events[0].PrefixOrg != "GOOGLE" {
		t.Errorf("PrefixAS=%d PrefixOrg=%q, want 15169/GOOGLE", events[0].PrefixAS, events[0].PrefixOrg)
	}
}
