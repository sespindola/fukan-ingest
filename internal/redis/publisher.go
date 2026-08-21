package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	redisclient "github.com/redis/go-redis/v9"
	"github.com/uber/h3-go/v4"

	"github.com/sespindola/fukan-ingest/internal/model"
)

const anycableBroadcastChannel = "__anycable__"

var telemetryResolutions = []int{2, 3, 4, 5, 6, 7}
var bgpResolutions = []int{3}

const (
	liveWireVersion           = 1
	maxEventsPerStreamBatch   = 128
	maxStreamBatchBytes       = 48 * 1024
	maxPublicationBundleBytes = 256 * 1024
)

type Publisher struct {
	client *redisclient.Client
}

type anycableEnvelope struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

// wireFukanEvent keeps the internal/NATS H3 representation as UInt64 while
// emitting the canonical H3 hexadecimal string expected by h3-js. The
// explicit H3 field shadows the embedded struct's json:"h3" field.
type wireFukanEvent struct {
	model.FukanEvent
	H3 string `json:"h3"`
}

type wireBgpEvent struct {
	model.BgpEvent
	H3 string `json:"h3"`
}

type liveDeltaBatch struct {
	Type   string            `json:"type"`
	V      int               `json:"v"`
	SentAt int64             `json:"sent_at"`
	Events []json.RawMessage `json:"events"`
}

func NewPublisher(url string) (*Publisher, error) {
	opts, err := redisclient.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis parse url: %w", err)
	}
	return &Publisher{client: redisclient.NewClient(opts)}, nil
}

// PublishBatch coalesces transport overhead by emitting bounded arrays of the
// newest events grouped by exact AnyCable stream. Multiple publications are
// themselves bundled into one Redis PUBLISH payload. AnyCable still performs
// normal stream routing, but clients receive one delta batch per matching H3
// stream instead of one WebSocket frame per asset.
func (p *Publisher) PublishBatch(ctx context.Context, events []model.FukanEvent) error {
	if len(events) == 0 {
		return nil
	}

	var (
		envelopes []anycableEnvelope
		err       error
	)
	if os.Getenv("FUKAN_LIVE_BATCHES") == "false" {
		envelopes, err = buildLegacyTelemetryEnvelopes(events)
	} else {
		envelopes, err = buildTelemetryEnvelopes(events, time.Now().UnixMilli())
	}
	if err != nil {
		return err
	}
	return p.publishEnvelopeBundles(ctx, envelopes)
}

func buildLegacyTelemetryEnvelopes(events []model.FukanEvent) ([]anycableEnvelope, error) {
	envelopes := make([]anycableEnvelope, 0, len(events)*len(telemetryResolutions))
	for _, event := range events {
		wire, err := json.Marshal(wireFukanEvent{
			FukanEvent: event,
			H3:         h3.Cell(event.H3Cell).String(),
		})
		if err != nil {
			return nil, fmt.Errorf("marshal event %s: %w", event.AssetID, err)
		}
		cell := h3.Cell(event.H3Cell)
		for _, resolution := range telemetryResolutions {
			streamCell := cell
			if resolution != 7 {
				parent, err := cell.Parent(resolution)
				if err != nil {
					return nil, fmt.Errorf("h3 parent res %d: %w", resolution, err)
				}
				streamCell = parent
			}
			envelopes = append(envelopes, anycableEnvelope{
				Stream: "telemetry:" + string(event.AssetType) + ":" + streamCell.String(),
				Data:   string(wire),
			})
		}
	}
	return envelopes, nil
}

func buildTelemetryEnvelopes(events []model.FukanEvent, sentAt int64) ([]anycableEnvelope, error) {
	groups := make(map[string][]json.RawMessage)

	for _, event := range events {
		wire, err := json.Marshal(wireFukanEvent{
			FukanEvent: event,
			H3:         h3.Cell(event.H3Cell).String(),
		})
		if err != nil {
			return nil, fmt.Errorf("marshal event %s: %w", event.AssetID, err)
		}

		cell := h3.Cell(event.H3Cell)
		prefix := "telemetry:" + string(event.AssetType) + ":"
		for _, resolution := range telemetryResolutions {
			streamCell := cell
			if resolution != 7 {
				parent, err := cell.Parent(resolution)
				if err != nil {
					return nil, fmt.Errorf("h3 parent res %d: %w", resolution, err)
				}
				streamCell = parent
			}
			stream := prefix + streamCell.String()
			groups[stream] = append(groups[stream], json.RawMessage(wire))
		}
	}

	streams := make([]string, 0, len(groups))
	for stream := range groups {
		streams = append(streams, stream)
	}
	sort.Strings(streams)

	envelopes := make([]anycableEnvelope, 0, len(streams))
	for _, stream := range streams {
		chunks, err := chunkLiveEvents(groups[stream], sentAt)
		if err != nil {
			return nil, fmt.Errorf("build stream batch %s: %w", stream, err)
		}
		for _, payload := range chunks {
			envelopes = append(envelopes, anycableEnvelope{Stream: stream, Data: string(payload)})
		}
	}
	return envelopes, nil
}

