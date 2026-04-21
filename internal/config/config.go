package config

import "errors"

// Config is the top-level configuration for fukan-ingest.
type Config struct {
	NATS         NATSConfig                     `mapstructure:"nats"`
	ClickHouse   ClickHouseConfig               `mapstructure:"clickhouse"`
	Redis        RedisConfig                    `mapstructure:"redis"`
	GeoIP        GeoIPConfig                    `mapstructure:"geoip"`
	Integrations map[string][]IntegrationConfig `mapstructure:"integrations"`
}

type NATSConfig struct {
	URL string `mapstructure:"url"`
}

type ClickHouseConfig struct {
	Addr     string `mapstructure:"addr"`
	Database string `mapstructure:"database"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
}

type RedisConfig struct {
	URL string `mapstructure:"url"`
}

// GeoIPConfig points at on-disk MaxMind MMDB files consumed by the BGP
// worker. CityDB provides prefix → (lat, lon); ASNDB provides prefix →
// (registered holder ASN, organization name). Paths are local-filesystem
// only; delivery (mount, copy, refresh) is the operator's responsibility.
type GeoIPConfig struct {
	CityDB string `mapstructure:"city_db"`
	ASNDB  string `mapstructure:"asn_db"`
}

type IntegrationConfig struct {
	Name         string `mapstructure:"name"`
	APIURL       string `mapstructure:"api_url"`
	APIKey       string `mapstructure:"api_key"`
	Interval     int    `mapstructure:"interval"` // seconds, 0 = worker default
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	TokenURL     string `mapstructure:"token_url"`
}

// Validate checks that required configuration fields are present.
func (c *Config) Validate() error {
	if c.NATS.URL == "" {
		return errors.New("config: nats.url is required")
	}
	if c.ClickHouse.Addr == "" {
		return errors.New("config: clickhouse.addr is required")
	}
	if c.ClickHouse.Database == "" {
		return errors.New("config: clickhouse.database is required")
	}
	if c.Redis.URL == "" {
		return errors.New("config: redis.url is required")
	}
	return nil
}
