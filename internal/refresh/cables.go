package refresh

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/ch-go/proto"

	"github.com/sespindola/fukan-ingest/internal/refresh/cables"
)

// Cables rebuilds the cable_meta and cable_landings tables from the
// committed JSONL baseline (Wikidata + Wikipedia) augmented with live OSM
// Overpass geometry. Every emitted row carries Wikidata identity AND a
// validated OSM polyline — cables without a precise OSM match are dropped.
//
// The pipeline runs three gates:
//   1. baseline.go applyPrecisionGate — drops country/region/inland-centroid
//      landings via the Natural Earth coastline buffer (LandingBufferKm).
//   2. merge.go Merge — drops cables without an OSM polyline within
//      LandingMatchKm of any baseline landing.
//   3. coastline.go PolylineClearsLand — drops OSM ways whose interior
//      crosses land outside endpoint approach buffers (RouteBufferKm,
//      LandingApproachKm).
//
// Source policy: never use submarinecablemap.com, TeleGeography data, or
// derivative datasets. See internal/refresh/cables/data/README.md.
func Cables(ctx context.Context, conn *ch.Client, overpassURL string, dryRun bool) error {
	start := time.Now()

	baseline, err := cables.LoadEmbeddedBaseline()
	if err != nil {
		return fmt.Errorf("load baseline: %w", err)
	}

	slog.Info("downloading OSM submarine cable data", "url", firstNonZero(overpassURL, cables.DefaultOverpassURL))
	osm, skipped, err := cables.FetchOSMCables(ctx, overpassURL)
	if err != nil {
		return fmt.Errorf("fetch OSM cables: %w", err)
	}
	slog.Info("parsed OSM cables", "cables", len(osm), "skipped_elements", skipped)

	rows, lands := cables.Merge(baseline, osm, cables.MergeOptions{Now: time.Now().UTC()})

	if dryRun || conn == nil {
		slog.Info("cable refresh dry-run complete",
			"cables", len(rows),
			"landings", len(lands),
			"duration", time.Since(start).Round(time.Millisecond),
		)
		return nil
	}

	for i := 0; i < len(rows); i += batchSize {
		end := min(i+batchSize, len(rows))
		if err := insertCableBatch(ctx, conn, rows[i:end]); err != nil {
			return fmt.Errorf("insert cable batch: %w", err)
		}
	}
	for i := 0; i < len(lands); i += batchSize {
		end := min(i+batchSize, len(lands))
		if err := insertLandingBatch(ctx, conn, lands[i:end]); err != nil {
			return fmt.Errorf("insert landing batch: %w", err)
		}
	}
	slog.Info("cable refresh complete",
		"cables", len(rows),
		"landings", len(lands),
		"duration", time.Since(start).Round(time.Millisecond),
	)
	return nil
}

