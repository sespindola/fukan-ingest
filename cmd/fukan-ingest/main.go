package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
