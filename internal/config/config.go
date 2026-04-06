package config

// Config is the top-level configuration for fukan-ingest.
type Config struct {
	NATS         NATSConfig                     `mapstructure:"nats"`
	ClickHouse   ClickHouseConfig               `mapstructure:"clickhouse"`
	Redis        RedisConfig                    `mapstructure:"redis"`
	OpenSky      OpenSkyConfig                  `mapstructure:"opensky"`
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

type OpenSkyConfig struct {
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	CSVURL       string `mapstructure:"csv_url"`
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
