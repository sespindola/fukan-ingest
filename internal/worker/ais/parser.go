package ais

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sespindola/fukan-ingest/internal/coord"
	"github.com/sespindola/fukan-ingest/internal/model"
)

type aisMessage struct {
	MessageType string          `json:"MessageType"`
	MetaData    aisMetaData     `json:"MetaData"`
	Message     json.RawMessage `json:"Message"`
}

type aisMetaData struct {
	MMSI     int64   `json:"MMSI"`
	ShipName string  `json:"ShipName"`
	Lat      float64 `json:"latitude"`
	Lon      float64 `json:"longitude"`
	TimeUTC  string  `json:"time_utc"`
}

type aisPositionReport struct {
	Sog                float64 `json:"Sog"`
	Cog                float64 `json:"Cog"`
	TrueHeading        int     `json:"TrueHeading"`
	Latitude           float64 `json:"Latitude"`
	Longitude          float64 `json:"Longitude"`
	NavigationalStatus int     `json:"NavigationalStatus"`
	RateOfTurn         float64 `json:"RateOfTurn"`
}

type aisShipStaticData struct {
	ImoNumber            int     `json:"ImoNumber"`
	CallSign             string  `json:"CallSign"`
	Name                 string  `json:"Name"`
	Type                 int     `json:"Type"`
	Dimension            aisDim  `json:"Dimension"`
	Eta                  aisEta  `json:"Eta"`
	MaximumStaticDraught float64 `json:"MaximumStaticDraught"`
	Destination          string  `json:"Destination"`
}

type aisDim struct {
	A int `json:"A"`
	B int `json:"B"`
	C int `json:"C"`
	D int `json:"D"`
}

type aisEta struct {
	Month  int `json:"Month"`
	Day    int `json:"Day"`
	Hour   int `json:"Hour"`
	Minute int `json:"Minute"`
}

var navStatusNames = [16]string{
	0:  "under_way_using_engine",
	1:  "at_anchor",
	2:  "not_under_command",
	3:  "restricted_manoeuvrability",
	4:  "constrained_by_draught",
	5:  "moored",
	6:  "aground",
	7:  "engaged_in_fishing",
	8:  "under_way_sailing",
	9:  "reserved_hsc",
	10: "reserved_wig",
	11: "reserved_11",
	12: "reserved_12",
	13: "reserved_13",
	14: "ais_sart",
	15: "not_defined",
}

// AIS ship type codes: first digit determines the category.
var shipTypeCategory = map[int]string{
	1: "reserved",
	2: "wing_in_ground",
	3: "special",
	4: "high_speed_craft",
	5: "special",
	6: "passenger",
	7: "cargo",
	8: "tanker",
	9: "other",
}

func navStatusName(code int) string {
	if code >= 0 && code < len(navStatusNames) {
		return navStatusNames[code]
	}
	return "not_defined"
}

func shipTypeName(code int) string {
	switch {
	case code == 0:
		return ""
	case code >= 20 && code <= 29:
		return "wing_in_ground"
	case code == 30:
		return "fishing"
	case code == 31 || code == 32:
		return "towing"
	case code == 33:
		return "dredging"
	case code == 34:
		return "diving"
	case code == 35:
		return "military"
	case code == 36:
		return "sailing"
	case code == 37:
		return "pleasure_craft"
	case code >= 40 && code <= 49:
		return "high_speed_craft"
	case code == 50:
		return "pilot"
	case code == 51:
		return "search_and_rescue"
	case code == 52:
		return "tug"
	case code == 53:
		return "port_tender"
	case code == 55:
		return "law_enforcement"
	case code == 58:
		return "medical"
	case code >= 60 && code <= 69:
		return "passenger"
	case code >= 70 && code <= 79:
		return "cargo"
	case code >= 80 && code <= 89:
		return "tanker"
	default:
		if cat, ok := shipTypeCategory[code/10]; ok {
			return cat
		}
		return "other"
	}
}

