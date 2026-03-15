package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
	"github.com/sespindola/fukan-ingest/internal/model"
)

// Publisher publishes FukanEvents to Redis pub/sub grouped by H3 cell.
type Publisher struct {
	client *redis.Client
}

// NewPublisher creates a Redis publisher from a URL (e.g. "redis://localhost:6379/0").
func NewPublisher(url string) (*Publisher, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis parse url: %w", err)
	}
	return &Publisher{client: redis.NewClient(opts)}, nil
}

// PublishBatch publishes events grouped by H3 cell using a pipeline.
// Failures are non-fatal — logged but not returned as errors.
func (p *Publisher) PublishBatch(ctx context.Context, events []model.FukanEvent) {
	pipe := p.client.Pipeline()
	for _, e := range events {
		channel := fmt.Sprintf("telemetry:%d", e.H3Cell)
		data, err := json.Marshal(e)
		if err != nil {
			slog.Warn("redis marshal failed", "asset_id", e.AssetID, "err", err)
			continue
		}
		pipe.Publish(ctx, channel, data)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("redis pipeline exec failed", "err", err)
	}
}

// Close closes the Redis connection.
func (p *Publisher) Close() error {
	return p.client.Close()
}
