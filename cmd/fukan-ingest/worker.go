package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sespindola/fukan-ingest/internal/config"
	natsPub "github.com/sespindola/fukan-ingest/internal/nats"
	"github.com/sespindola/fukan-ingest/internal/oauth2"
	"github.com/sespindola/fukan-ingest/internal/worker"
	"github.com/sespindola/fukan-ingest/internal/worker/adsb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

var workerType string

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Run feed ingestion worker(s)",
	RunE:  runWorker,
}

func init() {
	workerCmd.Flags().StringVar(&workerType, "type", "", "feed type (e.g. adsb)")
	_ = workerCmd.MarkFlagRequired("type")
	rootCmd.AddCommand(workerCmd)
}

func runWorker(cmd *cobra.Command, args []string) error {
	integrations := cfg.Integrations[workerType]

	// Legacy fallback: if no integrations configured, check env vars.
	if len(integrations) == 0 && workerType == "adsb" {
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

	// Inject OpenSky OAuth2 creds from env vars if not set in YAML.
	openSkyClientID := viper.GetString("opensky.client_id")
	openSkyClientSecret := viper.GetString("opensky.client_secret")
	for i := range integrations {
		if integrations[i].ClientID == "" && openSkyClientID != "" {
			integrations[i].ClientID = openSkyClientID
		}
		if integrations[i].ClientSecret == "" && openSkyClientSecret != "" {
			integrations[i].ClientSecret = openSkyClientSecret
		}
	}

	if len(integrations) == 0 {
		return fmt.Errorf("no integrations configured for feed type %q", workerType)
	}

	pub, err := natsPub.NewPublisher(cfg.NATS.URL)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer pub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		slog.Info("shutting down workers")
		cancel()
	}()

	g, gctx := errgroup.WithContext(ctx)

	for _, ic := range integrations {
		w, err := newWorker(workerType, ic, pub)
		if err != nil {
			cancel()
			return err
		}
		slog.Info("starting worker", "worker", w.Name())
		g.Go(func() error {
			return w.Run(gctx)
		})
	}

	if err := g.Wait(); err != nil {
		slog.Error("worker failed", "err", err)
	}

	if err := pub.Drain(); err != nil {
		slog.Warn("nats drain failed", "err", err)
	}
	slog.Info("all workers stopped")
	return nil
}

func newWorker(feedType string, ic config.IntegrationConfig, pub natsPub.Publisher) (worker.Worker, error) {
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
		return adsb.New(ic.APIURL, ic.Name, pub, opts...), nil
	default:
		return nil, fmt.Errorf("unknown feed type: %s", feedType)
	}
}
