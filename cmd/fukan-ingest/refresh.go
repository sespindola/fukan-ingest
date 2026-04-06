package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sespindola/fukan-ingest/internal/clickhouse"
	"github.com/sespindola/fukan-ingest/internal/oauth2"
	"github.com/sespindola/fukan-ingest/internal/refresh"
	"github.com/spf13/cobra"
)

var (
	refreshTarget string
	refreshDryRun bool
)

var refreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Refresh reference data from external sources",
	Long:  "Refresh reference data from external sources.\n\nValid targets:\n  airlines   Download and import airline/aircraft reference data from OpenSky",
	RunE:  runRefresh,
}

func init() {
	refreshCmd.Flags().StringVar(&refreshTarget, "target", "", "refresh target: airlines")
	_ = refreshCmd.MarkFlagRequired("target")
	refreshCmd.Flags().BoolVar(&refreshDryRun, "dry-run", false, "download and parse but skip DB writes")
	rootCmd.AddCommand(refreshCmd)
}

func runRefresh(cmd *cobra.Command, args []string) error {
	switch refreshTarget {
	case "airlines":
		return refreshAirlines()
	default:
		return fmt.Errorf("unknown refresh target: %s (supported: airlines)", refreshTarget)
	}
}

func refreshAirlines() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		slog.Info("received signal, cancelling refresh")
		cancel()
		<-sigCh
		slog.Info("received second signal, forcing exit")
		os.Exit(1)
	}()

	// Obtain OAuth2 token if credentials are configured.
	var token string
	if cfg.OpenSky.ClientID != "" && cfg.OpenSky.ClientSecret != "" {
		ts := oauth2.NewTokenSource(cfg.OpenSky.ClientID, cfg.OpenSky.ClientSecret, oauth2.DefaultOpenSkyTokenURL)
		t, err := ts.Token()
		if err != nil {
			return fmt.Errorf("oauth2 token: %w", err)
		}
		token = t
		slog.Info("authenticated with OpenSky OAuth2")
	} else {
		slog.Info("no OpenSky OAuth2 credentials configured, downloading anonymously")
	}

	if refreshDryRun {
		slog.Info("dry-run mode — will parse CSV but skip ClickHouse writes")
		return refresh.Aircraft(ctx, nil, cfg.OpenSky.CSVURL, token, true)
	}

	chClient, err := clickhouse.NewClient(ctx, cfg.ClickHouse.Addr, cfg.ClickHouse.Database, cfg.ClickHouse.User, cfg.ClickHouse.Password)
	if err != nil {
		return fmt.Errorf("clickhouse connect: %w", err)
	}
	defer chClient.Close()

	if err := chClient.Ping(ctx); err != nil {
		return fmt.Errorf("clickhouse ping: %w", err)
	}

	return refresh.Aircraft(ctx, chClient.Conn(), cfg.OpenSky.CSVURL, token, false)
}
