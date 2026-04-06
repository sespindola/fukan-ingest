package refresh

import (
	"encoding/csv"
	"io"
	"os"
	"strings"
	"testing"
)

func TestBuildHeaderIndex(t *testing.T) {
	header := []string{"icao24", "registration", "manufacturerName", "model", "typecode", "unknown_col", "operator", "operatorIcao"}
	idx := buildHeaderIndex(header)

	// Should map known columns and skip unknown ones.
	if len(idx) != 7 {
		t.Fatalf("expected 7 mapped columns, got %d", len(idx))
	}
	if _, ok := idx[5]; ok {
		t.Error("unknown_col should not be in index")
	}
}

func TestBuildHeaderIndexReordered(t *testing.T) {
	// Columns in a different order than expected.
	header := []string{"operator", "icao24", "model"}
	idx := buildHeaderIndex(header)

	var meta AircraftMeta
	idx[0](&meta, "Lufthansa")
	idx[1](&meta, "3c6444")
	idx[2](&meta, "A320")

	if meta.ICAO24 != "3C6444" {
		t.Errorf("expected ICAO24 3C6444, got %s", meta.ICAO24)
	}
	if meta.Operator != "Lufthansa" {
		t.Errorf("expected operator Lufthansa, got %s", meta.Operator)
	}
	if meta.Model != "A320" {
		t.Errorf("expected model A320, got %s", meta.Model)
	}
}

func TestParseRecord(t *testing.T) {
	header := []string{"icao24", "registration", "operator", "operatorIcao", "operatorIata", "model"}
	idx := buildHeaderIndex(header)

	record := []string{"3c6444", "D-AIZZ", "Lufthansa", "DLH", "LH", "A320-214"}
	meta := parseRecord(record, idx)

	if meta.ICAO24 != "3C6444" {
		t.Errorf("expected 3C6444, got %s", meta.ICAO24)
	}
	if meta.Registration != "D-AIZZ" {
		t.Errorf("expected D-AIZZ, got %s", meta.Registration)
	}
	if meta.Operator != "Lufthansa" {
		t.Errorf("expected Lufthansa, got %s", meta.Operator)
	}
	if meta.OperatorICAO != "DLH" {
		t.Errorf("expected DLH, got %s", meta.OperatorICAO)
	}
	if meta.OperatorIATA != "LH" {
		t.Errorf("expected LH, got %s", meta.OperatorIATA)
	}
	if meta.Model != "A320-214" {
		t.Errorf("expected A320-214, got %s", meta.Model)
	}
}

func TestParseRecordEmptyICAO(t *testing.T) {
	header := []string{"icao24", "registration"}
	idx := buildHeaderIndex(header)

	record := []string{"", "D-AIZZ"}
	meta := parseRecord(record, idx)

	if meta.ICAO24 != "" {
		t.Errorf("expected empty ICAO24, got %s", meta.ICAO24)
	}
}

func TestParseRecordTrimming(t *testing.T) {
	header := []string{"icao24", "operator"}
	idx := buildHeaderIndex(header)

	record := []string{"  3c6444  ", "  Lufthansa  "}
	meta := parseRecord(record, idx)

	if meta.ICAO24 != "3C6444" {
		t.Errorf("expected trimmed 3C6444, got %q", meta.ICAO24)
	}
	if meta.Operator != "Lufthansa" {
		t.Errorf("expected trimmed Lufthansa, got %q", meta.Operator)
	}
}

func TestParseRecordShortRow(t *testing.T) {
	header := []string{"icao24", "registration", "operator", "model"}
	idx := buildHeaderIndex(header)

	// Row has fewer columns than header — should not panic.
	record := []string{"3c6444", "D-AIZZ"}
	meta := parseRecord(record, idx)

	if meta.ICAO24 != "3C6444" {
		t.Errorf("expected 3C6444, got %s", meta.ICAO24)
	}
	if meta.Registration != "D-AIZZ" {
		t.Errorf("expected D-AIZZ, got %s", meta.Registration)
	}
	if meta.Operator != "" {
		t.Errorf("expected empty operator, got %s", meta.Operator)
	}
}

func TestSingleToDoubleQuoteReader(t *testing.T) {
	input := "'hello','world'\n'foo','bar'\n"
	r := &singleToDoubleQuoteReader{r: strings.NewReader(input)}
	buf, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	want := "\"hello\",\"world\"\n\"foo\",\"bar\"\n"
	if string(buf) != want {
		t.Errorf("got %q, want %q", string(buf), want)
	}
}

// TestParseLocalCSV validates parsing against the real OpenSky CSV file.
// Skipped if the file is not present (CI).
func TestParseLocalCSV(t *testing.T) {
	const csvPath = "../../aircraft-database-complete-2025-08.csv"
	if _, err := os.Stat(csvPath); err != nil {
		t.Skipf("skipping: local CSV not found at %s", csvPath)
	}

	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("open csv: %v", err)
	}
	defer f.Close()

	reader := csv.NewReader(&singleToDoubleQuoteReader{r: f})
	reader.ReuseRecord = true

	header, err := reader.Read()
	if err != nil {
		t.Fatalf("read csv header: %v", err)
	}
	indices := buildHeaderIndex(header)
	if len(indices) == 0 {
		t.Fatalf("no columns matched from header: %v", header)
	}

	var parsed, skipped int
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			skipped++
			continue
		}
		meta := parseRecord(record, indices)
		if meta.ICAO24 == "" {
			skipped++
			continue
		}
		if meta.ImageURL != "" || meta.ImageAttribution != "" {
			t.Fatalf("image fields should be empty from CSV parse, got url=%q attr=%q", meta.ImageURL, meta.ImageAttribution)
		}
		parsed++
	}

	t.Logf("parsed=%d skipped=%d", parsed, skipped)

	if parsed == 0 {
		t.Fatal("expected >0 parsed rows, got 0")
	}
	if skipped > parsed/100 {
		t.Errorf("too many skipped rows (>1%%): parsed=%d skipped=%d", parsed, skipped)
	}
}
