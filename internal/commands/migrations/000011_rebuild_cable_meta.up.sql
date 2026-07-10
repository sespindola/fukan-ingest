DROP TABLE IF EXISTS cable_meta;

CREATE TABLE cable_meta (
    cable_id                String,
    name                    String,
    slug                    String,
    alt_names               Array(String),
    owners                  Array(String),
    status                  LowCardinality(String),
    rfs_year                UInt16,
    length_km               UInt32,
    medium                  LowCardinality(String),
    category                LowCardinality(String),
    is_approximate_geometry UInt8,
    coords                  Array(Int32) CODEC(LZ4),
    bbox_min_lat            Int32 CODEC(DoubleDelta, LZ4),
    bbox_min_lon            Int32 CODEC(DoubleDelta, LZ4),
    bbox_max_lat            Int32 CODEC(DoubleDelta, LZ4),
    bbox_max_lon            Int32 CODEC(DoubleDelta, LZ4),
    provenance_source_urls  Array(String) CODEC(ZSTD(1)),
    osm_way_ids             Array(UInt64),
    updated_at              DateTime DEFAULT now()
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY cable_id;

CREATE TABLE IF NOT EXISTS cable_landings (
    cable_id       String,
    cable_name     String,
    country        LowCardinality(String),
    location_name  String,
    lat            Int32 CODEC(DoubleDelta, LZ4),
    lon            Int32 CODEC(DoubleDelta, LZ4),
    source         LowCardinality(String),
    source_url     String,
    retrieved_at   DateTime,
    updated_at     DateTime DEFAULT now()
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (cable_id, country, location_name);
