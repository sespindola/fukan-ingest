// Package cables holds the clean-room submarine cable dataset pipeline:
// a committed JSONL baseline (built from Wikidata + Wikipedia) augmented
// with live OSM geometry, merged into one row per cable for ClickHouse.
//
// The Go CableRow struct, the Rails ViewportQuery SELECT list, and the
// TS CableSegment type must stay aligned; see:
//   fukan-web/app/services/cable/viewport_query.rb
//   fukan-web/app/frontend/types/telemetry.ts
package cables

import (
	"math"
	"regexp"
	"strings"
	"time"
)

// CoordScale is the Int32-per-degree factor used in ClickHouse storage.
const CoordScale = 10_000_000

// ProvenanceSource records where a fact came from. Embedded in the JSONL
// baseline and surfaced as provenance_source_urls in ClickHouse.
type ProvenanceSource struct {
	URL         string `json:"url"`
	License     string `json:"license"`
	RetrievedAt string `json:"retrieved_at"`
}

// BaselineCable is one record in data/cables.jsonl.
type BaselineCable struct {
	CableID      string             `json:"cable_id"`
	Name         string             `json:"name"`
	Slug         string             `json:"slug"`
	AltNames     []string           `json:"alt_names"`
	Owners       []string           `json:"owners"`
	Status       string             `json:"status"`
	RFSYear      uint16             `json:"rfs_year"`
	LengthKM     uint32             `json:"length_km"`
	Medium       string             `json:"medium"`
	Category     string             `json:"category"`
	LandingIDs   []string           `json:"landing_ids"`
	NLandings    int                `json:"n_landings"`
	WPSection    *string            `json:"wikipedia_section,omitempty"`
	Sources      []ProvenanceSource `json:"sources"`
	SourceCount  int                `json:"source_count"`
}

// BaselineLanding is one record in data/landings.jsonl.
type BaselineLanding struct {
	LandingID    string             `json:"landing_id"`
	CableID      string             `json:"cable_id"`
	CableName    string             `json:"cable_name"`
	Country      string             `json:"country"`
	LocationName string             `json:"location_name"`
	Lat          float64            `json:"lat"`
	Lon          float64            `json:"lon"`
	Sources      []ProvenanceSource `json:"sources"`
}

// CableRow is the composite record that lands in ClickHouse (one row per
// cable_id). Built by merge.go from baseline + OSM fragments. Every row
// has both curated identity (Wikidata) AND validated geometry (OSM way
// passing PolylineClearsLand) — there is no longer an "approximate"
// great-circle synthesis path.
type CableRow struct {
	CableID              string
	Name                 string
	Slug                 string
	AltNames             []string
	Owners               []string
	Status               string
	RFSYear              uint16
	LengthKM             uint32
	Medium               string
	Category             string
	Coords               []int32 // alternating lat,lon * CoordScale
	BBoxMinLat           int32
	BBoxMinLon           int32
	BBoxMaxLat           int32
	BBoxMaxLon           int32
	ProvenanceSourceURLs []string
	OSMWayIDs            []uint64
	UpdatedAt            time.Time
}

// LandingRow is what lands in the cable_landings ClickHouse table.
type LandingRow struct {
	CableID      string
	CableName    string
	Country      string
	LocationName string
	Lat          int32
	Lon          int32
	Source       string
	SourceURL    string
	RetrievedAt  time.Time
}

// ScaleCoord converts a degree value to the Int32 * CoordScale
// representation used in ClickHouse.
func ScaleCoord(deg float64) int32 {
	return int32(math.Round(deg * CoordScale))
}

// DescaleCoord converts an Int32 * CoordScale back to degrees.
func DescaleCoord(v int32) float64 {
	return float64(v) / CoordScale
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify produces a stable kebab-case slug from a cable name.
func Slugify(name string) string {
	s := slugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "cable"
	}
	return s
}

// UniqueStrings returns s without duplicates, preserving first-seen order.
func UniqueStrings(s []string) []string {
	seen := make(map[string]struct{}, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
