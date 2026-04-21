package model

type AssetType string

const (
	AssetAircraft  AssetType = "aircraft"
	AssetVessel    AssetType = "vessel"
	AssetSatellite AssetType = "satellite"
	AssetBGPNode   AssetType = "bgp_node"
	AssetNews      AssetType = "news"
)

// FukanEvent is the canonical normalized event for moving assets
// (aircraft, vessels, satellites). BGP events DO NOT flow through this
// struct — they use BgpEvent and their own NATS subject / ClickHouse
// table / AnyCable stream prefix. See bgp_event.go.
type FukanEvent struct {
	Timestamp    int64     `json:"ts"`       // Unix epoch milliseconds
	AssetID      string    `json:"id"`       // ICAO hex, MMSI, NORAD ID
	AssetType    AssetType `json:"type"`     // aircraft, vessel, satellite
	Callsign     string    `json:"callsign"` // flight callsign, vessel name, satellite designator
	Origin       string    `json:"origin"`   // origin country, city, airport, port
	Category     string    `json:"cat"`      // asset category (e.g. aircraft wake class, vessel type)
	Lat          int32     `json:"lat"`      // latitude * 10_000_000
	Lon          int32     `json:"lon"`      // longitude * 10_000_000
	Alt          int32     `json:"alt"`      // meters above sea level (0 for surface)
	Speed        float32   `json:"spd"`      // knots (aircraft/vessel) or 0
	Heading      float32   `json:"hdg"`      // degrees (0-360) or 0
	VerticalRate float32   `json:"vr"`       // meters/second, positive = climbing
	H3Cell       uint64    `json:"h3"`       // pre-computed H3 index at resolution 7
	Source       string    `json:"src"`      // provider identifier (e.g. "adsb_exchange")
	Squawk       string    `json:"squawk"`
	NavStatus    string    `json:"nav_status"`
	IMONumber    uint32    `json:"imo_number"`
	ShipType     string    `json:"ship_type"`
	Destination  string    `json:"destination"`
	Draught      float32   `json:"draught"`
	DimA         uint16    `json:"dim_a"`
	DimB         uint16    `json:"dim_b"`
	DimC         uint16    `json:"dim_c"`
	DimD         uint16    `json:"dim_d"`
	ETA           string    `json:"eta"`
	RateOfTurn    float32   `json:"rate_of_turn"`
	OrbitRegime   string    `json:"orbit_regime"`
	Confidence    string    `json:"confidence"`
	TLEEpoch      int64     `json:"tle_epoch"`
	Inclination   float32   `json:"inclination"`
	PeriodMinutes float32   `json:"period_minutes"`
	ApogeeKm      float32   `json:"apogee_km"`
	PerigeeKm     float32   `json:"perigee_km"`
	SatStatus     string    `json:"sat_status"`
}
