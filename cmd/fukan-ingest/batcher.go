package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sespindola/fukan-ingest/internal/batcher"
	"github.com/sespindola/fukan-ingest/internal/clickhouse"
	natsSub "github.com/sespindola/fukan-ingest/internal/nats"
	redisPub "github.com/sespindola/fukan-ingest/internal/redis"
	"github.com/spf13/cobra"
)

var batcherType string

var batcherCmd = &cobra.Command{
	Use:   "batcher",
	Short: "Run ClickHouse batch inserter",
	RunE:  runBatcher,
}

func init() {
	batcherCmd.Flags().StringVar(&batcherType, "type", "aircraft", "asset type (e.g. aircraft)")
	rootCmd.AddCommand(batcherCmd)
}

func runBatcher(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chClient, err := clickhouse.NewClient(ctx, cfg.ClickHouse.Addr, cfg.ClickHouse.Database, cfg.ClickHouse.User, cfg.ClickHouse.Password)
	if err != nil {
		return err
	}
	defer chClient.Close()

	if err := chClient.Ping(ctx); err != nil {
		return err
	}

	redisClient, err := redisPub.NewPublisher(cfg.Redis.URL)
	if err != nil {
		return err
	}
	defer redisClient.Close()

	b := batcher.New(chClient, redisClient)

	sub, err := natsSub.NewSubscriber(cfg.NATS.URL)
	if err != nil {
		return err
	}

	subject := "fukan.telemetry." + batcherType
	queue := "batcher-" + batcherType

	_, err = sub.QueueSubscribe(subject, queue, b.HandleMsg)
	if err != nil {
		return err
	}

	slog.Info("batcher started", "asset_type", batcherType, "subject", subject)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	slog.Info("shutting down batcher")
	sub.Drain()
	b.DrainAndFlush()
	sub.Close()
	slog.Info("batcher stopped")
	return nil
}
