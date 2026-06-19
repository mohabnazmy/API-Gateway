// Command gateway is the API Gateway entrypoint.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mohabnazmy/API-Gateway/internal/config"
	"github.com/mohabnazmy/API-Gateway/internal/registry"
	"github.com/mohabnazmy/API-Gateway/internal/server"
)

func main() {
	cfg, err := config.Load()
	logger := newLogger(logLevel(cfg, err))
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// The registry holds the live config snapshot the data plane reads. In Phase
	// 1 it is loaded once from the bootstrap config; later phases reload it from
	// the config store on change.
	reg := registry.New(logger)
	if err := reg.Load(cfg.Routes); err != nil {
		logger.Error("failed to load routes", "error", err)
		os.Exit(1)
	}

	srv := server.New(cfg, reg, logger)

	// Run the server, shutting down gracefully on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("gateway listening", "addr", cfg.ProxyAddr)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			os.Exit(1)
		}
		logger.Info("shutdown complete")
	}
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// logLevel resolves the desired log level even when config loading failed, so
// startup errors are still emitted at a sensible level.
func logLevel(cfg *config.Config, loadErr error) slog.Level {
	if loadErr != nil || cfg == nil {
		return slog.LevelInfo
	}
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