func chunkLiveEvents(events []json.RawMessage, sentAt int64) ([][]byte, error) {
	var chunks [][]byte
	current := make([]json.RawMessage, 0, min(len(events), maxEventsPerStreamBatch))
	emptyPayload, err := json.Marshal(liveDeltaBatch{
		Type: "delta_batch", V: liveWireVersion, SentAt: sentAt, Events: []json.RawMessage{},
	})
	if err != nil {
		return nil, err
	}
	currentBytes := len(emptyPayload)

	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		payload, err := json.Marshal(liveDeltaBatch{
			Type:   "delta_batch",
			V:      liveWireVersion,
			SentAt: sentAt,
			Events: current,
		})
		if err != nil {
			return err
		}
		chunks = append(chunks, payload)
		current = make([]json.RawMessage, 0, maxEventsPerStreamBatch)
		currentBytes = len(emptyPayload)
		return nil
	}

	for _, event := range events {
		additionalBytes := len(event)
		if len(current) > 0 {
			additionalBytes++ // comma between array elements
		}
		if len(current) > 0 && (len(current) >= maxEventsPerStreamBatch || currentBytes+additionalBytes > maxStreamBatchBytes) {
			if err := flush(); err != nil {
				return nil, err
			}
			additionalBytes = len(event)
		}
		current = append(current, event)
		currentBytes += additionalBytes
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return chunks, nil
}

func (p *Publisher) PublishBGPBatch(ctx context.Context, events []model.BgpEvent) error {
	if len(events) == 0 {
		return nil
	}

	envelopes := make([]anycableEnvelope, 0, len(events))
	for _, event := range events {
		data, err := json.Marshal(wireBgpEvent{
			BgpEvent: event,
			H3:       h3.Cell(event.H3Cell).String(),
		})
		if err != nil {
			return fmt.Errorf("marshal bgp event %s: %w", event.EventID, err)
		}
		cell := h3.Cell(event.H3Cell)
		for _, resolution := range bgpResolutions {
			parent, err := cell.Parent(resolution)
			if err != nil {
				return fmt.Errorf("h3 parent res %d: %w", resolution, err)
			}
			envelopes = append(envelopes, anycableEnvelope{
				Stream: "bgp:" + parent.String(),
				Data:   string(data),
			})
		}
	}
	return p.publishEnvelopeBundles(ctx, envelopes)
}

func (p *Publisher) publishEnvelopeBundles(ctx context.Context, envelopes []anycableEnvelope) error {
	pipe := p.client.Pipeline()
	bundle := make([]json.RawMessage, 0, len(envelopes))
	bundleBytes := 2

	flush := func() error {
		if len(bundle) == 0 {
			return nil
		}
		payload, err := json.Marshal(bundle)
		if err != nil {
			return fmt.Errorf("marshal publication bundle: %w", err)
		}
		pipe.Publish(ctx, anycableBroadcastChannel, payload)
		bundle = bundle[:0]
		bundleBytes = 2
		return nil
	}

	for _, envelope := range envelopes {
		raw, err := json.Marshal(envelope)
		if err != nil {
			return fmt.Errorf("marshal envelope: %w", err)
		}
		additional := len(raw)
		if len(bundle) > 0 {
			additional++
		}
		if len(bundle) > 0 && bundleBytes+additional > maxPublicationBundleBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		bundle = append(bundle, json.RawMessage(raw))
		bundleBytes += additional
	}
	if err := flush(); err != nil {
		return err
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis publish pipeline: %w", err)
	}
	return nil
}

func (p *Publisher) Close() error {
	return p.client.Close()
}
