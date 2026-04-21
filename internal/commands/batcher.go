package commands

import (
	"fmt"
	"log/slog"

	"github.com/ClickHouse/ch-go"
	"github.com/nats-io/nats.go"
	"github.com/sespindola/fukan-ingest/internal/batcher"
	"github.com/sespindola/fukan-ingest/internal/clickhouse"
	"github.com/sespindola/fukan-ingest/internal/config"
	fukanNats "github.com/sespindola/fukan-ingest/internal/nats"
	"github.com/sespindola/fukan-ingest/internal/redis"
	fukanSignal "github.com/sespindola/fukan-ingest/internal/signal"
	"github.com/spf13/cobra"
)

// knownBatcherTypes guards --type against typos that would otherwise
// subscribe to a dead NATS subject (e.g. fukan.telemetry.aircraftx) and
// sit idle forever looking healthy.
var knownBatcherTypes = map[string]bool{
	"aircraft":  true,
	"vessel":    true,
	"satellite": true,
	"bgp":       true,
}

func newBatcherCmd() *cobra.Command {
	var batcherType string

	cmd := &cobra.Command{
		Use:   "batcher",
		Short: "Run ClickHouse batch inserter",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !knownBatcherTypes[batcherType] {
				return fmt.Errorf("unknown batcher type %q: must be one of aircraft, vessel, satellite, bgp", batcherType)
			}

			cfg := configFrom(cmd)
			ctx, cancel := fukanSignal.NotifyCtx(cmd.Context())
			defer cancel()

			conn, err := clickhouse.Connect(ctx, cfg.ClickHouse)
			if err != nil {
				return err
			}
			defer conn.Close()

			redisPub, err := redis.NewPublisher(cfg.Redis.URL)
			if err != nil {
				return err
			}
			defer redisPub.Close()

			nc, err := fukanNats.Connect(cfg.NATS.URL)
			if err != nil {
				return err
			}

			drainFn, err := startBatcher(batcherType, conn, cfg.ClickHouse, redisPub, nc)
			if err != nil {
				nc.Close()
				return err
			}

			<-ctx.Done()

			slog.Info("shutting down batcher")
			nc.Drain()
			drainFn()
			nc.Close()
			slog.Info("batcher stopped")
			return nil
		},
	}
	cmd.Flags().StringVar(&batcherType, "type", "aircraft", "asset type: aircraft, vessel, satellite, bgp")
	return cmd
}

// startBatcher wires a batcher of the appropriate kind to a NATS subscription
// and returns a drain function to be called during shutdown. BGP events flow
// through a dedicated table + Redis stream prefix, so they use a separate
// batcher type rather than the generic telemetry path.
func startBatcher(
	batcherType string,
	conn *ch.Client,
	chCfg config.ClickHouseConfig,
	redisPub *redis.Publisher,
	nc *nats.Conn,
) (func(), error) {
	if batcherType == "bgp" {
		b := batcher.NewBGP(conn, chCfg, redisPub)
		subject := "fukan.bgp.events"
		queue := "batcher-bgp"
		if _, err := nc.QueueSubscribe(subject, queue, b.HandleMsg); err != nil {
			return nil, err
		}
		slog.Info("batcher started", "asset_type", batcherType, "subject", subject)
		return b.DrainAndFlush, nil
	}

	b := batcher.New(conn, chCfg, redisPub)
	subject := "fukan.telemetry." + batcherType
	queue := "batcher-" + batcherType
	if _, err := nc.QueueSubscribe(subject, queue, b.HandleMsg); err != nil {
		return nil, err
	}
	slog.Info("batcher started", "asset_type", batcherType, "subject", subject)
	return b.DrainAndFlush, nil
}
