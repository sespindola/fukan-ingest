package bgp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/nats-io/nats.go"
	fukanNats "github.com/sespindola/fukan-ingest/internal/nats"
	"github.com/sespindola/fukan-ingest/internal/worker"
)

const defaultWSURL = "wss://ris-live.ripe.net/v1/ws/?client=fukan-ingest"

type BGPWorker struct {
	wsURL  string
	source string
	nc     *nats.Conn
	geo    *Geo
	state  *PrefixState
	city   *CityLookup
	asn    *ASNLookup
}

func New(wsURL, source string, nc *nats.Conn, city *CityLookup, asn *ASNLookup) *BGPWorker {
	if wsURL == "" {
		wsURL = defaultWSURL
	}
	return &BGPWorker{
		wsURL:  wsURL,
		source: source,
		nc:     nc,
		geo:    NewGeo(),
		state:  NewPrefixState(500_000),
		city:   city,
		asn:    asn,
	}
}

func (w *BGPWorker) Name() string {
	return "bgp:" + w.source
}

func (w *BGPWorker) Run(ctx context.Context) error {
	return worker.RunWithReconnect(ctx, w.Name(), w.stream)
}

type risSubscribe struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

func (w *BGPWorker) stream(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, w.wsURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "shutdown")

	conn.SetReadLimit(1 << 20)

	sub := risSubscribe{
		Type: "ris_subscribe",
		Data: map[string]string{"type": "UPDATE"},
	}
	subData, err := json.Marshal(sub)
	if err != nil {
		return fmt.Errorf("marshal subscription: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, subData); err != nil {
		return fmt.Errorf("ws write subscription: %w", err)
	}

	var (
		processed atomic.Int64
		emitted   atomic.Int64
	)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}

		events, err := ParseMessage(data, w.source, w.geo, w.state, w.city, w.asn)
		if err != nil {
			slog.Warn("bgp parse failed", "err", err)
			continue
		}

		for _, event := range events {
			if err := fukanNats.PublishJSON(w.nc, "fukan.bgp.events", event); err != nil {
				slog.Warn("nats publish failed", "event_id", event.EventID, "err", err)
				continue
			}
			emitted.Add(1)
		}

		if n := processed.Add(1); n%10000 == 0 {
			slog.Info("bgp messages processed",
				"worker", w.Name(),
				"processed", n,
				"emitted", emitted.Load())
		}
	}
}
