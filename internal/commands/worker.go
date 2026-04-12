package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sespindola/fukan-ingest/internal/config"
	fukanNats "github.com/sespindola/fukan-ingest/internal/nats"
	"github.com/sespindola/fukan-ingest/internal/oauth2"
	fukanSignal "github.com/sespindola/fukan-ingest/internal/signal"
	"github.com/sespindola/fukan-ingest/internal/worker"
	"github.com/sespindola/fukan-ingest/internal/worker/adsb"
	"github.com/sespindola/fukan-ingest/internal/worker/ais"
	tleWorker "github.com/sespindola/fukan-ingest/internal/worker/tle"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

func newWorkerCmd() *cobra.Command {
	var workerType string

	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run feed ingestion worker(s)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorker(cmd.Context(), configFrom(cmd), workerType)
		},
	}
	cmd.Flags().StringVar(&workerType, "type", "", "feed type: adsb, ais, tle")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func runWorker(ctx context.Context, cfg *config.Config, feedType string) error {
	integrations := cfg.Integrations[feedType]

	// Legacy fallback: if no integrations configured, check env vars.
	if len(integrations) == 0 && feedType == "adsb" {
		feedURL := os.Getenv("ADSB_FEED_URL")
		if feedURL != "" {
			source := os.Getenv("ADSB_SOURCE")
			if source == "" {
				source = "adsb_exchange"
			}
			integrations = []config.IntegrationConfig{
				{Name: source, APIURL: feedURL},
			}
		}
	}

	if len(integrations) == 0 {
		return fmt.Errorf("no integrations configured for feed type %q", feedType)
	}

	nc, err := fukanNats.Connect(cfg.NATS.URL)
	if err != nil {
		return err
	}
	defer nc.Close()

	ctx, cancel := fukanSignal.NotifyCtx(ctx)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)

	// TLE workers handle multiple sources internally (CelesTrak + classified).
	// Other feed types spawn one worker per integration.
	if feedType == "tle" {
		w := newTLEWorker(integrations, nc)
		slog.Info("starting worker", "worker", w.Name())
		g.Go(func() error {
			return w.Run(gctx)
		})
	} else {
		for _, ic := range integrations {
			w, err := newWorker(feedType, ic, nc)
			if err != nil {
				cancel()
				return err
			}
			slog.Info("starting worker", "worker", w.Name())
			g.Go(func() error {
				return w.Run(gctx)
			})
		}
	}

	if err := g.Wait(); err != nil {
		slog.Error("worker failed", "err", err)
	}

	if err := nc.Drain(); err != nil {
		slog.Warn("nats drain failed", "err", err)
	}
	slog.Info("all workers stopped")
	return nil
}

func newWorker(feedType string, ic config.IntegrationConfig, nc *nats.Conn) (worker.Worker, error) {
	switch feedType {
	case "adsb":
		var opts []adsb.Option
		if ic.Interval > 0 {
			opts = append(opts, adsb.WithInterval(time.Duration(ic.Interval)*time.Second))
		}
		if ic.APIKey != "" {
			opts = append(opts, adsb.WithAPIKey(ic.APIKey))
		}
		if ic.ClientID != "" && ic.ClientSecret != "" {
			tokenURL := ic.TokenURL
			if tokenURL == "" {
				tokenURL = oauth2.DefaultOpenSkyTokenURL
			}
			opts = append(opts, adsb.WithOAuth2(ic.ClientID, ic.ClientSecret, tokenURL))
		}
		return adsb.New(ic.APIURL, ic.Name, nc, opts...), nil
	case "ais":
		var opts []ais.Option
		if ic.APIKey != "" {
			opts = append(opts, ais.WithAPIKey(ic.APIKey))
		}
		return ais.New(ic.APIURL, ic.Name, nc, opts...), nil
	default:
		return nil, fmt.Errorf("unknown feed type: %s", feedType)
	}
}

func newTLEWorker(integrations []config.IntegrationConfig, nc *nats.Conn) worker.Worker {
	// Find the primary (CelesTrak) and optional classified integration.
	var celestrakURL, celestrakName, classifiedURL string
	for _, ic := range integrations {
		if ic.Name == "classfd" {
			classifiedURL = ic.APIURL
		} else {
			celestrakURL = ic.APIURL
			celestrakName = ic.Name
		}
	}
	var opts []tleWorker.Option
	if classifiedURL != "" {
		opts = append(opts, tleWorker.WithClassifiedURL(classifiedURL))
	}
	return tleWorker.New(celestrakURL, celestrakName, nc, opts...)
}
