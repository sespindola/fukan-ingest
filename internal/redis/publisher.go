package redis

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/uber/h3-go/v4"

	"github.com/sespindola/fukan-ingest/internal/model"
)

// anycableBroadcastChannel is the Redis pub/sub channel anycable-go subscribes
// to when configured with `broadcast_adapters = ["redis"]`. It matches
// anycable-go's --redis_channel default.
const anycableBroadcastChannel = "__anycable__"

// broadcastResolutions are the H3 resolutions a single event is broadcast at.
// Each event fans out to one stream per resolution so the client can subscribe
// at any altitude band the frontend chooses in types/globe.ts. Covering res 2
// through 7 means a browser zoomed out to continental view (res 3, ~185 cells
// over Iberia) and a browser zoomed to ground level (res 7) both receive
// matching broadcasts without requiring server-side child expansion.
var broadcastResolutions = []int{2, 3, 4, 5, 6, 7}

// Publisher broadcasts FukanEvents to anycable-go via Redis pub/sub using the
// AnyCable broadcast envelope: {"stream": "telemetry:<h3_hex>", "data": "<event_json>"}.
// H3 cells are encoded in the h3-js canonical hex form (h3-go Cell.String()),
// which matches what the browser's polygonToCells() returns.
type Publisher struct {
	client *redis.Client
}

// anycableEnvelope is the wire format anycable-go expects on the broadcast
// channel. `data` is a JSON-encoded string that is passed through to the
// client's ActionCable subscription `received` callback.
type anycableEnvelope struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

// NewPublisher creates a Redis publisher from a URL (e.g. "redis://localhost:6379/0").
func NewPublisher(url string) (*Publisher, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis parse url: %w", err)
	}
	return &Publisher{client: redis.NewClient(opts)}, nil
}

// PublishBatch emits one envelope per (event, resolution) combination in a
// single Redis pipeline. Intended to be called from a single broadcaster
// goroutine so Redis sees a small, steady number of pipeline Execs regardless
// of event rate, avoiding the connection-pool contention that goroutine-per-
// event publishing causes under burst.
func (p *Publisher) PublishBatch(ctx context.Context, events []model.FukanEvent) error {
	if len(events) == 0 {
		return nil
	}
	pipe := p.client.Pipeline()
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal event %s: %w", e.AssetID, err)
		}
		dataStr := string(data)

		cell := h3.Cell(e.H3Cell)
		for _, res := range broadcastResolutions {
			streamCell := cell
			if res != 7 {
				parent, pErr := cell.Parent(res)
				if pErr != nil {
					return fmt.Errorf("h3 parent res %d: %w", res, pErr)
				}
				streamCell = parent
			}
			envelope, mErr := json.Marshal(anycableEnvelope{
				Stream: "telemetry:" + streamCell.String(),
				Data:   dataStr,
			})
			if mErr != nil {
				return fmt.Errorf("marshal envelope res %d: %w", res, mErr)
			}
			pipe.Publish(ctx, anycableBroadcastChannel, envelope)
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis publish pipeline: %w", err)
	}
	return nil
}

// Close closes the Redis connection.
func (p *Publisher) Close() error {
	return p.client.Close()
}
