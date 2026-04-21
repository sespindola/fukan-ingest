package model

// BgpEvent is a discrete BGP routing happening — an announcement,
// withdrawal, hijack, or route leak — emitted by the RIS Live worker.
// Unlike FukanEvent, this is event-stream data: each instance is a
// one-time occurrence, not an update to long-lived asset state. It
// flows through its own NATS subject (fukan.bgp.events), ClickHouse
// table (bgp_events), and AnyCable stream prefix (bgp:<h3>).
//
// SCHEMA DRIFT HAZARD: these `json:"..."` tags MUST match the column
// aliases in fukan-web's app/services/bgp/viewport_query.rb AND the
// BgpEvent interface in app/frontend/types/telemetry.ts. If they
// drift, bootstrap payloads and live broadcasts will silently disagree
// on field names and the frontend will read `undefined`. The newest
// fields — prefix_as and prefix_org — are especially easy to miss
// because they were added after the initial schema landed.
type BgpEvent struct {
	Timestamp  int64    `json:"ts"`          // Unix epoch milliseconds
	EventID    string   `json:"id"`          // fnv64 hash of (ts|prefix|category|originAS)
	Category   string   `json:"cat"`         // announcement | withdrawal | hijack | leak
	Prefix     string   `json:"prefix"`      // CIDR, e.g. "8.8.8.0/24"
	OriginAS   uint32   `json:"origin_as"`   // AS currently announcing the prefix (from RIS Live)
	PrefixAS   uint32   `json:"prefix_as"`   // registered holder AS (GeoLite2-ASN prefix lookup); may differ from OriginAS on hijacks
	PrefixOrg  string   `json:"prefix_org"`  // registered holder organization name; empty when MMDB has no record
	ASPath     []uint32 `json:"as_path"`     // full AS-path hops
	PathCoords []int32  `json:"path_coords"` // per-hop lat/lon pairs (scaled x10^7), alternating; unresolved hops skipped
	Collector  string   `json:"collector"`   // RIS collector ID (rrc00, rrc01, ...)
	Lat        int32    `json:"lat"`         // latitude  * 10_000_000
	Lon        int32    `json:"lon"`         // longitude * 10_000_000
	H3Cell     uint64   `json:"h3"`          // pre-computed H3 index at resolution 7
	Source     string   `json:"src"`         // provider identifier (e.g. "ris-live")
}
