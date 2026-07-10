package cables

import (
	"strings"
	"testing"
	"time"
)

// TestMergeKeepsCableWithOSMMatch — Q1 has valid coastal landings AND an OSM
// way matching by name, with way coords coincident with the landings. The
// way is two-coord (endpoints only), so PolylineClearsLand trivially passes.
func TestMergeKeepsCableWithOSMMatch(t *testing.T) {
	b, err := LoadBaseline(strings.NewReader(fixtureCables), strings.NewReader(fixtureLandings))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	osm := []OSMCable{
		{
			OSMID: 999,
			Name:  "Test Alpha",
			Coords: []int32{
				ScaleCoord(47.815), ScaleCoord(-4.3703),
				ScaleCoord(53.5956), ScaleCoord(7.2053),
			},
		},
	}
	rows, lands := Merge(b, osm, MergeOptions{Now: time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].CableID != "Q1" {
		t.Errorf("rows[0].CableID = %q, want Q1", rows[0].CableID)
	}
	if len(rows[0].OSMWayIDs) != 1 || rows[0].OSMWayIDs[0] != 999 {
		t.Errorf("OSMWayIDs = %v, want [999]", rows[0].OSMWayIDs)
	}
	if !containsURL(rows[0].ProvenanceSourceURLs, "openstreetmap.org/way/999") {
		t.Errorf("missing OSM provenance: %v", rows[0].ProvenanceSourceURLs)
	}
	if !containsURL(rows[0].ProvenanceSourceURLs, "wikidata.org/wiki/Q1") {
		t.Errorf("missing Wikidata provenance: %v", rows[0].ProvenanceSourceURLs)
	}
	// Landings come only from cables that survived the merge.
	if len(lands) != 2 {
		t.Errorf("lands = %d, want 2 (Q1 has 2 landings)", len(lands))
	}
}

// TestMergeDropsCableWithoutOSM — no OSM matches at all → cable dropped.
// New policy: every emitted row needs validated geometry.
func TestMergeDropsCableWithoutOSM(t *testing.T) {
	b, err := LoadBaseline(strings.NewReader(fixtureCables), strings.NewReader(fixtureLandings))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	rows, _ := Merge(b, nil, MergeOptions{})
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0 (no OSM ways → no rows)", len(rows))
	}
}

// TestMergeDropsOSMOrphans — OSM ways with no Wikidata baseline are dropped.
// New policy: every cable needs curated identity, OSM-only orphans don't.
func TestMergeDropsOSMOrphans(t *testing.T) {
	b, err := LoadBaseline(strings.NewReader(fixtureCables), strings.NewReader(fixtureLandings))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	osm := []OSMCable{
		{
			OSMID: 1001,
			Name:  "Orphan Cable",
			Coords: []int32{
				ScaleCoord(0), ScaleCoord(150),
				ScaleCoord(1), ScaleCoord(151),
			},
		},
	}
	rows, _ := Merge(b, osm, MergeOptions{})
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0 (OSM-only orphan should be dropped)", len(rows))
	}
}

// TestMergeDropsOSMMatchTooFarFromLandings — OSM way matches by name but
// its coords are nowhere near any baseline landing → dropped (country
// tiebreak / landing-alignment fail).
func TestMergeDropsOSMMatchTooFarFromLandings(t *testing.T) {
	b, err := LoadBaseline(strings.NewReader(fixtureCables), strings.NewReader(fixtureLandings))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	osm := []OSMCable{
		{
			OSMID: 1234,
			Name:  "Test Alpha", // matches Q1 by name
			Coords: []int32{
				ScaleCoord(-50.0), ScaleCoord(150.0), // South Pacific
				ScaleCoord(-49.0), ScaleCoord(151.0),
			},
		},
	}
	rows, _ := Merge(b, osm, MergeOptions{})
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0 (OSM match too far from any baseline landing)", len(rows))
	}
}

func containsURL(urls []string, fragment string) bool {
	for _, u := range urls {
		if strings.Contains(u, fragment) {
			return true
		}
	}
	return false
}
