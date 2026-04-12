package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/ch-go/proto"
	"github.com/sespindola/fukan-ingest/internal/model"
)

// InsertBatch inserts a batch of FukanEvents into fukan.telemetry_raw using columnar encoding.
func InsertBatch(ctx context.Context, conn *ch.Client, events []model.FukanEvent) error {
	if len(events) == 0 {
		return nil
	}

	colEventTime := new(proto.ColDateTime64).WithPrecision(proto.PrecisionMilli)
	var (
		colAssetID      proto.ColStr
		colCallsign     proto.ColStr
		colLat          proto.ColInt32
		colLon          proto.ColInt32
		colAlt          proto.ColInt32
		colSpeed        proto.ColFloat32
		colHeading      proto.ColFloat32
		colVerticalRate proto.ColFloat32
		colH3           proto.ColUInt64
		colSquawk       proto.ColStr
		colIMONumber    proto.ColUInt32
		colDestination  proto.ColStr
		colDraught      proto.ColFloat32
		colDimA         proto.ColUInt16
		colDimB         proto.ColUInt16
		colDimC         proto.ColUInt16
		colDimD         proto.ColUInt16
		colETA          proto.ColStr
		colRateOfTurn   proto.ColFloat32
	)
	colAssetType := proto.NewLowCardinality[string](&proto.ColStr{})
	colOrigin := proto.NewLowCardinality[string](&proto.ColStr{})
	colCategory := proto.NewLowCardinality[string](&proto.ColStr{})
	colSource := proto.NewLowCardinality[string](&proto.ColStr{})
	colNavStatus := proto.NewLowCardinality[string](&proto.ColStr{})
	colShipType := proto.NewLowCardinality[string](&proto.ColStr{})

	for _, e := range events {
		colEventTime.Append(time.UnixMilli(e.Timestamp))
		colAssetID.Append(e.AssetID)
		colAssetType.Append(string(e.AssetType))
		colCallsign.Append(e.Callsign)
		colOrigin.Append(e.Origin)
		colCategory.Append(e.Category)
		colLat.Append(e.Lat)
		colLon.Append(e.Lon)
		colAlt.Append(e.Alt)
		colSpeed.Append(e.Speed)
		colHeading.Append(e.Heading)
		colVerticalRate.Append(e.VerticalRate)
		colH3.Append(e.H3Cell)
		colSource.Append(e.Source)
		colSquawk.Append(e.Squawk)
		colNavStatus.Append(e.NavStatus)
		colIMONumber.Append(e.IMONumber)
		colShipType.Append(e.ShipType)
		colDestination.Append(e.Destination)
		colDraught.Append(e.Draught)
		colDimA.Append(e.DimA)
		colDimB.Append(e.DimB)
		colDimC.Append(e.DimC)
		colDimD.Append(e.DimD)
		colETA.Append(e.ETA)
		colRateOfTurn.Append(e.RateOfTurn)
	}

	input := proto.Input{
		{Name: "event_time", Data: colEventTime},
		{Name: "asset_id", Data: &colAssetID},
		{Name: "asset_type", Data: colAssetType},
		{Name: "callsign", Data: &colCallsign},
		{Name: "origin", Data: colOrigin},
		{Name: "category", Data: colCategory},
		{Name: "lat", Data: &colLat},
		{Name: "lon", Data: &colLon},
		{Name: "alt", Data: &colAlt},
		{Name: "speed", Data: &colSpeed},
		{Name: "heading", Data: &colHeading},
		{Name: "vertical_rate", Data: &colVerticalRate},
		{Name: "h3_cell", Data: &colH3},
		{Name: "source", Data: colSource},
		{Name: "squawk", Data: &colSquawk},
		{Name: "nav_status", Data: colNavStatus},
		{Name: "imo_number", Data: &colIMONumber},
		{Name: "ship_type", Data: colShipType},
		{Name: "destination", Data: &colDestination},
		{Name: "draught", Data: &colDraught},
		{Name: "dim_a", Data: &colDimA},
		{Name: "dim_b", Data: &colDimB},
		{Name: "dim_c", Data: &colDimC},
		{Name: "dim_d", Data: &colDimD},
		{Name: "eta", Data: &colETA},
		{Name: "rate_of_turn", Data: &colRateOfTurn},
	}

	err := conn.Do(ctx, ch.Query{
		Body:  "INSERT INTO fukan.telemetry_raw VALUES",
		Input: input,
	})
	if err != nil {
		return fmt.Errorf("clickhouse insert: %w", err)
	}
	return nil
}
