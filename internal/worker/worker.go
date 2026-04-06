package worker

import (
	"context"
	"log/slog"
	"time"
)

// Worker is a feed consumer that runs until the context is cancelled.
type Worker interface {
	Run(ctx context.Context) error
	Name() string
}

// RunWithReconnect wraps fn in an exponential backoff reconnect loop.
// Backoff starts at 1s, doubles each attempt, caps at 60s.
// If fn runs for longer than 60s before failing, the backoff resets to 1s
// (long-lived connection was healthy; next failure is a fresh incident).
// Returns only when ctx is cancelled.
func RunWithReconnect(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	const (
		initialBackoff = 1 * time.Second
		maxBackoff     = 60 * time.Second
		healthyAfter   = 60 * time.Second
	)
	backoff := initialBackoff

	for {
		start := time.Now()
		err := fn(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) > healthyAfter {
			backoff = initialBackoff
		}
		slog.Warn("worker failed, reconnecting",
			"worker", name, "err", err, "backoff", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, maxBackoff)
	}
}