func firstNonZero(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func insertCableBatch(ctx context.Context, conn *ch.Client, batch []cables.CableRow) error {
	var (
		colCableID              proto.ColStr
		colName                 proto.ColStr
		colSlug                 proto.ColStr
		colAltNames             = proto.NewArray[string](&proto.ColStr{})
		colOwners               = proto.NewArray[string](&proto.ColStr{})
		colRFSYear              proto.ColUInt16
		colLengthKM             proto.ColUInt32
		colCoords               = proto.NewArrInt32()
		colBBoxMinLat           proto.ColInt32
		colBBoxMinLon           proto.ColInt32
		colBBoxMaxLat           proto.ColInt32
		colBBoxMaxLon           proto.ColInt32
		colProvenanceSourceURLs = proto.NewArray[string](&proto.ColStr{})
		colOSMWayIDs            = proto.NewArray[uint64](&proto.ColUInt64{})
	)
	colStatus := proto.NewLowCardinality[string](&proto.ColStr{})
	colMedium := proto.NewLowCardinality[string](&proto.ColStr{})
	colCategory := proto.NewLowCardinality[string](&proto.ColStr{})

	for _, r := range batch {
		colCableID.Append(r.CableID)
		colName.Append(r.Name)
		colSlug.Append(r.Slug)
		colAltNames.Append(r.AltNames)
		colOwners.Append(r.Owners)
		colStatus.Append(r.Status)
		colRFSYear.Append(r.RFSYear)
		colLengthKM.Append(r.LengthKM)
		colMedium.Append(r.Medium)
		colCategory.Append(r.Category)
		colCoords.Append(r.Coords)
		colBBoxMinLat.Append(r.BBoxMinLat)
		colBBoxMinLon.Append(r.BBoxMinLon)
		colBBoxMaxLat.Append(r.BBoxMaxLat)
		colBBoxMaxLon.Append(r.BBoxMaxLon)
		colProvenanceSourceURLs.Append(r.ProvenanceSourceURLs)
		colOSMWayIDs.Append(r.OSMWayIDs)
	}

	input := proto.Input{
		{Name: "cable_id", Data: &colCableID},
		{Name: "name", Data: &colName},
		{Name: "slug", Data: &colSlug},
		{Name: "alt_names", Data: colAltNames},
		{Name: "owners", Data: colOwners},
		{Name: "status", Data: colStatus},
		{Name: "rfs_year", Data: &colRFSYear},
		{Name: "length_km", Data: &colLengthKM},
		{Name: "medium", Data: colMedium},
		{Name: "category", Data: colCategory},
		{Name: "coords", Data: colCoords},
		{Name: "bbox_min_lat", Data: &colBBoxMinLat},
		{Name: "bbox_min_lon", Data: &colBBoxMinLon},
		{Name: "bbox_max_lat", Data: &colBBoxMaxLat},
		{Name: "bbox_max_lon", Data: &colBBoxMaxLon},
		{Name: "provenance_source_urls", Data: colProvenanceSourceURLs},
		{Name: "osm_way_ids", Data: colOSMWayIDs},
	}
	return conn.Do(ctx, ch.Query{
		Body: "INSERT INTO fukan.cable_meta (cable_id, name, slug, alt_names, owners, status, rfs_year, length_km, " +
			"medium, category, coords, bbox_min_lat, bbox_min_lon, bbox_max_lat, bbox_max_lon, " +
			"provenance_source_urls, osm_way_ids) VALUES",
		Input: input,
	})
}

func insertLandingBatch(ctx context.Context, conn *ch.Client, batch []cables.LandingRow) error {
	var (
		colCableID      proto.ColStr
		colCableName    proto.ColStr
		colLocationName proto.ColStr
		colLat          proto.ColInt32
		colLon          proto.ColInt32
		colSourceURL    proto.ColStr
		colRetrievedAt  proto.ColDateTime
	)
	colCountry := proto.NewLowCardinality[string](&proto.ColStr{})
	colSource := proto.NewLowCardinality[string](&proto.ColStr{})

	for _, r := range batch {
		colCableID.Append(r.CableID)
		colCableName.Append(r.CableName)
		colCountry.Append(r.Country)
		colLocationName.Append(r.LocationName)
		colLat.Append(r.Lat)
		colLon.Append(r.Lon)
		colSource.Append(r.Source)
		colSourceURL.Append(r.SourceURL)
		colRetrievedAt.Append(r.RetrievedAt)
	}

	input := proto.Input{
		{Name: "cable_id", Data: &colCableID},
		{Name: "cable_name", Data: &colCableName},
		{Name: "country", Data: colCountry},
		{Name: "location_name", Data: &colLocationName},
		{Name: "lat", Data: &colLat},
		{Name: "lon", Data: &colLon},
		{Name: "source", Data: colSource},
		{Name: "source_url", Data: &colSourceURL},
		{Name: "retrieved_at", Data: &colRetrievedAt},
	}
	return conn.Do(ctx, ch.Query{
		Body: "INSERT INTO fukan.cable_landings (cable_id, cable_name, country, location_name, lat, lon, source, source_url, retrieved_at) VALUES",
		Input: input,
	})
}
