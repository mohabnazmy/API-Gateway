// Package registry holds the live data-plane configuration as an atomically
// swappable snapshot. The data plane reads Current() lock-free on every request;
// Load builds a new snapshot and swaps it in (the basis for hot-reload).
package registry

import (
	"log/slog"
	"sync/atomic"

	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/proxy"
)

// Registry owns the current configuration snapshot.
type Registry struct {
	current atomic.Pointer[proxy.Snapshot]
	logger  *slog.Logger
	opts    proxy.Options
}

// New returns a Registry seeded with an empty snapshot, so Current() is always
// safe to call before the first Load. Optional proxy Options (e.g. the upstream
// transport) apply to every snapshot it builds.
func New(logger *slog.Logger, opts ...proxy.Options) *Registry {
	r := &Registry{logger: logger}
	if len(opts) > 0 {
		r.opts = opts[0]
	}
	empty, _ := proxy.NewSnapshot(nil, logger, r.opts)
	r.current.Store(empty)
	return r
}

// Current returns the active snapshot. Safe for concurrent, lock-free reads.
func (r *Registry) Current() *proxy.Snapshot {
	return r.current.Load()
}

// Load compiles routes into a new snapshot and atomically swaps it in. If
// compilation fails, the active snapshot is left untouched (N5: no half-apply).
// The previous snapshot's limiters are stopped after the swap.
func (r *Registry) Load(routes []model.Route) error {
	next, err := proxy.NewSnapshot(routes, r.logger, r.opts)
	if err != nil {
		return err
	}
	prev := r.current.Swap(next)
	if prev != nil {
		prev.Close()
	}
	r.logRoutes(routes)
	return nil
}

func (r *Registry) logRoutes(routes []model.Route) {
	if len(routes) == 0 {
		r.logger.Warn("no routes configured; all proxied requests will 404")
		return
	}
	for _, rt := range routes {
		r.logger.Info("route registered",
			"name", rt.Name,
			"path_prefix", rt.PathPrefix,
			"upstream", rt.Upstream,
			"strip_prefix", rt.StripPrefix,
			"require_auth", rt.Auth.RequireAuth,
			"rate_limited", rt.RateLimit.Enabled(),
		)
	}
}
