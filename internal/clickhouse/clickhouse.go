package clickhouse

import (
	"context"
	"fmt"

	"github.com/ClickHouse/ch-go"
	"github.com/sespindola/fukan-ingest/internal/config"
)

// Connect dials ClickHouse, pings it, and returns the bare connection.
func Connect(ctx context.Context, cfg config.ClickHouseConfig) (*ch.Client, error) {
	c, err := ch.Dial(ctx, ch.Options{
		Address:  cfg.Addr,
		Database: cfg.Database,
		User:     cfg.User,
		Password: cfg.Password,
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse dial: %w", err)
	}
	if err := c.Ping(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("clickhouse ping: %w", err)
	}
	return c, nil
}
