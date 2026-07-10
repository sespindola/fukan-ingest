package cables

import "testing"

func TestParseOverpassBody(t *testing.T) {
	body := []byte(`{
	  "elements": [
	    {"type":"node","id":1,"lat":10.1,"lon":-20.2},
	    {"type":"node","id":2,"lat":11.1,"lon":-21.2},
	    {"type":"node","id":3,"lat":12.1,"lon":-22.2},
	    {"type":"way","id":100,"timestamp":"2026-04-01T00:00:00Z","nodes":[1,2,3],
	      "tags":{"communication":"line","submarine":"yes","name":"Test Cable","operator":"Example","telecom:medium":"fibre"}},
	    {"type":"way","id":101,"nodes":[1,2],
	      "tags":{"power":"cable","submarine":"yes","name":"Power Cable"}},
	    {"type":"way","id":102,"nodes":[1],
	      "tags":{"communication":"line","seamark:type":"cable_submarine"}}
	  ]
	}`)

	cables, skipped, err := ParseOverpassBody(body)
	if err != nil {
		t.Fatalf("ParseOverpassBody: %v", err)
	}
	if len(cables) != 1 {
		t.Fatalf("cables = %d, want 1", len(cables))
	}
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}
	c := cables[0]
	if c.OSMID != 100 || c.Name != "Test Cable" || c.Operator != "Example" {
		t.Fatalf("unexpected cable metadata: %+v", c)
	}
	if c.Medium != "fibre" || c.Category != "fibre" {
		t.Fatalf("medium/category = %q/%q, want fibre/fibre", c.Medium, c.Category)
	}
	if len(c.Coords) != 6 {
		t.Fatalf("coords len = %d, want 6", len(c.Coords))
	}
}

func TestNameKeyNormalization(t *testing.T) {
	cases := map[string]string{
		"SEA-ME-WE 3":        "sea me we 3",
		"SEA-ME-WE_3":        "sea me we 3",
		"sea me we 3":        "sea me we 3",
		"2Africa":            "2africa",
		"TAT-14 (cable)":     "tat 14 cable",
		"":                   "",
	}
	for in, want := range cases {
		got := nameKey(in)
		if got != want {
			t.Errorf("nameKey(%q) = %q, want %q", in, got, want)
		}
	}
}
