package cables

import (
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// LandingMatchKm: a baseline landing must be within this distance of any
// OSM way coord for the way to count as a candidate match. Permissive
// enough to absorb the gap between Wikidata's city-centroid landings and
// OSM's actual cable-landing-station coords.
const LandingMatchKm = 100.0

// MergeOptions controls the baseline-plus-OSM merge.
type MergeOptions struct {
	Now time.Time
}

// Merge composes cable + landing rows from the baseline JSONL and a live
// OSM Overpass fetch under a hard precision policy:
//
//   - Every emitted cable row has BOTH curated identity (Wikidata baseline)
//     AND validated geometry (an OSM way).
//   - The OSM way must have at least one of the cable's baseline landings
//     within LandingMatchKm of one of its coords (country tiebreak +
//     landing-alignment check).
//   - The OSM polyline must satisfy PolylineClearsLand (no interior land
//     segments outside endpoint approach buffers).
//
// Cables without an OSM match, or whose only OSM match fails country/
// alignment/polyline checks, are dropped. OSM-only orphans (ways with no
// Wikidata baseline) are dropped — they lack curated identity and
// landings, so they fail "precise on landings" by definition.
func Merge(b *Baseline, osm []OSMCable, opt MergeOptions) (rows []CableRow, lands []LandingRow) {
	if opt.Now.IsZero() {
		opt.Now = time.Now().UTC()
	}

	// Index OSM by normalized name. A single name may have multiple
	// candidate ways (cable segments), so the value is a slice.
	osmByName := make(map[string][]*OSMCable, len(osm))
	for i := range osm {
		o := &osm[i]
		key := nameKey(o.Name)
		if key == "" {
			continue
		}
		osmByName[key] = append(osmByName[key], o)
	}

	var (
		emitted          int
		droppedNoOSM     int
		droppedNoNearby  int
		droppedOnLand    int
	)

	for qid, c := range b.Cables {
		landings := b.Landings[qid]

		candidates := collectOSMCandidates(c, osmByName)
		if len(candidates) == 0 {
			droppedNoOSM++
			continue
		}

		// Score each candidate by landing-proximity. Pick the best.
		var (
			best      *OSMCable
			bestScore int
		)
		for _, candidate := range candidates {
			score := countNearbyLandings(candidate, landings, LandingMatchKm)
			if score > bestScore {
				best = candidate
				bestScore = score
			} else if score == bestScore && best != nil &&
				len(candidate.Coords) > len(best.Coords) {
				// Tiebreak on length — longer way usually = more complete cable.
				best = candidate
			}
		}
		if best == nil || bestScore == 0 {
			droppedNoNearby++
			continue
		}

		// Reject ways whose interior crosses land outside endpoint buffers.
		if !PolylineClearsLand(best.Coords) {
			droppedOnLand++
			continue
		}

		row := CableRow{
			CableID:    c.CableID,
			Name:       c.Name,
			Slug:       c.Slug,
			AltNames:   UniqueStrings(c.AltNames),
			Owners:     UniqueStrings(c.Owners),
			Status:     defaultStatus(c.Status),
			RFSYear:    c.RFSYear,
			LengthKM:   c.LengthKM,
			Medium:     defaultMedium(c.Medium),
			Category:   defaultCategory(c.Category),
			Coords:     best.Coords,
			BBoxMinLat: best.BBoxMinLat,
			BBoxMinLon: best.BBoxMinLon,
			BBoxMaxLat: best.BBoxMaxLat,
			BBoxMaxLon: best.BBoxMaxLon,
			OSMWayIDs:  []uint64{best.OSMID},
			UpdatedAt:  opt.Now,
		}

		prov := make([]string, 0, len(c.Sources)+1)
		for _, s := range c.Sources {
			if s.URL != "" {
				prov = append(prov, s.URL)
			}
		}
		prov = append(prov, osmProvenanceURL(best.OSMID))
		row.ProvenanceSourceURLs = UniqueStrings(prov)
		rows = append(rows, row)
		emitted++

		for _, l := range landings {
			sourceURL := ""
			sourceLicense := ""
			if len(l.Sources) > 0 {
				sourceURL = l.Sources[0].URL
				sourceLicense = l.Sources[0].License
			}
			lands = append(lands, LandingRow{
				CableID:      c.CableID,
				CableName:    c.Name,
				Country:      strings.ToUpper(strings.TrimSpace(l.Country)),
				LocationName: l.LocationName,
				Lat:          ScaleCoord(l.Lat),
				Lon:          ScaleCoord(l.Lon),
				Source:       classifySource(sourceLicense),
				SourceURL:    sourceURL,
				RetrievedAt:  opt.Now,
			})
		}
	}

	slog.Info("cable merge complete",
		"baseline_cables", len(b.Cables),
		"osm_ways", len(osm),
		"rows_emitted", emitted,
		"dropped_no_osm_match", droppedNoOSM,
		"dropped_no_landing_in_country", droppedNoNearby,
		"dropped_polyline_on_land", droppedOnLand,
	)
	return rows, lands
}

// countNearbyLandings returns the number of baseline landings whose
// coordinate sits within maxKm of any coord on the OSM way. Used as the
// cable's landing-alignment + country-tiebreak score during candidate
// selection.
func countNearbyLandings(osm *OSMCable, landings []*BaselineLanding, maxKm float64) int {
	if len(osm.Coords) < 2 || len(landings) == 0 {
		return 0
	}
	count := 0
	for _, l := range landings {
		if isLandingNearWay(l.Lat, l.Lon, osm, maxKm) {
			count++
		}
	}
	return count
}

func isLandingNearWay(lat, lon float64, osm *OSMCable, maxKm float64) bool {
	n := len(osm.Coords) / 2
	if n < 2 {
		return false
	}
	// Endpoints are most likely to be near landings.
	p0Lat := DescaleCoord(osm.Coords[0])
	p0Lon := DescaleCoord(osm.Coords[1])
	pNLat := DescaleCoord(osm.Coords[2*(n-1)])
	pNLon := DescaleCoord(osm.Coords[2*(n-1)+1])
	if haversineKm(lat, lon, p0Lat, p0Lon) <= maxKm {
		return true
	}
	if haversineKm(lat, lon, pNLat, pNLon) <= maxKm {
		return true
	}
	// Then check intermediate inflection points (every coord — n is small).
	for i := 1; i < n-1; i++ {
		plat := DescaleCoord(osm.Coords[2*i])
		plon := DescaleCoord(osm.Coords[2*i+1])
		if haversineKm(lat, lon, plat, plon) <= maxKm {
			return true
		}
	}
	return false
}

// nameCandidates returns the list of names worth fuzzy-matching against OSM.
func nameCandidates(c *BaselineCable) []string {
	out := []string{c.Name, c.Slug}
	out = append(out, c.AltNames...)
	return out
}

// minPrefixMatchLen is the minimum length a baseline nameKey must have to
// be allowed as a prefix-match against OSM names. Below this we'd produce
// too many false positives ("AC" prefix-matching "AC-1", "AC-2", "ACCEL", ...).
const minPrefixMatchLen = 4

// collectOSMCandidates finds OSM ways whose name plausibly matches one of
// the baseline cable's names. Three strategies, in order of strictness:
//
//  1. Exact `nameKey` match — handles "Atlantic Crossing 1" ↔ "Atlantic Crossing 1".
//  2. Roman-numeral-normalized exact match — handles "Americas II" ↔
//     OSM's "Americas 2" / "Americas-2".
//  3. Word-prefix match (baseline nameKey is the leading word(s) of an OSM
//     nameKey, followed by a space) — handles multi-segment cables in OSM
//     like "Apollo" ↔ "Apollo South" / "Apollo North", or
//     "Atlantic Crossing 1" ↔ "Atlantic Crossing 1 (AC1) Seg.A".
//
// All candidates are then re-validated by the country/landing-alignment
// score and PolylineClearsLand, so loose matches don't survive.
func collectOSMCandidates(c *BaselineCable, osmByName map[string][]*OSMCable) []*OSMCable {
	seen := make(map[uint64]bool)
	var out []*OSMCable

	tryKey := func(k string, allowPrefix bool) {
		if k == "" {
			return
		}
		// Exact match.
		for _, m := range osmByName[k] {
			if !seen[m.OSMID] {
				seen[m.OSMID] = true
				out = append(out, m)
			}
		}
		// Word-prefix match (only with sufficient length).
		if allowPrefix && len(k) >= minPrefixMatchLen {
			pfx := k + " "
			for osmK, ms := range osmByName {
				if strings.HasPrefix(osmK, pfx) {
					for _, m := range ms {
						if !seen[m.OSMID] {
							seen[m.OSMID] = true
							out = append(out, m)
						}
					}
				}
			}
		}
	}

	for _, name := range nameCandidates(c) {
		tryKey(nameKey(name), true)
		// Roman-numeral-normalized variant (catches "II" → "2" mismatches).
		if rn := romanToArabic(name); rn != name {
			tryKey(nameKey(rn), true)
		}
	}
	return out
}

// romanRE matches standalone Roman-numeral tokens (I, II, III, IV, V, VI,
// VII, VIII, IX, X) at word boundaries. Cable-name conventions rarely use
// Roman numerals beyond X.
var romanRE = regexp.MustCompile(`(?i)\b(VIII|VII|VI|IV|IX|III|II|I|X|V)\b`)

var romanMap = map[string]string{
	"I": "1", "II": "2", "III": "3", "IV": "4", "V": "5",
	"VI": "6", "VII": "7", "VIII": "8", "IX": "9", "X": "10",
}

func romanToArabic(s string) string {
	return romanRE.ReplaceAllStringFunc(s, func(m string) string {
		if n, ok := romanMap[strings.ToUpper(m)]; ok {
			return n
		}
		return m
	})
}

// nameKey normalizes a cable name for fuzzy comparison with OSM's `name`
// tag: lowercase, strip punctuation, collapse whitespace.
func nameKey(name string) string {
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(name))
	prevSpace := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevSpace = false
		case r == '-' || r == ' ' || r == '_' || r == '.':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func osmProvenanceURL(id uint64) string {
	return "https://www.openstreetmap.org/way/" + itoa(id)
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func defaultStatus(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func defaultMedium(s string) string {
	if s == "" {
		return "fibre"
	}
	return s
}

func defaultCategory(s string) string {
	if s == "" {
		return "fibre_optic"
	}
	return s
}

func classifySource(license string) string {
	switch {
	case strings.HasPrefix(license, "CC0"):
		return "wikidata"
	case strings.Contains(license, "CC-BY-SA"):
		return "wikipedia"
	case strings.Contains(strings.ToLower(license), "public"):
		return "public"
	default:
		return "baseline"
	}
}
