package batcher

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/nats-io/nats.go"
	"github.com/sespindola/fukan-ingest/internal/clickhouse"
	"github.com/sespindola/fukan-ingest/internal/model"
	"github.com/sespindola/fukan-ingest/internal/redis"
)

const (
	MaxBatchSize    = 10_000
	MaxFlushLatency = 2 * time.Second
	MaxRetryBuffer  = 10

	maxRetryAttempts    = 5
	initialRetryBackoff = 100 * time.Millisecond
	maxRetryBackoff     = 5 * time.Second
)

// Batcher accumulates events and flushes on whichever threshold hits first:
// MaxBatchSize events or MaxFlushLatency elapsed.
type Batcher struct {
	ch       *ch.Client
	redis    *redis.Publisher
	buf      []model.FukanEvent
	mu       sync.Mutex
	timer    *time.Timer
	retrySem chan struct{}
	retryWg  sync.WaitGroup
}

func New(ch *ch.Client, redis *redis.Publisher) *Batcher {
	return &Batcher{
		ch:       ch,
		redis:    redis,
		buf:      make([]model.FukanEvent, 0, MaxBatchSize),
		retrySem: make(chan struct{}, MaxRetryBuffer),
	}
}

// HandleMsg is the NATS message handler. It unmarshals, validates, and buffers events.
func (b *Batcher) HandleMsg(msg *nats.Msg) {
	var event model.FukanEvent
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		slog.Warn("batcher unmarshal failed", "err", err)
		return
	}
	if err := model.Validate(event); err != nil {
		slog.Warn("batcher validation failed", "err", err)
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf = append(b.buf, event)
	if len(b.buf) >= MaxBatchSize {
		b.flushLocked()
	} else if b.timer == nil {
		b.timer = time.AfterFunc(MaxFlushLatency, func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.flushLocked()
		})
	}
}

func (b *Batcher) flushLocked() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if len(b.buf) == 0 {
		return
	}

	batch := b.buf
	b.buf = make([]model.FukanEvent, 0, MaxBatchSize)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := clickhouse.InsertBatch(ctx, b.ch, batch); err != nil {
		slog.Error("clickhouse insert failed, scheduling retry",
			"err", err, "count", len(batch))
		b.scheduleRetry(batch)
		return
	}
	slog.Info("flushed batch", "count", len(batch))

	if err := b.redis.PublishBatch(ctx, batch); err != nil {
		slog.Warn("redis publish failed", "err", err)
	}
}

func (b *Batcher) scheduleRetry(batch []model.FukanEvent) {
	select {
	case b.retrySem <- struct{}{}:
	default:
		slog.Error("retry buffer full, dropping batch", "count", len(batch))
		return
	}
	b.retryWg.Add(1)
	go func() {
		defer b.retryWg.Done()
		defer func() { <-b.retrySem }()
		b.retryInsert(batch)
	}()
}

func (b *Batcher) retryInsert(batch []model.FukanEvent) {
	backoff := initialRetryBackoff
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		time.Sleep(backoff)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := clickhouse.InsertBatch(ctx, b.ch, batch)
		cancel()

		if err == nil {
			slog.Info("retry succeeded", "attempt", attempt, "count", len(batch))
			rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
			if rerr := b.redis.PublishBatch(rctx, batch); rerr != nil {
				slog.Warn("redis publish failed on retry", "err", rerr)
			}
			rcancel()
			return
		}

		slog.Warn("retry failed", "attempt", attempt, "err", err, "count", len(batch))
		backoff = min(backoff*2, maxRetryBackoff)
	}
	slog.Error("all retries exhausted, dropping batch", "count", len(batch))
}

// DrainAndFlush flushes any remaining buffered events and waits for in-flight retries.
func (b *Batcher) DrainAndFlush() {
	b.mu.Lock()
	b.flushLocked()
	b.mu.Unlock()
	b.retryWg.Wait()
}
