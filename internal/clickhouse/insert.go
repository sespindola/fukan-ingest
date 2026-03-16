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
func (c *Client) InsertBatch(ctx context.Context, events []model.FukanEvent) error {
	if len(events) == 0 {
		return nil
	}

	var (
		colEventTime proto.ColDateTime64
		colAssetID   proto.ColStr
		colType      proto.ColStr
		colLat       proto.ColInt32
		colLon       proto.ColInt32
		colAlt       proto.ColInt32
		colSpeed     proto.ColFloat32
		colHeading   proto.ColFloat32
		colH3        proto.ColUInt64
		colSource    proto.ColStr
		colMeta      proto.ColStr
	)
	colEventTime.Precision = 3 // milliseconds, matches DateTime64(3) in schema

	for _, e := range events {
		colEventTime.Append(time.UnixMilli(e.Timestamp))
		colAssetID.Append(e.AssetID)
		colType.Append(string(e.AssetType))
		colLat.Append(e.Lat)
		colLon.Append(e.Lon)
		colAlt.Append(e.Alt)
		colSpeed.Append(e.Speed)
		colHeading.Append(e.Heading)
		colH3.Append(e.H3Cell)
		colSource.Append(e.Source)
		colMeta.Append(e.Metadata)
	}

	input := proto.Input{
		{Name: "event_time", Data: &colEventTime},
		{Name: "asset_id", Data: &colAssetID},
		{Name: "asset_type", Data: proto.NewLowCardinality(&colType)},
		{Name: "lat", Data: &colLat},
		{Name: "lon", Data: &colLon},
		{Name: "alt", Data: &colAlt},
		{Name: "speed", Data: &colSpeed},
		{Name: "heading", Data: &colHeading},
		{Name: "h3_cell", Data: &colH3},
		{Name: "source", Data: proto.NewLowCardinality(&colSource)},
		{Name: "metadata", Data: &colMeta},
	}

    err := c.conn.Do(ctx, ch.Query{
        Body:  "INSERT INTO fukan.telemetry_raw (event_time, asset_id, asset_type, lat, lon, alt, speed, heading, h3_cell, source, metadata) VALUES",
        Input: input,
    })
	if err != nil {
		return fmt.Errorf("clickhouse insert: %w", err)
	}
	return nil
}
