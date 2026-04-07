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
		colAssetID     proto.ColStr
		colCallsign    proto.ColStr
		colLat         proto.ColInt32
		colLon         proto.ColInt32
		colAlt         proto.ColInt32
		colSpeed       proto.ColFloat32
		colHeading     proto.ColFloat32
		colVerticalRate proto.ColFloat32
		colH3          proto.ColUInt64
		colMeta        proto.ColStr
	)
	colAssetType := proto.NewLowCardinality[string](&proto.ColStr{})
	colOrigin := proto.NewLowCardinality[string](&proto.ColStr{})
	colCategory := proto.NewLowCardinality[string](&proto.ColStr{})
	colSource := proto.NewLowCardinality[string](&proto.ColStr{})

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
		colMeta.Append(e.Metadata)
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
		{Name: "metadata", Data: &colMeta},
	}

	err := conn.Do(ctx, ch.Query{
		Body:  "INSERT INTO fukan.telemetry_raw (event_time, asset_id, asset_type, callsign, origin, category, lat, lon, alt, speed, heading, vertical_rate, h3_cell, source, metadata) VALUES",
		Input: input,
	})
	if err != nil {
		return fmt.Errorf("clickhouse insert: %w", err)
	}
	return nil
}
