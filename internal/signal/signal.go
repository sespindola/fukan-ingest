package signal

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// NotifyCtx cancels the returned context on SIGTERM/SIGINT.
// A second signal forces os.Exit(1).
func NotifyCtx(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		select {
		case <-sigCh:
			slog.Info("received signal, shutting down")
			cancel()
		case <-ctx.Done():
			signal.Stop(sigCh)
			return
		}
		select {
		case <-sigCh:
			slog.Info("received second signal, forcing exit")
			os.Exit(1)
		case <-ctx.Done():
		}
		signal.Stop(sigCh)
	}()

	return ctx, cancel
}
