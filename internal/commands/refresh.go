package commands

import (
	"fmt"
	"log/slog"

	"github.com/sespindola/fukan-ingest/internal/clickhouse"
	"github.com/sespindola/fukan-ingest/internal/oauth2"
	"github.com/sespindola/fukan-ingest/internal/refresh"
	fukanSignal "github.com/sespindola/fukan-ingest/internal/signal"
	"github.com/spf13/cobra"
)

func newRefreshCmd() *cobra.Command {
	var (
		refreshTarget string
		refreshDryRun bool
	)

	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Refresh reference data from external sources",
		Long:  "Refresh reference data from external sources.\n\nValid targets:\n  airlines     Download and import airline/aircraft reference data from OpenSky\n  satellites   Download and import satellite catalog from GCAT (planet4589.org)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd)
			ctx, cancel := fukanSignal.NotifyCtx(cmd.Context())
			defer cancel()

			switch refreshTarget {
			case "airlines":
				// Discover the latest aircraft CSV from the OpenSky S3 bucket.
				csvURL, err := refresh.DiscoverLatestAircraftCSV(ctx)
				if err != nil {
					return fmt.Errorf("discover aircraft CSV: %w", err)
				}

				// Read OAuth2 credentials from the first adsb integration entry.
				var token string
				if adsbIntegrations := cfg.Integrations["adsb"]; len(adsbIntegrations) > 0 {
					ic := adsbIntegrations[0]
					if ic.ClientID != "" && ic.ClientSecret != "" {
						tokenURL := ic.TokenURL
						if tokenURL == "" {
							tokenURL = oauth2.DefaultOpenSkyTokenURL
						}
						ts := oauth2.NewTokenSource(ic.ClientID, ic.ClientSecret, tokenURL)
						t, err := ts.Token()
						if err != nil {
							return fmt.Errorf("oauth2 token: %w", err)
						}
						token = t
						slog.Info("authenticated with OpenSky OAuth2")
					}
				}
				if token == "" {
					slog.Info("no OpenSky OAuth2 credentials configured, downloading anonymously")
				}

				if refreshDryRun {
					slog.Info("dry-run mode — will parse CSV but skip ClickHouse writes")
					return refresh.Aircraft(ctx, nil, csvURL, token, true)
				}

				conn, err := clickhouse.Connect(ctx, cfg.ClickHouse)
				if err != nil {
					return err
				}
				defer conn.Close()

				return refresh.Aircraft(ctx, conn, csvURL, token, false)

			case "satellites":
				if refreshDryRun {
					slog.Info("dry-run mode — will parse TSV but skip ClickHouse writes")
					return refresh.Satellite(ctx, nil, "", true)
				}

				conn, err := clickhouse.Connect(ctx, cfg.ClickHouse)
				if err != nil {
					return err
				}
				defer conn.Close()

				return refresh.Satellite(ctx, conn, "", false)

			default:
				return fmt.Errorf("unknown refresh target: %s (supported: airlines, satellites)", refreshTarget)
			}
		},
	}
	cmd.Flags().StringVar(&refreshTarget, "target", "", "refresh target: airlines, satellites")
	_ = cmd.MarkFlagRequired("target")
	cmd.Flags().BoolVar(&refreshDryRun, "dry-run", false, "download and parse but skip DB writes")
	return cmd
}
