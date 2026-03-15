package clickhouse

import (
	"context"
	"fmt"

	"github.com/ClickHouse/ch-go"
)

// Client wraps a ch-go native connection to ClickHouse.
type Client struct {
	conn *ch.Client
}

// NewClient connects to ClickHouse using the native protocol.
func NewClient(ctx context.Context, addr, database, user, password string) (*Client, error) {
	c, err := ch.Dial(ctx, ch.Options{
		Address:  addr,
		Database: database,
		User:     user,
		Password: password,
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse dial: %w", err)
	}
	return &Client{conn: c}, nil
}

// Ping checks the ClickHouse connection.
func (c *Client) Ping(ctx context.Context) error {
	return c.conn.Ping(ctx)
}

// Conn returns the underlying ch-go client for direct use.
func (c *Client) Conn() *ch.Client {
	return c.conn
}

// Close closes the ClickHouse connection.
func (c *Client) Close() {
	c.conn.Close()
}
