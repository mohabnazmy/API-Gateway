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

	"golang.org/x/crypto/bcrypt"

	"github.com/mohabnazmy/API-Gateway/internal/admin"
	"github.com/mohabnazmy/API-Gateway/internal/config"
	"github.com/mohabnazmy/API-Gateway/internal/configsync"
	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/proxy"
	"github.com/mohabnazmy/API-Gateway/internal/registry"
	"github.com/mohabnazmy/API-Gateway/internal/server"
	"github.com/mohabnazmy/API-Gateway/internal/store"
)

func main() {
	// Load a .env file into the environment if present (path overridable via
	// GATEWAY_ENV_FILE). Real environment variables still take precedence, so
	// this is purely a local-dev convenience.
	if err := config.LoadDotEnv(os.Getenv("GATEWAY_ENV_FILE")); err != nil {
		newLogger(slog.LevelInfo).Error("failed to load env file", "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load()
	logger := newLogger(logLevel(cfg, err))
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// The registry holds the live config snapshot the data plane reads. The
	// upstream transport (with timeouts) applies to every route's reverse proxy.
	// Per-route upstream authentication (e.g. google_oidc) is built by
	// internal/upstreamauth during route compilation.
	reg := registry.New(logger, proxy.Options{
		Transport: proxy.NewTransport(cfg.UpstreamDialTimeout, cfg.UpstreamResponseTimeout),
	})

	// SQLite is the durable source of truth. On first run it is seeded from
	// GATEWAY_ROUTES; thereafter the store is authoritative. The reloader loads
	// the store into the registry and (when polling is enabled) hot-reloads on
	// config changes without a restart.
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Error("failed to open config store", "path", cfg.DBPath, "error", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	startupCtx := context.Background()
	if n, err := store.SeedRoutes(startupCtx, st, cfg.Routes); err != nil {
		logger.Error("failed to seed routes", "error", err)
		os.Exit(1)
	} else if n > 0 {
		logger.Info("seeded routes from GATEWAY_ROUTES into config store", "count", n, "path", cfg.DBPath)
	}

	reloader := configsync.New(st, reg, logger)
	if err := reloader.LoadNow(startupCtx); err != nil {
		logger.Error("failed to load routes from config store", "error", err)
		os.Exit(1)
	}

	// Control plane: seed a bootstrap admin from env (first run only) and, when an
	// admin JWT secret is configured, serve the Admin API on a separate private
	// listener (never on the public proxy port).
	if cfg.AdminUser != "" && cfg.AdminPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
		if err != nil {
			logger.Error("failed to hash bootstrap admin password", "error", err)
			os.Exit(1)
		}
		if seeded, err := store.SeedAdminUser(startupCtx, st,
			model.AdminUser{Username: cfg.AdminUser, PasswordHash: string(hash), TokenVersion: 1}); err != nil {
			logger.Error("failed to seed bootstrap admin", "error", err)
			os.Exit(1)
		} else if seeded {
			logger.Info("seeded bootstrap admin user", "username", cfg.AdminUser)
		}
	}

	var adminSrv *http.Server
	if cfg.AdminJWTSecret != "" {
		adminSvc := admin.NewService(st, reloader, cfg.AdminJWTSecret, cfg.AdminTokenTTL, logger)
		adminSrv = &http.Server{
			Addr:         cfg.AdminAddr,
			Handler:      adminSvc.Router(),
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			IdleTimeout:  cfg.IdleTimeout,
		}
	} else {
		logger.Warn("admin API disabled: set GATEWAY_ADMIN_JWT_SECRET to enable the control plane")
	}

	srv := server.New(cfg, reg, st, logger)

	// Run the server, shutting down gracefully on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// When enabled, poll the store and hot-reload the data plane on changes.
	if cfg.ConfigPollInterval > 0 {
		go reloader.Run(ctx, cfg.ConfigPollInterval)
		logger.Info("config hot-reload polling enabled", "interval", cfg.ConfigPollInterval)
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("gateway listening", "addr", cfg.ProxyAddr)
		serverErr <- srv.ListenAndServe()
	}()
	if adminSrv != nil {
		go func() {
			logger.Info("admin api listening", "addr", cfg.AdminAddr)
			if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErr <- err
			}
		}()
	}

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
		if adminSrv != nil {
			if err := adminSrv.Shutdown(shutdownCtx); err != nil {
				logger.Error("admin server shutdown failed", "error", err)
			}
		}
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
