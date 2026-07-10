package cables

import (
	"strings"
	"testing"
)

const fixtureCables = `{"cable_id":"Q1","name":"Test Alpha","slug":"test-alpha","alt_names":[],"owners":["Op A"],"status":"active","rfs_year":2020,"length_km":100,"medium":"fibre","category":"fibre_optic","landing_ids":["Q1:Q10","Q1:Q11"],"n_landings":2,"sources":[{"url":"https://www.wikidata.org/wiki/Q1","license":"CC0","retrieved_at":"2026-04-24"},{"url":"https://en.wikipedia.org/wiki/Test_Alpha","license":"CC-BY-SA-3.0","retrieved_at":"2026-04-24"}],"source_count":2}
{"cable_id":"Q2","name":"Test Beta","slug":"test-beta","alt_names":["TB"],"owners":[],"status":"planned","rfs_year":0,"length_km":0,"medium":"fibre","category":"fibre_optic","landing_ids":[],"n_landings":0,"sources":[{"url":"https://www.wikidata.org/wiki/Q2","license":"CC0","retrieved_at":"2026-04-24"}],"source_count":1}
`

const fixtureLandings = `{"landing_id":"Q1:Q10","cable_id":"Q1","cable_name":"Test Alpha","country":"FR","location_name":"Penmarch","lat":47.815,"lon":-4.3703,"sources":[{"url":"https://www.wikidata.org/wiki/Q10","license":"CC0","retrieved_at":"2026-04-24"}]}
{"landing_id":"Q1:Q11","cable_id":"Q1","cable_name":"Test Alpha","country":"DE","location_name":"Norden","lat":53.5956,"lon":7.2053,"sources":[{"url":"https://www.wikidata.org/wiki/Q11","license":"CC0","retrieved_at":"2026-04-24"}]}
`

func TestLoadBaseline(t *testing.T) {
	b, err := LoadBaseline(strings.NewReader(fixtureCables), strings.NewReader(fixtureLandings))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	// Q1 has 2 coastal landings → kept. Q2 has 0 landings → dropped by the
	// precision gate (cables need ≥2 valid landings to validate route geometry).
	if len(b.Cables) != 1 {
		t.Fatalf("cables = %d, want 1 (Q1 kept, Q2 dropped by gate)", len(b.Cables))
	}
	q1 := b.Cables["Q1"]
	if q1 == nil || q1.Name != "Test Alpha" {
		t.Fatalf("Q1 not loaded correctly: %+v", q1)
	}
	if len(b.Landings["Q1"]) != 2 {
		t.Fatalf("Q1 landings = %d, want 2", len(b.Landings["Q1"]))
	}
	if b.Landings["Q1"][0].Country != "FR" {
		t.Fatalf("Q1 first landing country = %q, want FR", b.Landings["Q1"][0].Country)
	}
	if _, exists := b.Cables["Q2"]; exists {
		t.Fatalf("Q2 should have been dropped by precision gate")
	}
}

// TestPrecisionGateRejectsCountryCentroids confirms the load-time gate
// drops the (-14, -53) Brazil-centroid landings that previously rendered
// as fake "Brazil" PoPs across multiple cables.
func TestPrecisionGateRejectsCountryCentroids(t *testing.T) {
	cables := `{"cable_id":"Qbad","name":"Bad","slug":"bad","alt_names":[],"owners":[],"status":"active","rfs_year":0,"length_km":0,"medium":"fibre","category":"fibre_optic","landing_ids":["Qbad:QBR","Qbad:QIT"],"n_landings":2,"sources":[{"url":"https://www.wikidata.org/wiki/Qbad","license":"CC0","retrieved_at":"2026-04-24"}],"source_count":1}
`
	landings := `{"landing_id":"Qbad:QBR","cable_id":"Qbad","cable_name":"Bad","country":"BR","location_name":"Brazil","lat":-14.0,"lon":-53.0,"sources":[]}
{"landing_id":"Qbad:QIT","cable_id":"Qbad","cable_name":"Bad","country":"IT","location_name":"Italy","lat":42.5,"lon":12.5,"sources":[]}
`
	b, err := LoadBaseline(strings.NewReader(cables), strings.NewReader(landings))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if _, exists := b.Cables["Qbad"]; exists {
		t.Fatalf("cable with two country-centroid landings should be dropped; got %+v", b.Cables["Qbad"])
	}
}

func TestEmbeddedBaselineParses(t *testing.T) {
	b, err := LoadEmbeddedBaseline()
	if err != nil {
		t.Fatalf("LoadEmbeddedBaseline: %v", err)
	}
	// Threshold accounts for the precision gate dropping cables whose
	// only landings are country/region/inland-city centroids. The remaining
	// cables are those with at least one coastal landing — the merge step
	// further requires an OSM polyline match.
	if len(b.Cables) < 60 {
		t.Fatalf("embedded baseline has %d cables, expected >=60", len(b.Cables))
	}
	// Every cable must carry at least one provenance source.
	for id, c := range b.Cables {
		if len(c.Sources) == 0 {
			t.Errorf("cable %s has no sources", id)
		}
	}
}

func TestLoadBaselineRejectsDuplicateIDs(t *testing.T) {
	dup := fixtureCables + `{"cable_id":"Q1","name":"dup","slug":"dup","sources":[]}` + "\n"
	_, err := LoadBaseline(strings.NewReader(dup), strings.NewReader(""))
	if err == nil {
		t.Fatal("expected duplicate cable_id error, got nil")
	}
}
