package commands

import (
	"embed"
	"fmt"
	"log/slog"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/clickhouse"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/spf13/cobra"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Run ClickHouse schema migrations",
	}

	cmd.AddCommand(newMigrateUpCmd())
	cmd.AddCommand(newMigrateDownCmd())
	cmd.AddCommand(newMigrateVersionCmd())

	return cmd
}

func newMigrator(cmd *cobra.Command) (*migrate.Migrate, error) {
	cfg := configFrom(cmd)

	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migration source: %w", err)
	}

	url := fmt.Sprintf("clickhouse://%s?username=%s&password=%s&database=%s&x-multi-statement=true&x-migrations-table-engine=MergeTree",
		cfg.ClickHouse.Addr,
		cfg.ClickHouse.User,
		cfg.ClickHouse.Password,
		cfg.ClickHouse.Database,
	)

	m, err := migrate.NewWithSourceInstance("iofs", source, url)
	if err != nil {
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return m, nil
}

func newMigrateUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Apply all pending migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := newMigrator(cmd)
			if err != nil {
				return err
			}
			defer m.Close()

			if err := m.Up(); err != nil && err != migrate.ErrNoChange {
				return fmt.Errorf("migrate up: %w", err)
			}

			v, dirty, _ := m.Version()
			slog.Info("migrations applied", "version", v, "dirty", dirty)
			return nil
		},
	}
}

func newMigrateDownCmd() *cobra.Command {
	var steps int

	cmd := &cobra.Command{
		Use:   "down",
		Short: "Revert migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := newMigrator(cmd)
			if err != nil {
				return err
			}
			defer m.Close()

			if steps > 0 {
				if err := m.Steps(-steps); err != nil && err != migrate.ErrNoChange {
					return fmt.Errorf("migrate down: %w", err)
				}
			} else {
				if err := m.Down(); err != nil && err != migrate.ErrNoChange {
					return fmt.Errorf("migrate down: %w", err)
				}
			}

			v, dirty, verr := m.Version()
			if verr == migrate.ErrNilVersion {
				slog.Info("all migrations reverted")
			} else {
				slog.Info("migrations reverted", "version", v, "dirty", dirty)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&steps, "steps", "n", 0, "number of migrations to revert (default: all)")
	return cmd
}

func newMigrateVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print current migration version",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := newMigrator(cmd)
			if err != nil {
				return err
			}
			defer m.Close()

			v, dirty, err := m.Version()
			if err == migrate.ErrNilVersion {
				fmt.Println("no migrations applied")
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Printf("version: %d, dirty: %v\n", v, dirty)
			return nil
		},
	}
}
