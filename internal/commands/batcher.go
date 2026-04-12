package commands

import (
	"log/slog"

	"github.com/sespindola/fukan-ingest/internal/batcher"
	"github.com/sespindola/fukan-ingest/internal/clickhouse"
	fukanNats "github.com/sespindola/fukan-ingest/internal/nats"
	"github.com/sespindola/fukan-ingest/internal/redis"
	fukanSignal "github.com/sespindola/fukan-ingest/internal/signal"
	"github.com/spf13/cobra"
)

func newBatcherCmd() *cobra.Command {
	var batcherType string

	cmd := &cobra.Command{
		Use:   "batcher",
		Short: "Run ClickHouse batch inserter",
		RunE: func(cmd *cobra.Command, args []string) error {
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

			b := batcher.New(conn, cfg.ClickHouse, redisPub)

			nc, err := fukanNats.Connect(cfg.NATS.URL)
			if err != nil {
				return err
			}

			subject := "fukan.telemetry." + batcherType
			queue := "batcher-" + batcherType

			_, err = nc.QueueSubscribe(subject, queue, b.HandleMsg)
			if err != nil {
				return err
			}

			slog.Info("batcher started", "asset_type", batcherType, "subject", subject)

			<-ctx.Done()

			slog.Info("shutting down batcher")
			nc.Drain()
			b.DrainAndFlush()
			nc.Close()
			slog.Info("batcher stopped")
			return nil
		},
	}
	cmd.Flags().StringVar(&batcherType, "type", "aircraft", "asset type: aircraft, vessel, satellite")
	return cmd
}
