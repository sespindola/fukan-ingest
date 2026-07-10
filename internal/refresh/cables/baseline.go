package cables

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

//go:embed data/cables.jsonl
var embeddedCablesJSONL []byte

//go:embed data/landings.jsonl
var embeddedLandingsJSONL []byte

// Baseline is the parsed, in-memory view of the committed JSONL files.
// Keys are cable_id.
type Baseline struct {
	Cables   map[string]*BaselineCable
	Landings map[string][]*BaselineLanding // cable_id -> landings
}

// LoadEmbeddedBaseline parses the JSONL files that ship with the binary.
func LoadEmbeddedBaseline() (*Baseline, error) {
	return LoadBaseline(bytes.NewReader(embeddedCablesJSONL), bytes.NewReader(embeddedLandingsJSONL))
}

// LoadBaseline parses cables.jsonl and landings.jsonl from the given
// readers. Used by tests to inject fixtures.
func LoadBaseline(cablesR, landingsR io.Reader) (*Baseline, error) {
	b := &Baseline{
		Cables:   make(map[string]*BaselineCable),
		Landings: make(map[string][]*BaselineLanding),
	}

	cScan := bufio.NewScanner(cablesR)
	cScan.Buffer(make([]byte, 1024*1024), 1024*1024)
	var lineNo int
	for cScan.Scan() {
		lineNo++
		line := bytes.TrimSpace(cScan.Bytes())
		if len(line) == 0 {
			continue
		}
		var c BaselineCable
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("cables.jsonl line %d: %w", lineNo, err)
		}
		if c.CableID == "" {
			return nil, fmt.Errorf("cables.jsonl line %d: missing cable_id", lineNo)
		}
		if _, dup := b.Cables[c.CableID]; dup {
			return nil, fmt.Errorf("cables.jsonl line %d: duplicate cable_id %q", lineNo, c.CableID)
		}
		b.Cables[c.CableID] = &c
	}
	if err := cScan.Err(); err != nil {
		return nil, fmt.Errorf("scan cables.jsonl: %w", err)
	}

	lScan := bufio.NewScanner(landingsR)
	lScan.Buffer(make([]byte, 1024*1024), 1024*1024)
	lineNo = 0
	for lScan.Scan() {
		lineNo++
		line := bytes.TrimSpace(lScan.Bytes())
		if len(line) == 0 {
			continue
		}
		var l BaselineLanding
		if err := json.Unmarshal(line, &l); err != nil {
			return nil, fmt.Errorf("landings.jsonl line %d: %w", lineNo, err)
		}
		if l.CableID == "" {
			return nil, fmt.Errorf("landings.jsonl line %d: missing cable_id", lineNo)
		}
		b.Landings[l.CableID] = append(b.Landings[l.CableID], &l)
	}
	if err := lScan.Err(); err != nil {
		return nil, fmt.Errorf("scan landings.jsonl: %w", err)
	}

	slog.Info("baseline loaded",
		"cables", len(b.Cables),
		"landings_total", totalLandings(b.Landings),
		"cables_with_landings", cablesWithLandings(b.Landings),
	)

	applyPrecisionGate(b)
	return b, nil
}

// applyPrecisionGate drops landings whose coords fail IsLandingCoord (i.e.
// >LandingBufferKm from any coastline edge — country/region centroids,
// deep-inland points). Cables with 0 valid landings remaining are dropped
// since they no longer have any country tiebreak signal for OSM matching.
//
// Cables with 1+ valid landings are kept here — the merge step is the
// hard gate that drops them if no precise OSM polyline can be matched.
//
// This is belt-and-suspenders against any future regression in the Python
// builder. Logs a structured drop report.
func applyPrecisionGate(b *Baseline) {
	cablesIn := len(b.Cables)
	landingsIn := totalLandings(b.Landings)

	var droppedLandings []string
	for cableID, ls := range b.Landings {
		kept := ls[:0]
		for _, l := range ls {
			if IsLandingCoord(l.Lat, l.Lon) {
				kept = append(kept, l)
				continue
			}
			if len(droppedLandings) < 8 {
				droppedLandings = append(droppedLandings,
					fmt.Sprintf("%s/%s(%.3f,%.3f)", l.CableName, l.LocationName, l.Lat, l.Lon))
			}
		}
		b.Landings[cableID] = kept
	}

	var droppedCables []string
	for cableID := range b.Cables {
		if len(b.Landings[cableID]) == 0 {
			delete(b.Cables, cableID)
			delete(b.Landings, cableID)
			if len(droppedCables) < 8 {
				droppedCables = append(droppedCables, cableID)
			}
		}
	}

	slog.Info("baseline precision gate applied",
		"cables_in", cablesIn,
		"cables_out", len(b.Cables),
		"landings_in", landingsIn,
		"landings_out", totalLandings(b.Landings),
		"dropped_landing_examples", droppedLandings,
		"dropped_cable_examples", droppedCables,
		"coastline_polys", CoastlinePolygonCount(),
		"landing_buffer_km", LandingBufferKm,
	)
}

func totalLandings(m map[string][]*BaselineLanding) int {
	n := 0
	for _, ls := range m {
		n += len(ls)
	}
	return n
}

func cablesWithLandings(m map[string][]*BaselineLanding) int {
	n := 0
	for _, ls := range m {
		if len(ls) > 0 {
			n++
		}
	}
	return n
}
