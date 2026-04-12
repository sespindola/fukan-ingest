ALTER TABLE telemetry_raw
    ADD COLUMN IF NOT EXISTS squawk        String                  CODEC(LZ4)          AFTER source,
    ADD COLUMN IF NOT EXISTS nav_status    LowCardinality(String)                      AFTER squawk,
    ADD COLUMN IF NOT EXISTS imo_number    UInt32                  CODEC(LZ4)          AFTER nav_status,
    ADD COLUMN IF NOT EXISTS ship_type     LowCardinality(String)                      AFTER imo_number,
    ADD COLUMN IF NOT EXISTS destination   String                  CODEC(LZ4)          AFTER ship_type,
    ADD COLUMN IF NOT EXISTS draught       Float32                 CODEC(Gorilla, LZ4) AFTER destination,
    ADD COLUMN IF NOT EXISTS dim_a         UInt16                  CODEC(LZ4)          AFTER draught,
    ADD COLUMN IF NOT EXISTS dim_b         UInt16                  CODEC(LZ4)          AFTER dim_a,
    ADD COLUMN IF NOT EXISTS dim_c         UInt16                  CODEC(LZ4)          AFTER dim_b,
    ADD COLUMN IF NOT EXISTS dim_d         UInt16                  CODEC(LZ4)          AFTER dim_c,
    ADD COLUMN IF NOT EXISTS eta           String                  CODEC(LZ4)          AFTER dim_d,
    ADD COLUMN IF NOT EXISTS rate_of_turn  Float32                 CODEC(Gorilla, LZ4) AFTER eta;

ALTER TABLE telemetry_raw DROP COLUMN IF EXISTS metadata;

DROP VIEW IF EXISTS telemetry_latest_flat;
DROP VIEW IF EXISTS telemetry_latest_mv;

ALTER TABLE telemetry_latest
    ADD COLUMN IF NOT EXISTS squawk_state        AggregateFunction(argMax, String, DateTime64(3)),
    ADD COLUMN IF NOT EXISTS nav_status_state    AggregateFunction(argMax, String, DateTime64(3)),
    ADD COLUMN IF NOT EXISTS imo_number_state    AggregateFunction(argMax, UInt32, DateTime64(3)),
    ADD COLUMN IF NOT EXISTS ship_type_state     AggregateFunction(argMax, String, DateTime64(3)),
    ADD COLUMN IF NOT EXISTS destination_state   AggregateFunction(argMax, String, DateTime64(3)),
    ADD COLUMN IF NOT EXISTS draught_state       AggregateFunction(argMax, Float32, DateTime64(3)),
    ADD COLUMN IF NOT EXISTS rate_of_turn_state  AggregateFunction(argMax, Float32, DateTime64(3));

CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_latest_mv
TO telemetry_latest AS
SELECT
    asset_type,
    asset_id,
    argMaxState(callsign, event_time)      AS callsign_state,
    argMaxState(origin, event_time)        AS origin_state,
    argMaxState(category, event_time)      AS category_state,
    maxState(event_time)                   AS ts_state,
    argMaxState(lat, event_time)           AS lat_state,
    argMaxState(lon, event_time)           AS lon_state,
    argMaxState(alt, event_time)           AS alt_state,
    argMaxState(speed, event_time)         AS speed_state,
    argMaxState(heading, event_time)       AS heading_state,
    argMaxState(vertical_rate, event_time) AS vertical_rate_state,
    argMaxState(h3_cell, event_time)       AS h3_state,
    argMaxState(source, event_time)        AS source_state,
    argMaxState(squawk, event_time)        AS squawk_state,
    argMaxState(nav_status, event_time)    AS nav_status_state,
    argMaxState(imo_number, event_time)    AS imo_number_state,
    argMaxState(ship_type, event_time)     AS ship_type_state,
    argMaxState(destination, event_time)   AS destination_state,
    argMaxState(draught, event_time)       AS draught_state,
    argMaxState(rate_of_turn, event_time)  AS rate_of_turn_state
FROM telemetry_raw
GROUP BY asset_type, asset_id;

CREATE VIEW IF NOT EXISTS telemetry_latest_flat AS
SELECT
    asset_type,
    asset_id,
    argMaxMerge(callsign_state)      AS callsign,
    argMaxMerge(origin_state)        AS origin,
    argMaxMerge(category_state)      AS category,
    maxMerge(ts_state)               AS event_time,
    argMaxMerge(lat_state)           AS lat,
    argMaxMerge(lon_state)           AS lon,
    argMaxMerge(alt_state)           AS alt,
    argMaxMerge(speed_state)         AS speed,
    argMaxMerge(heading_state)       AS heading,
    argMaxMerge(vertical_rate_state) AS vertical_rate,
    argMaxMerge(h3_state)            AS h3_cell,
    argMaxMerge(source_state)        AS source,
    argMaxMerge(squawk_state)        AS squawk,
    argMaxMerge(nav_status_state)    AS nav_status,
    argMaxMerge(imo_number_state)    AS imo_number,
    argMaxMerge(ship_type_state)     AS ship_type,
    argMaxMerge(destination_state)   AS destination,
    argMaxMerge(draught_state)       AS draught,
    argMaxMerge(rate_of_turn_state)  AS rate_of_turn
FROM telemetry_latest
GROUP BY asset_type, asset_id;
