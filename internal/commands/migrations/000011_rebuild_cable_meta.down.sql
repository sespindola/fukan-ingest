DROP TABLE IF EXISTS cable_landings;
DROP TABLE IF EXISTS cable_meta;

CREATE TABLE IF NOT EXISTS cable_meta (
    source            LowCardinality(String),
    osm_type          LowCardinality(String),
    osm_id            UInt64,
    name              String,
    ref               String,
    operator          String,
    medium            LowCardinality(String),
    category          LowCardinality(String),
    coords            Array(Int32) CODEC(LZ4),
    bbox_min_lat      Int32 CODEC(DoubleDelta, LZ4),
    bbox_min_lon      Int32 CODEC(DoubleDelta, LZ4),
    bbox_max_lat      Int32 CODEC(DoubleDelta, LZ4),
    bbox_max_lon      Int32 CODEC(DoubleDelta, LZ4),
    source_updated_at DateTime,
    updated_at        DateTime DEFAULT now()
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (source, osm_type, osm_id);
