package model

type AssetType string

const (
	AssetAircraft  AssetType = "aircraft"
	AssetVessel    AssetType = "vessel"
	AssetSatellite AssetType = "satellite"
	AssetBGPNode   AssetType = "bgp_node"
	AssetNews      AssetType = "news"
)

// FukanEvent is the canonical normalized event.
// Every ETL worker MUST produce this struct. No exceptions.
type FukanEvent struct {
	Timestamp int64     `json:"ts"`   // Unix epoch milliseconds
	AssetID   string    `json:"id"`   // ICAO hex, MMSI, NORAD ID, ASN, or event hash
	AssetType AssetType `json:"type"` // aircraft, vessel, satellite, bgp_node, news
	Lat       int32     `json:"lat"`  // latitude * 10_000_000
	Lon       int32     `json:"lon"`  // longitude * 10_000_000
	Alt       int32     `json:"alt"`  // meters above sea level (0 for surface/network)
	Speed     float32   `json:"spd"`  // knots (aircraft/vessel) or 0
	Heading   float32   `json:"hdg"`  // degrees (0-360) or 0
	H3Cell    uint64    `json:"h3"`   // pre-computed H3 index at resolution 7
	Source    string    `json:"src"`  // provider identifier (e.g. "adsb_exchange")
	Metadata  string    `json:"meta"` // JSON blob, type-specific (squawk, nav_status, etc.)
}
