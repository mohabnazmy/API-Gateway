// Package server wires the registry, middleware and proxy into the public
// data-plane HTTP server with graceful shutdown. The admin (control-plane)
// server is added in a later phase.
package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/mohabnazmy/API-Gateway/internal/config"
	"github.com/mohabnazmy/API-Gateway/internal/middleware"
	"github.com/mohabnazmy/API-Gateway/internal/proxy"
	"github.com/mohabnazmy/API-Gateway/internal/registry"
)

// New builds the public data-plane HTTP server. It reads all route config from
// the registry, so a future hot-reload swaps config without rebuilding the
// server. The returned server is ready to ListenAndServe.
func New(cfg *config.Config, reg *registry.Registry, logger *slog.Logger) *http.Server {
	promReg := prometheus.NewRegistry()
	promReg.MustRegister(prometheus.NewGoCollector())
	metrics := middleware.NewMetrics(promReg)
	auth := middleware.NewAuthenticator(cfg.JWTSecret, cfg.APIKeys)
	realIP := middleware.NewRealIP(cfg.TrustedProxies)

	r := chi.NewRouter()

	// Operational endpoints, exempt from proxying, auth and rate limiting.
	r.Get("/healthz", healthHandler)
	r.Handle(cfg.MetricsPath, promhttp.HandlerFor(promReg, promhttp.HandlerOpts{}))

	// Everything else flows through the gateway middleware chain. Order is
	// significant: Resolve runs first so logging, metrics, rate limiting and auth
	// can all see the matched route; Dispatch emits the final response (or 404).
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequestID)
		r.Use(middleware.Recover(logger))
		r.Use(proxy.Resolve(reg))
		r.Use(middleware.Logging(logger, realIP))
		r.Use(metrics.Middleware)
		r.Use(middleware.RateLimit(realIP))
		r.Use(auth.Middleware)
		r.Handle("/*", http.HandlerFunc(proxy.Dispatch))
	})

	return &http.Server{
		Addr:         cfg.ProxyAddr,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
