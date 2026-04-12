package ais

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/nats-io/nats.go"
	fukanNats "github.com/sespindola/fukan-ingest/internal/nats"
	"github.com/sespindola/fukan-ingest/internal/worker"
	"github.com/coder/websocket"
)

type Option func(*AISWorker)

func WithAPIKey(key string) Option {
	return func(w *AISWorker) { w.apiKey = key }
}

type AISWorker struct {
	wsURL  string
	source string
	nc     *nats.Conn
	apiKey string
}

func New(wsURL, source string, nc *nats.Conn, opts ...Option) *AISWorker {
	w := &AISWorker{
		wsURL:  wsURL,
		source: source,
		nc:     nc,
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

func (w *AISWorker) Run(ctx context.Context) error {
	return worker.RunWithReconnect(ctx, w.Name(), w.stream)
}

func (w *AISWorker) Name() string {
	return "ais:" + w.source
}

type subscription struct {
	APIKey             string          `json:"APIKey"`
	BoundingBoxes      [][][2]float64  `json:"BoundingBoxes"`
	FilterMessageTypes []string        `json:"FilterMessageTypes"`
}

func (w *AISWorker) stream(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, w.wsURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "shutdown")

	conn.SetReadLimit(1 << 20)

	sub := subscription{
		APIKey:             w.apiKey,
		BoundingBoxes:      [][][2]float64{{{-90, -180}, {90, 180}}},
		FilterMessageTypes: []string{"PositionReport", "StandardClassBPositionReport", "ShipStaticData"},
	}

	subData, err := json.Marshal(sub)
	if err != nil {
		return fmt.Errorf("marshal subscription: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, subData); err != nil {
		return fmt.Errorf("ws write subscription: %w", err)
	}

	var count atomic.Int64

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}

		event, ok := ParseMessage(data, w.source)
		if !ok {
			continue
		}

		if err := fukanNats.PublishJSON(w.nc, "fukan.telemetry.vessel", event); err != nil {
			slog.Warn("nats publish failed", "asset_id", event.AssetID, "err", err)
		}

		if n := count.Add(1); n%10000 == 0 {
			slog.Info("ais messages processed", "worker", w.Name(), "count", n)
		}
	}
}