// ParseMessage converts a single AISStream WebSocket message into a FukanEvent.
// Returns the event and true on success, or a zero event and false if the
// message should be skipped.
func ParseMessage(data []byte, source string) (model.FukanEvent, bool) {
	var msg aisMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return model.FukanEvent{}, false
	}

	ts, err := time.Parse(time.RFC3339, msg.MetaData.TimeUTC)
	if err != nil {
		ts, err = time.Parse("2006-01-02 15:04:05.999999999 +0000 UTC", msg.MetaData.TimeUTC)
		if err != nil {
			return model.FukanEvent{}, false
		}
	}

	mmsi := strconv.FormatInt(msg.MetaData.MMSI, 10)
	callsign := strings.TrimSpace(msg.MetaData.ShipName)

	switch msg.MessageType {
	case "PositionReport", "StandardClassBPositionReport":
		return parsePositionReport(msg, mmsi, callsign, ts, source)
	case "ShipStaticData":
		return parseShipStaticData(msg, mmsi, callsign, ts, source)
	default:
		return model.FukanEvent{}, false
	}
}

func parsePositionReport(msg aisMessage, mmsi, callsign string, ts time.Time, source string) (model.FukanEvent, bool) {
	var wrapper map[string]aisPositionReport
	if err := json.Unmarshal(msg.Message, &wrapper); err != nil {
		return model.FukanEvent{}, false
	}

	rpt, ok := wrapper[msg.MessageType]
	if !ok {
		return model.FukanEvent{}, false
	}

	lat := rpt.Latitude
	lon := rpt.Longitude
	if lat == 0 && lon == 0 {
		return model.FukanEvent{}, false
	}

	heading := float32(rpt.Cog)
	if rpt.TrueHeading >= 0 && rpt.TrueHeading < 360 {
		heading = float32(rpt.TrueHeading)
	}

	h3cell, err := coord.ComputeH3(lat, lon)
	if err != nil {
		return model.FukanEvent{}, false
	}

	event := model.FukanEvent{
		Timestamp:  ts.UnixMilli(),
		AssetID:    mmsi,
		AssetType:  model.AssetVessel,
		Callsign:   callsign,
		Lat:        coord.ScaleLat(lat),
		Lon:        coord.ScaleLon(lon),
		Speed:      float32(rpt.Sog),
		Heading:    heading,
		H3Cell:     h3cell,
		Source:     source,
		NavStatus:  navStatusName(rpt.NavigationalStatus),
		RateOfTurn: float32(rpt.RateOfTurn),
	}

	if err := model.Validate(event); err != nil {
		return model.FukanEvent{}, false
	}
	return event, true
}

func parseShipStaticData(msg aisMessage, mmsi, callsign string, ts time.Time, source string) (model.FukanEvent, bool) {
	var wrapper map[string]aisShipStaticData
	if err := json.Unmarshal(msg.Message, &wrapper); err != nil {
		return model.FukanEvent{}, false
	}

	sd, ok := wrapper["ShipStaticData"]
	if !ok {
		return model.FukanEvent{}, false
	}

	lat := msg.MetaData.Lat
	lon := msg.MetaData.Lon
	if lat == 0 && lon == 0 {
		return model.FukanEvent{}, false
	}

	h3cell, err := coord.ComputeH3(lat, lon)
	if err != nil {
		return model.FukanEvent{}, false
	}

	if callsign == "" {
		callsign = strings.TrimSpace(sd.Name)
	}

	var eta string
	if sd.Eta.Month > 0 && sd.Eta.Day > 0 {
		eta = fmt.Sprintf("%02d-%02d %02d:%02d", sd.Eta.Month, sd.Eta.Day, sd.Eta.Hour, sd.Eta.Minute)
	}

	event := model.FukanEvent{
		Timestamp:   ts.UnixMilli(),
		AssetID:     mmsi,
		AssetType:   model.AssetVessel,
		Callsign:    callsign,
		Lat:         coord.ScaleLat(lat),
		Lon:         coord.ScaleLon(lon),
		H3Cell:      h3cell,
		Source:      source,
		IMONumber:   uint32(sd.ImoNumber),
		ShipType:    shipTypeName(sd.Type),
		Destination: strings.TrimSpace(sd.Destination),
		Draught:     float32(sd.MaximumStaticDraught),
		DimA:        uint16(sd.Dimension.A),
		DimB:        uint16(sd.Dimension.B),
		DimC:        uint16(sd.Dimension.C),
		DimD:        uint16(sd.Dimension.D),
		ETA:         eta,
	}

	if err := model.Validate(event); err != nil {
		return model.FukanEvent{}, false
	}
	return event, true
}
