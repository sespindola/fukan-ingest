package adsb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"
	fukanNats "github.com/sespindola/fukan-ingest/internal/nats"
	"github.com/sespindola/fukan-ingest/internal/oauth2"
	"github.com/sespindola/fukan-ingest/internal/worker"
)

// Option configures an ADSBWorker.
type Option func(*ADSBWorker)

func WithInterval(d time.Duration) Option {
	return func(w *ADSBWorker) { w.interval = d }
}

func WithAPIKey(key string) Option {
	return func(w *ADSBWorker) { w.apiKey = key }
}

func WithOAuth2(clientID, clientSecret, tokenURL string) Option {
	return func(w *ADSBWorker) {
		w.clientID = clientID
		w.clientSecret = clientSecret
		w.tokenURL = tokenURL
	}
}

// ADSBWorker consumes an ADS-B feed and publishes normalized events to NATS.
type ADSBWorker struct {
	apiURL       string
	source       string
	nc           *nats.Conn
	interval     time.Duration
	apiKey       string
	clientID     string
	clientSecret string
	tokenURL     string
}

func New(apiURL, source string, nc *nats.Conn, opts ...Option) *ADSBWorker {
	w := &ADSBWorker{
		apiURL:   apiURL,
		source:   source,
		nc:       nc,
		interval: 10 * time.Second,
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

func (w *ADSBWorker) Run(ctx context.Context) error {
	return worker.RunWithReconnect(ctx, w.Name(), w.poll)
}

func (w *ADSBWorker) Name() string {
	return "adsb:" + w.source
}

func (w *ADSBWorker) poll(ctx context.Context) error {
	var tokenSource *oauth2.TokenSource
	if w.clientID != "" {
		tokenURL := w.tokenURL
		if tokenURL == "" {
			tokenURL = oauth2.DefaultOpenSkyTokenURL
		}
		tokenSource = oauth2.NewTokenSource(w.clientID, w.clientSecret, tokenURL)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.fetchAndPublish(ctx, client, tokenSource); err != nil {
				return err
			}
		}
	}
}

func (w *ADSBWorker) fetchAndPublish(ctx context.Context, client *http.Client, tokenSource *oauth2.TokenSource) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.apiURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	if w.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+w.apiKey)
	} else if tokenSource != nil {
		token, err := tokenSource.Token()
		if err != nil {
			return fmt.Errorf("oauth2 token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opensky returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	events, err := ParseStates(body, w.source)
	if err != nil {
		return fmt.Errorf("parse states: %w", err)
	}

	for _, event := range events {
		if err := fukanNats.PublishJSON(w.nc, "fukan.telemetry.aircraft", event); err != nil {
			slog.Warn("nats publish failed", "asset_id", event.AssetID, "err", err)
		}
	}

	slog.Info("published events", "worker", w.Name(), "count", len(events))
	return nil
}
