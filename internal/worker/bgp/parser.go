package bgp

import (
	"encoding/json"
	"fmt"
	"hash/fnv"

	"github.com/sespindola/fukan-ingest/internal/coord"
	"github.com/sespindola/fukan-ingest/internal/model"
)

// RIS Live message envelope. See https://ris-live.ripe.net/
type risEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type risMessage struct {
	Timestamp     float64           `json:"timestamp"`
	Peer          string            `json:"peer"`
	PeerASN       string            `json:"peer_asn"`
	ID            string            `json:"id"`
	Host          string            `json:"host"`
	Type          string            `json:"type"`
	// Raw path — elements are either a bare ASN (number) or an AS_SET
	// (nested array of ASNs). Use flattenPath to normalize.
	Path          []json.RawMessage `json:"path"`
	Announcements []risAnnouncement `json:"announcements"`
	Withdrawals   []string          `json:"withdrawals"`
}

// flattenPath normalizes a RIS Live path — whose elements are either a
// bare ASN or a nested AS_SET array — into a flat []uint32. AS_SETs
// (aggregate-route contributors per RFC 4271 §4.3) are expanded in-order;
// we lose the set semantics but keep each contributing AS visible for
// leak detection and geographic resolution.
func flattenPath(raw []json.RawMessage) []uint32 {
	out := make([]uint32, 0, len(raw))
	for _, el := range raw {
		var asn uint32
		if err := json.Unmarshal(el, &asn); err == nil {
			out = append(out, asn)
			continue
		}
		var set []uint32
		if err := json.Unmarshal(el, &set); err == nil {
			out = append(out, set...)
		}
		// Anything else is silently dropped — malformed paths shouldn't
		// break the whole message.
	}
	return out
}

type risAnnouncement struct {
	NextHop  string   `json:"next_hop"`
	Prefixes []string `json:"prefixes"`
}

// cityLookup resolves a prefix to (lat, lon) via MaxMind's GeoLite2-City.
// Tests can pass nil (no MMDB) or a fake; the real worker injects
// *CityLookup.
type cityLookup interface {
	LookupPrefix(prefix string) (lat, lon float64, ok bool)
}

// asnLookup resolves a prefix to its registered holder ASN + org name
// via MaxMind's GeoLite2-ASN. Same nil-safe pattern as cityLookup.
type asnLookup interface {
	LookupPrefix(prefix string) (asn uint32, org string, ok bool)
}

// resolvePrefixOrg returns the MMDB-reported (ASN, org) for the prefix,
// or (0, "", false) on any miss. Never drops an event — the caller just
// stamps zero values on the output.
func resolvePrefixOrg(asn asnLookup, prefix string) (uint32, string, bool) {
	if asn == nil {
		return 0, "", false
	}
	return asn.LookupPrefix(prefix)
}

// resolveCoords tries the MMDB city lookup first, falling back to
// geoFromPath (ASN → country-centroid). Returns ok=false when both miss —
// the caller should drop the event rather than emit null-island coords.
func resolveCoords(city cityLookup, geo *Geo, prefix string, path []uint32) (float64, float64, bool) {
	if city != nil {
		if lat, lon, ok := city.LookupPrefix(prefix); ok {
			return lat, lon, true
		}
	}
	return geoFromPath(geo, path)
}

// resolveWithdrawalCoords is resolveCoords's analogue for withdrawals,
// where we have no AS path. Fallback is a peer-ASN lookup.
func resolveWithdrawalCoords(city cityLookup, geo *Geo, prefix string, peerASN uint32) (float64, float64, bool) {
	if city != nil {
		if lat, lon, ok := city.LookupPrefix(prefix); ok {
			return lat, lon, true
		}
	}
	_, lat, lon, ok := geo.Lookup(peerASN)
	return lat, lon, ok
}

