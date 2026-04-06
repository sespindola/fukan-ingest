package main

import (
	"log/slog"
	"os"

	"github.com/sespindola/fukan-ingest/internal/commands"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	if err := commands.Execute(); err != nil {
		os.Exit(1)
	}
}
