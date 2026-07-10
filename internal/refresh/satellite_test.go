package refresh

import (
	"strings"
	"testing"
)

func TestParseGCATOrgNames(t *testing.T) {
	input := strings.Join([]string{
		"#Code\tUCode\tStateCode\tType\tClass\tTStart\tTStop\tShortName\tName\tLocation\tLongitude\tLatitude\tError\tParent\tShortEName\tEName\tUName",
		"# Updated 2026 Apr 23 2252:24",
		"US\tUS\tUS\tCY\tC\t1776 Jul  4\t-\tUSA\tUnited States of America\tWashington, DC\t-77.0200\t38.9000\t0.0200 \t-\tUSA\tUnited States of America\tUnited States of America",
		"SPXS\tSPXS\tUS\tO/PL\tB\t2015 Jun\t-\tSpaceX/Seattle\tSpaceX (Seattle)\tSeattle:Redmond, Washington\t-122.1200\t47.6700\t0.0200 \t-\t-\t-\tSpaceX (Seattle)",
	}, "\n")

	orgNames, err := parseGCATOrgNames(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseGCATOrgNames: %v", err)
	}

	if got := orgNames["US"]; got != "United States of America" {
		t.Errorf("US = %q, want United States of America", got)
	}
	if got := orgNames["SPXS"]; got != "SpaceX (Seattle)" {
		t.Errorf("SPXS = %q, want SpaceX (Seattle)", got)
	}
}

func TestHumanizeSatStatus(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "O", want: "In orbit"},
		{in: "R", want: "Reentered"},
		{in: "AO IN", want: "Attached inside"},
		{in: "UNKNOWN", want: "UNKNOWN"},
	}

	for _, tt := range tests {
		if got := humanizeSatStatus(tt.in); got != tt.want {
			t.Errorf("humanizeSatStatus(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHumanizeSatType(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "P", want: "Payload"},
		{in: "P      R", want: "Payload; Satellite active orbit lowering to reentry"},
		{in: "P      M k", want: "Payload; Satellite failed in operational orbit and decaying uncontrolled; Annotation color: black"},
	}

	for _, tt := range tests {
		if got := humanizeSatType(tt.in); got != tt.want {
			t.Errorf("humanizeSatType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseSatRecordHumanizesCatalogCodes(t *testing.T) {
	header := "#JCAT\tSatcat\tName\tOwner\tState\tType\tLDate\tMass\tPerigee\tApogee\tInc\tStatus"
	indices := buildSatHeaderIndex(header)
	orgNames := map[string]string{
		"SPXS": "SpaceX (Seattle)",
		"US":   "United States of America",
	}

	line := "S44240\t44240\tStarlink 26\tSPXS\tUS\tP      M k\t2019 May 24\t227\t440\t550\t53.00\tR"
	meta, ok := parseSatRecord(line, indices, orgNames)
	if !ok {
		t.Fatal("parseSatRecord returned false")
	}

	if meta.Owner != "SpaceX (Seattle)" {
		t.Errorf("Owner = %q, want SpaceX (Seattle)", meta.Owner)
	}
	if meta.State != "United States of America" {
		t.Errorf("State = %q, want United States of America", meta.State)
	}
	if meta.ObjectType != "Payload; Satellite failed in operational orbit and decaying uncontrolled; Annotation color: black" {
		t.Errorf("ObjectType = %q", meta.ObjectType)
	}
	if meta.Status != "Reentered" {
		t.Errorf("Status = %q, want Reentered", meta.Status)
	}
}