// ParseMessage decodes a RIS Live envelope and, after filtering, returns any
// BgpEvents that should be published. Events whose coordinates cannot be
// resolved from either the MMDB or the ASN-centroid Geo are dropped.
func ParseMessage(data []byte, source string, geo *Geo, state *PrefixState, city cityLookup, asn asnLookup) ([]model.BgpEvent, error) {
	var env risEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	if env.Type != "ris_message" {
		return nil, nil
	}

	var msg risMessage
	if err := json.Unmarshal(env.Data, &msg); err != nil {
		return nil, fmt.Errorf("ris_message: %w", err)
	}
	if msg.Type != "UPDATE" {
		return nil, nil
	}

	tsMillis := int64(msg.Timestamp * 1000)
	events := make([]model.BgpEvent, 0, 4)
	path := flattenPath(msg.Path)

	// Announcements: one event per prefix, but only if it's a new prefix or
	// an origin change (the latter is the hijack heuristic). Plain
	// re-announcements get dropped.
	if len(path) > 0 {
		originAS := path[len(path)-1]
		isLeak := detectLeak(path)

		for _, a := range msg.Announcements {
			for _, prefix := range a.Prefixes {
				prev, known := state.Observe(prefix, originAS)

				var category string
				switch {
				case isLeak:
					category = "leak"
				case known && prev != originAS:
					category = "hijack"
				case !known:
					category = "announcement"
				default:
					continue // boring re-announcement
				}

				lat, lon, ok := resolveCoords(city, geo, prefix, path)
				if !ok {
					continue
				}

				prefixAS, prefixOrg, _ := resolvePrefixOrg(asn, prefix)

				events = append(events, buildEvent(buildEventArgs{
					tsMillis:   tsMillis,
					prefix:     prefix,
					category:   category,
					originAS:   originAS,
					prefixAS:   prefixAS,
					prefixOrg:  prefixOrg,
					asPath:     path,
					pathCoords: resolvePathCoords(geo, path),
					collector:  msg.Host,
					lat:        lat,
					lon:        lon,
					source:     source,
				}))
			}
		}
	}

	// Withdrawals: one event per prefix, but only if we'd seen it before
	// (unknown withdrawals are just RIB cleanup noise).
	for _, prefix := range msg.Withdrawals {
		if !state.Known(prefix) {
			continue
		}
		state.Forget(prefix)

		peerASN, ok := ParseASN(msg.PeerASN)
		if !ok {
			continue
		}
		lat, lon, ok := resolveWithdrawalCoords(city, geo, prefix, peerASN)
		if !ok {
			continue
		}

		prefixAS, prefixOrg, _ := resolvePrefixOrg(asn, prefix)

		events = append(events, buildEvent(buildEventArgs{
			tsMillis:  tsMillis,
			prefix:    prefix,
			category:  "withdrawal",
			originAS:  peerASN,
			prefixAS:  prefixAS,
			prefixOrg: prefixOrg,
			asPath:    nil,
			collector: msg.Host,
			lat:       lat,
			lon:       lon,
			source:    source,
		}))
	}

	return events, nil
}

// geoFromPath returns the first AS in the path (right-to-left, i.e. starting
// at the origin) whose geolocation is known. Origin-first ordering means the
// point lands near the physical owner of the prefix rather than at a random
// transit hop.
func geoFromPath(g *Geo, path []uint32) (lat, lon float64, ok bool) {
	for i := len(path) - 1; i >= 0; i-- {
		if _, la, lo, found := g.Lookup(path[i]); found {
			return la, lo, true
		}
	}
	return 0, 0, false
}

// resolvePathCoords walks the full AS path and returns alternating
// scaled-lat/scaled-lon pairs for each hop that the Geo cache can resolve.
// Unknown hops are skipped — they reach the frontend as a shorter polyline
// rather than a misleading null-island detour. On first sight of an unknown
// ASN, Geo fires a background RIPEstat fetch; subsequent events for the
// same path render more completely as the cache warms up.
func resolvePathCoords(g *Geo, path []uint32) []int32 {
	if len(path) == 0 {
		return nil
	}
	coords := make([]int32, 0, len(path)*2)
	for _, asn := range path {
		if _, lat, lon, ok := g.Lookup(asn); ok {
			coords = append(coords, coord.ScaleLat(lat), coord.ScaleLon(lon))
		}
	}
	return coords
}

// detectLeak implements the "Tier-1 downstream of non-Tier-1" heuristic: if
// any Tier-1 appears in the AS-path preceded by a non-Tier-1 (reading
// left-to-right, so Tier-1 is further from the origin), that's a route leak
// because Tier-1s should only appear at the top of the transit cone.
func detectLeak(path []uint32) bool {
	sawNonTier1 := false
	for _, asn := range path {
		if tier1ASNs[asn] {
			if sawNonTier1 {
				return true
			}
		} else {
			sawNonTier1 = true
		}
	}
	return false
}

type buildEventArgs struct {
	tsMillis   int64
	prefix     string
	category   string
	originAS   uint32
	prefixAS   uint32
	prefixOrg  string
	asPath     []uint32
	pathCoords []int32
	collector  string
	lat        float64
	lon        float64
	source     string
}

func buildEvent(a buildEventArgs) model.BgpEvent {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%s|%s|%d", a.tsMillis, a.prefix, a.category, a.originAS)
	eventID := fmt.Sprintf("bgp-%x", h.Sum64())

	h3cell, _ := coord.ComputeH3(a.lat, a.lon)

	return model.BgpEvent{
		Timestamp:  a.tsMillis,
		EventID:    eventID,
		Category:   a.category,
		Prefix:     a.prefix,
		OriginAS:   a.originAS,
		PrefixAS:   a.prefixAS,
		PrefixOrg:  a.prefixOrg,
		ASPath:     a.asPath,
		PathCoords: a.pathCoords,
		Collector:  a.collector,
		Lat:        coord.ScaleLat(a.lat),
		Lon:        coord.ScaleLon(a.lon),
		H3Cell:     h3cell,
		Source:     a.source,
	}
}
