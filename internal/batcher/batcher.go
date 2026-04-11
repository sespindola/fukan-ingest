package batcher

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/nats-io/nats.go"
	"github.com/sespindola/fukan-ingest/internal/clickhouse"
	"github.com/sespindola/fukan-ingest/internal/config"
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

const (
	// broadcastBufferSize caps the in-memory queue of events awaiting broadcast.
	// Sized to absorb a multi-second burst at the projected aggregate event rate
	// (aircraft + AIS + BGP + ...). On overflow events are dropped with a warning.
	broadcastBufferSize = 50_000
	// broadcastBatchSize is the max number of events coalesced into a single
	// Redis pipeline Exec.
	broadcastBatchSize = 500
	// broadcastFlushInterval bounds the broadcaster's latency when events
	// arrive slowly. At 500 events/s the size threshold hits first; at idle
	// any pending event is still flushed within this window.
	broadcastFlushInterval = 50 * time.Millisecond
	// broadcastPublishTimeout bounds a single Redis pipeline Exec. Generous
	// because only one Exec is in flight at a time (single broadcaster).
	broadcastPublishTimeout = 5 * time.Second
)

// Batcher accumulates events and flushes to ClickHouse on whichever threshold
// hits first: MaxBatchSize events or MaxFlushLatency elapsed. Broadcasts to
// anycable-go via Redis pub/sub happen on a separate goroutine fed by a
// bounded channel from the NATS arrival path, decoupled from the ClickHouse
// flush so live subscribers see updates within ~50 ms regardless of the
// ClickHouse buffer size and without unbounded goroutine spawning.
type Batcher struct {
	ch            *ch.Client
	chCfg         config.ClickHouseConfig
	redis         *redis.Publisher
	buf           []model.FukanEvent
	mu            sync.Mutex
	timer         *time.Timer
	retrySem      chan struct{}
	retryWg       sync.WaitGroup
	broadcastCh   chan model.FukanEvent
	broadcastDone chan struct{}
}

func New(ch *ch.Client, chCfg config.ClickHouseConfig, redis *redis.Publisher) *Batcher {
	b := &Batcher{
		ch:            ch,
		chCfg:         chCfg,
		redis:         redis,
		buf:           make([]model.FukanEvent, 0, MaxBatchSize),
		retrySem:      make(chan struct{}, MaxRetryBuffer),
		broadcastCh:   make(chan model.FukanEvent, broadcastBufferSize),
		broadcastDone: make(chan struct{}),
	}
	go b.broadcastLoop()
	return b
}

// HandleMsg is the NATS message handler. It unmarshals, validates, enqueues
// the event for live broadcast, and buffers it for ClickHouse persistence.
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

	select {
	case b.broadcastCh <- event:
	default:
		slog.Warn("broadcast buffer full, dropping event", "asset_id", event.AssetID)
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

// broadcastLoop is the sole consumer of broadcastCh. It accumulates events
// and flushes them to Redis either when broadcastBatchSize is reached or
// broadcastFlushInterval elapses with a non-empty batch. Runs until
// broadcastCh is closed by DrainAndFlush, at which point it flushes any
// remainder and signals broadcastDone.
func (b *Batcher) broadcastLoop() {
	defer close(b.broadcastDone)

	batch := make([]model.FukanEvent, 0, broadcastBatchSize)
	ticker := time.NewTicker(broadcastFlushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), broadcastPublishTimeout)
		if err := b.redis.PublishBatch(ctx, batch); err != nil {
			slog.Warn("broadcast publish failed", "err", err, "count", len(batch))
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

func (b *Batcher) reconnect() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	newConn, err := clickhouse.Connect(ctx, b.chCfg)
	if err != nil {
		slog.Warn("clickhouse reconnect failed", "err", err)
		return
	}

	b.mu.Lock()
	old := b.ch
	b.ch = newConn
	b.mu.Unlock()

	old.Close()
	slog.Info("clickhouse reconnected")
}

func (b *Batcher) retryInsert(batch []model.FukanEvent) {
	backoff := initialRetryBackoff
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		time.Sleep(backoff)

		b.mu.Lock()
		conn := b.ch
		b.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := clickhouse.InsertBatch(ctx, conn, batch)
		cancel()

		if err == nil {
			slog.Info("retry succeeded", "attempt", attempt, "count", len(batch))
			return
		}

		slog.Warn("retry failed", "attempt", attempt, "err", err, "count", len(batch))

		if isClientClosed(err) {
			b.reconnect()
		}

		backoff = min(backoff*2, maxRetryBackoff)
	}
	slog.Error("all retries exhausted, dropping batch", "count", len(batch))
}

func isClientClosed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "client is closed")
}

// DrainAndFlush flushes any remaining buffered events, closes the broadcast
// channel so the broadcaster goroutine drains and returns, and waits for
// in-flight ClickHouse retries before the Redis connection can close cleanly.
func (b *Batcher) DrainAndFlush() {
	b.mu.Lock()
	b.flushLocked()
	b.mu.Unlock()
	b.retryWg.Wait()
	close(b.broadcastCh)
	<-b.broadcastDone
}
