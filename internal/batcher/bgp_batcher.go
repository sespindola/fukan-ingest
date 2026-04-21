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
	"github.com/sespindola/fukan-ingest/internal/config"
	"github.com/sespindola/fukan-ingest/internal/model"
	"github.com/sespindola/fukan-ingest/internal/redis"
)

// BGPBatcher accumulates BgpEvents and flushes to fukan.bgp_events on whichever
// threshold hits first: MaxBatchSize events or MaxFlushLatency elapsed. A
// dedicated broadcast goroutine handles Redis pub/sub on the `bgp:` stream
// prefix (single H3 resolution). Structure mirrors the telemetry Batcher —
// kept as a parallel type rather than a generic because the two paths call
// different ClickHouse insert + Redis publish methods.
type BGPBatcher struct {
	ch            *ch.Client
	chCfg         config.ClickHouseConfig
	redis         *redis.Publisher
	buf           []model.BgpEvent
	mu            sync.Mutex
	timer         *time.Timer
	retrySem      chan struct{}
	retryWg       sync.WaitGroup
	broadcastCh   chan model.BgpEvent
	broadcastDone chan struct{}
}

func NewBGP(ch *ch.Client, chCfg config.ClickHouseConfig, redis *redis.Publisher) *BGPBatcher {
	b := &BGPBatcher{
		ch:            ch,
		chCfg:         chCfg,
		redis:         redis,
		buf:           make([]model.BgpEvent, 0, MaxBatchSize),
		retrySem:      make(chan struct{}, MaxRetryBuffer),
		broadcastCh:   make(chan model.BgpEvent, broadcastBufferSize),
		broadcastDone: make(chan struct{}),
	}
	go b.broadcastLoop()
	return b
}

// HandleMsg is the NATS message handler. It unmarshals, validates, enqueues
// the event for live broadcast, and buffers it for ClickHouse persistence.
func (b *BGPBatcher) HandleMsg(msg *nats.Msg) {
	var event model.BgpEvent
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		slog.Warn("bgp batcher unmarshal failed", "err", err)
		return
	}
	if err := model.ValidateBgp(event); err != nil {
		slog.Warn("bgp batcher validation failed", "err", err)
		return
	}

	select {
	case b.broadcastCh <- event:
	default:
		slog.Warn("bgp broadcast buffer full, dropping event", "event_id", event.EventID)
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

func (b *BGPBatcher) broadcastLoop() {
	defer close(b.broadcastDone)

	batch := make([]model.BgpEvent, 0, broadcastBatchSize)
	ticker := time.NewTicker(broadcastFlushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), broadcastPublishTimeout)
		if err := b.redis.PublishBGPBatch(ctx, batch); err != nil {
			slog.Warn("bgp broadcast publish failed", "err", err, "count", len(batch))
		}
		cancel()
		batch = batch[:0]
	}

	for {
		select {
		case e, ok := <-b.broadcastCh:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			if len(batch) >= broadcastBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (b *BGPBatcher) flushLocked() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if len(b.buf) == 0 {
		return
	}

	batch := b.buf
	b.buf = make([]model.BgpEvent, 0, MaxBatchSize)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := clickhouse.InsertBGPBatch(ctx, b.ch, batch); err != nil {
		slog.Error("bgp clickhouse insert failed, scheduling retry",
			"err", err, "count", len(batch))
		b.scheduleRetry(batch)
		return
	}
	slog.Info("bgp flushed batch", "count", len(batch))
}

func (b *BGPBatcher) scheduleRetry(batch []model.BgpEvent) {
	select {
	case b.retrySem <- struct{}{}:
	default:
		slog.Error("bgp retry buffer full, dropping batch", "count", len(batch))
		return
	}
	b.retryWg.Add(1)
	go func() {
		defer b.retryWg.Done()
		defer func() { <-b.retrySem }()
		b.retryInsert(batch)
	}()
}

func (b *BGPBatcher) reconnect() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	newConn, err := clickhouse.Connect(ctx, b.chCfg)
	if err != nil {
		slog.Warn("bgp clickhouse reconnect failed", "err", err)
		return
	}

	b.mu.Lock()
	old := b.ch
	b.ch = newConn
	b.mu.Unlock()

	old.Close()
	slog.Info("bgp clickhouse reconnected")
}

func (b *BGPBatcher) retryInsert(batch []model.BgpEvent) {
	backoff := initialRetryBackoff
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		time.Sleep(backoff)

		b.mu.Lock()
		conn := b.ch
		b.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := clickhouse.InsertBGPBatch(ctx, conn, batch)
		cancel()

		if err == nil {
			slog.Info("bgp retry succeeded", "attempt", attempt, "count", len(batch))
			return
		}

		slog.Warn("bgp retry failed", "attempt", attempt, "err", err, "count", len(batch))

		if isClientClosed(err) {
			b.reconnect()
		}

		backoff = min(backoff*2, maxRetryBackoff)
	}
	slog.Error("bgp all retries exhausted, dropping batch", "count", len(batch))
}

// DrainAndFlush flushes any remaining buffered events, closes the broadcast
// channel so the broadcaster goroutine drains and returns, and waits for
// in-flight ClickHouse retries before the Redis connection can close cleanly.
func (b *BGPBatcher) DrainAndFlush() {
	b.mu.Lock()
	b.flushLocked()
	b.mu.Unlock()
	b.retryWg.Wait()
	close(b.broadcastCh)
	<-b.broadcastDone
}
