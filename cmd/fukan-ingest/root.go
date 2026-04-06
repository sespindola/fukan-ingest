package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sespindola/fukan-ingest/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile string
	cfg     config.Config
)

var rootCmd = &cobra.Command{
	Use:   "fukan-ingest",
	Short: "Fukan data ingestion pipeline",
	Long:  "Unified CLI for fukan-ingest workers and batchers.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return initConfig()
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ./config.yaml)")
}

func initConfig() error {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("/etc/fukan-ingest")
	}

	// Defaults matching the old envOrDefault values.
	viper.SetDefault("nats.url", "nats://localhost:4222")
	viper.SetDefault("clickhouse.addr", "localhost:9000")
	viper.SetDefault("clickhouse.database", "fukan")
	viper.SetDefault("clickhouse.user", "default")
	viper.SetDefault("clickhouse.password", "")
	viper.SetDefault("redis.url", "redis://localhost:6379/0")

	// Env-var overrides for backward compatibility.
	viper.SetEnvPrefix("")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Explicit bindings for legacy env vars (no prefix).
	_ = viper.BindEnv("nats.url", "NATS_URL")
	_ = viper.BindEnv("clickhouse.addr", "CLICKHOUSE_ADDR")
	_ = viper.BindEnv("clickhouse.database", "CLICKHOUSE_DATABASE")
	_ = viper.BindEnv("clickhouse.user", "CLICKHOUSE_USER")
	_ = viper.BindEnv("clickhouse.password", "CLICKHOUSE_PASSWORD")
	_ = viper.BindEnv("redis.url", "REDIS_URL")

	// OpenSky config: OAuth2 + aircraft database CSV URL.
	viper.SetDefault("opensky.csv_url", "https://opensky-network.org/datasets/metadata/aircraftDatabase.csv")
	_ = viper.BindEnv("opensky.client_id", "OPENSKY_CLIENT_ID")
	_ = viper.BindEnv("opensky.client_secret", "OPENSKY_CLIENT_SECRET")
	_ = viper.BindEnv("opensky.csv_url", "OPENSKY_CSV_URL")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			// Only fail if a config file was explicitly specified.
			if cfgFile != "" {
				return fmt.Errorf("read config: %w", err)
			}
			// For path-not-found with no explicit file, also ignore.
			if !os.IsNotExist(err) {
				return fmt.Errorf("read config: %w", err)
			}
		}
	}

	if err := viper.Unmarshal(&cfg); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}
	return nil
}
