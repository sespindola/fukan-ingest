CREATE TABLE IF NOT EXISTS aircraft_meta (
    icao24              String,
    registration        String       DEFAULT '',
    manufacturer_name   String       DEFAULT '',
    model               String       DEFAULT '',
    typecode            String       DEFAULT '',
    icao_aircraft_type  LowCardinality(String) DEFAULT '',
    operator            String       DEFAULT '',
    operator_callsign   String       DEFAULT '',
    operator_icao       LowCardinality(String) DEFAULT '',
    operator_iata       LowCardinality(String) DEFAULT '',
    owner               String       DEFAULT '',
    built               String       DEFAULT '',
    status              String       DEFAULT '',
    category_desc       String       DEFAULT '',
    image_url           String       DEFAULT '',
    image_attribution   String       DEFAULT '',
    updated_at          DateTime     DEFAULT now()
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY icao24;
