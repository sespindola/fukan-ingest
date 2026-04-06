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
		Long:  "Refresh reference data from external sources.\n\nValid targets:\n  airlines   Download and import airline/aircraft reference data from OpenSky",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd)
			ctx, cancel := fukanSignal.NotifyCtx(cmd.Context())
			defer cancel()

			switch refreshTarget {
			case "airlines":
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

				conn, err := clickhouse.Connect(ctx, cfg.ClickHouse)
				if err != nil {
					return err
				}
				defer conn.Close()

				return refresh.Aircraft(ctx, conn, cfg.OpenSky.CSVURL, token, false)
			default:
				return fmt.Errorf("unknown refresh target: %s (supported: airlines)", refreshTarget)
			}
		},
	}
	cmd.Flags().StringVar(&refreshTarget, "target", "", "refresh target: airlines")
	_ = cmd.MarkFlagRequired("target")
	cmd.Flags().BoolVar(&refreshDryRun, "dry-run", false, "download and parse but skip DB writes")
	return cmd
}
