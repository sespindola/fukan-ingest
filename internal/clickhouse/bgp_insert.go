package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/ch-go/proto"
	"github.com/sespindola/fukan-ingest/internal/model"
)

// InsertBGPBatch inserts a batch of BgpEvents into fukan.bgp_events using
// columnar encoding. Append-only — no argMax, no "latest per ASN". Paired
// with the dedicated BGP batcher (internal/batcher/bgp_batcher.go).
func InsertBGPBatch(ctx context.Context, conn *ch.Client, events []model.BgpEvent) error {
	if len(events) == 0 {
		return nil
	}

	colEventTime := new(proto.ColDateTime64).WithPrecision(proto.PrecisionMilli)
	var (
		colEventID  proto.ColStr
		colPrefix   proto.ColStr
		colOriginAS proto.ColUInt32
		colPrefixAS proto.ColUInt32
		colLat      proto.ColInt32
		colLon      proto.ColInt32
		colH3       proto.ColUInt64
	)
	colCategory := proto.NewLowCardinality[string](&proto.ColStr{})
	colCollector := proto.NewLowCardinality[string](&proto.ColStr{})
	colSource := proto.NewLowCardinality[string](&proto.ColStr{})
	colPrefixOrg := proto.NewLowCardinality[string](&proto.ColStr{})
	colASPath := proto.NewArrUInt32()
	colPathCoords := proto.NewArrInt32()

	for _, e := range events {
		colEventTime.Append(time.UnixMilli(e.Timestamp))
		colEventID.Append(e.EventID)
		colCategory.Append(e.Category)
		colPrefix.Append(e.Prefix)
		colOriginAS.Append(e.OriginAS)
		colPrefixAS.Append(e.PrefixAS)
		colPrefixOrg.Append(e.PrefixOrg)
		colASPath.Append(e.ASPath)
		colPathCoords.Append(e.PathCoords)
		colCollector.Append(e.Collector)
		colLat.Append(e.Lat)
		colLon.Append(e.Lon)
		colH3.Append(e.H3Cell)
		colSource.Append(e.Source)
	}

	input := proto.Input{
		{Name: "event_time", Data: colEventTime},
		{Name: "event_id", Data: &colEventID},
		{Name: "category", Data: colCategory},
		{Name: "prefix", Data: &colPrefix},
		{Name: "origin_as", Data: &colOriginAS},
		{Name: "prefix_as", Data: &colPrefixAS},
		{Name: "prefix_org", Data: colPrefixOrg},
		{Name: "as_path", Data: colASPath},
		{Name: "path_coords", Data: colPathCoords},
		{Name: "collector", Data: colCollector},
		{Name: "lat", Data: &colLat},
		{Name: "lon", Data: &colLon},
		{Name: "h3_cell", Data: &colH3},
		{Name: "source", Data: colSource},
	}

	err := conn.Do(ctx, ch.Query{
		Body:  "INSERT INTO fukan.bgp_events VALUES",
		Input: input,
	})
	if err != nil {
		return fmt.Errorf("clickhouse bgp insert: %w", err)
	}
	return nil
}
