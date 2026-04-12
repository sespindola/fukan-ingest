ALTER TABLE telemetry_raw
    ADD COLUMN IF NOT EXISTS sat_status LowCardinality(String) AFTER perigee_km;

DROP VIEW IF EXISTS telemetry_latest_flat;
DROP VIEW IF EXISTS telemetry_latest_mv;

ALTER TABLE telemetry_latest
    ADD COLUMN IF NOT EXISTS sat_status_state AggregateFunction(argMax, String, DateTime64(3));

CREATE MATERIALIZED VIEW IF NOT EXISTS telemetry_latest_mv
TO telemetry_latest AS
SELECT
    asset_type,
    asset_id,
    argMaxState(callsign, event_time)        AS callsign_state,
    argMaxState(origin, event_time)          AS origin_state,
    argMaxState(category, event_time)        AS category_state,
    maxState(event_time)                     AS ts_state,
    argMaxState(lat, event_time)             AS lat_state,
    argMaxState(lon, event_time)             AS lon_state,
    argMaxState(alt, event_time)             AS alt_state,
    argMaxState(speed, event_time)           AS speed_state,
    argMaxState(heading, event_time)         AS heading_state,
    argMaxState(vertical_rate, event_time)   AS vertical_rate_state,
    argMaxState(h3_cell, event_time)         AS h3_state,
    argMaxState(source, event_time)          AS source_state,
    argMaxState(squawk, event_time)          AS squawk_state,
    argMaxState(nav_status, event_time)      AS nav_status_state,
    argMaxState(imo_number, event_time)      AS imo_number_state,
    argMaxState(ship_type, event_time)       AS ship_type_state,
    argMaxState(destination, event_time)     AS destination_state,
    argMaxState(draught, event_time)         AS draught_state,
    argMaxState(rate_of_turn, event_time)    AS rate_of_turn_state,
    argMaxState(orbit_regime, event_time)    AS orbit_regime_state,
    argMaxState(confidence, event_time)      AS confidence_state,
    argMaxState(tle_epoch, event_time)       AS tle_epoch_state,
    argMaxState(inclination, event_time)     AS inclination_state,
    argMaxState(period_minutes, event_time)  AS period_minutes_state,
    argMaxState(apogee_km, event_time)       AS apogee_km_state,
    argMaxState(perigee_km, event_time)      AS perigee_km_state,
    argMaxState(sat_status, event_time)      AS sat_status_state
FROM telemetry_raw
GROUP BY asset_type, asset_id;

CREATE VIEW IF NOT EXISTS telemetry_latest_flat AS
SELECT
    asset_type,
    asset_id,
    argMaxMerge(callsign_state)        AS callsign,
    argMaxMerge(origin_state)          AS origin,
    argMaxMerge(category_state)        AS category,
    maxMerge(ts_state)                 AS event_time,
    argMaxMerge(lat_state)             AS lat,
    argMaxMerge(lon_state)             AS lon,
    argMaxMerge(alt_state)             AS alt,
    argMaxMerge(speed_state)           AS speed,
    argMaxMerge(heading_state)         AS heading,
    argMaxMerge(vertical_rate_state)   AS vertical_rate,
    argMaxMerge(h3_state)              AS h3_cell,
    argMaxMerge(source_state)          AS source,
    argMaxMerge(squawk_state)          AS squawk,
    argMaxMerge(nav_status_state)      AS nav_status,
    argMaxMerge(imo_number_state)      AS imo_number,
    argMaxMerge(ship_type_state)       AS ship_type,
    argMaxMerge(destination_state)     AS destination,
    argMaxMerge(draught_state)         AS draught,
    argMaxMerge(rate_of_turn_state)    AS rate_of_turn,
    argMaxMerge(orbit_regime_state)    AS orbit_regime,
    argMaxMerge(confidence_state)      AS confidence,
    argMaxMerge(tle_epoch_state)       AS tle_epoch,
    argMaxMerge(inclination_state)     AS inclination,
    argMaxMerge(period_minutes_state)  AS period_minutes,
    argMaxMerge(apogee_km_state)       AS apogee_km,
    argMaxMerge(perigee_km_state)      AS perigee_km,
    argMaxMerge(sat_status_state)      AS sat_status
FROM telemetry_latest
GROUP BY asset_type, asset_id;
