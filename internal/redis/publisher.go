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

// telemetryResolutions are the H3 resolutions a single telemetry event is
// broadcast at. Covering res 2 through 7 lets the frontend subscribe at any
// altitude band without requiring server-side child expansion.
var telemetryResolutions = []int{2, 3, 4, 5, 6, 7}

// bgpResolutions broadcasts BGP events at a single coarse resolution. BGP
// event coordinates are themselves imprecise (geolocated from the origin AS's
// HQ lat/lon, not actual routing infrastructure), so zoom-band-precise
// subscriptions would be misleading. The frontend always subscribes at
// res 3 regardless of camera altitude — see app/frontend/hooks/useAnyCable.ts.
var bgpResolutions = []int{3}

// Publisher broadcasts events to anycable-go via Redis pub/sub using the
// AnyCable broadcast envelope: {"stream": "<prefix>:<h3_hex>", "data": "<event_json>"}.
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

// PublishBatch emits telemetry envelopes (one per (event, resolution)
// combination) on the `telemetry:<h3_hex>` stream in a single Redis
// pipeline. Intended to be called from a single broadcaster goroutine so
// Redis sees a small, steady number of pipeline Execs regardless of event
// rate.
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
		if err := queueEnvelopes(ctx, pipe, data, h3.Cell(e.H3Cell), "telemetry:", telemetryResolutions); err != nil {
			return err
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis publish pipeline: %w", err)
	}
	return nil
}

// PublishBGPBatch emits BGP envelopes on the `bgp:<h3_hex>` stream at a
// single resolution. Same pipelining semantics as PublishBatch.
func (p *Publisher) PublishBGPBatch(ctx context.Context, events []model.BgpEvent) error {
	if len(events) == 0 {
		return nil
	}
	pipe := p.client.Pipeline()
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal bgp event %s: %w", e.EventID, err)
		}
		if err := queueEnvelopes(ctx, pipe, data, h3.Cell(e.H3Cell), "bgp:", bgpResolutions); err != nil {
			return err
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis bgp publish pipeline: %w", err)
	}
	return nil
}

// queueEnvelopes appends one anycable envelope per target resolution to the
// pipeline. The H3 cell is coarsened to each resolution's parent via h3-go
// Cell.Parent; resolution 7 is passed through unchanged since all events are
// computed at that resolution.
func queueEnvelopes(ctx context.Context, pipe redis.Pipeliner, data []byte, cell h3.Cell, prefix string, resolutions []int) error {
	dataStr := string(data)
	for _, res := range resolutions {
		streamCell := cell
		if res != 7 {
			parent, pErr := cell.Parent(res)
			if pErr != nil {
				return fmt.Errorf("h3 parent res %d: %w", res, pErr)
			}
			streamCell = parent
		}
		envelope, mErr := json.Marshal(anycableEnvelope{
			Stream: prefix + streamCell.String(),
			Data:   dataStr,
		})
		if mErr != nil {
			return fmt.Errorf("marshal envelope res %d: %w", res, mErr)
		}
		pipe.Publish(ctx, anycableBroadcastChannel, envelope)
	}
	return nil
}

// Close closes the Redis connection.
func (p *Publisher) Close() error {
	return p.client.Close()
}
