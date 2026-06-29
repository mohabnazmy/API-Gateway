// Package configsync bridges the config store to the live registry: it loads
// routes at startup and, when polling is enabled, reloads them whenever the
// store's config version changes — without a restart. It depends on the store
// and registry only through narrow interfaces, so neither imports the other.
package configsync

import (
	"context"
	"log/slog"
	"time"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// Source is the config store as this package needs it.
type Source interface {
	ListRoutes(ctx context.Context) ([]model.Route, error)
	Version(ctx context.Context) (int64, error)
}

// Target is the registry as this package needs it: an atomic snapshot swap.
type Target interface {
	Load(routes []model.Route) error
}

// Reloader keeps Target in sync with Source. lastVersion is only touched by
// LoadNow (at startup, before Run) and by Run's single goroutine, so it needs no
// lock.
type Reloader struct {
	src    Source
	target Target
	logger *slog.Logger

	lastVersion int64
}

// New returns a Reloader from src into target.
func New(src Source, target Target, logger *slog.Logger) *Reloader {
	return &Reloader{src: src, target: target, logger: logger}
}

// LoadNow loads the current config into the target once and records its version.
// Call it at startup before Run.
func (r *Reloader) LoadNow(ctx context.Context) error {
	v, err := r.src.Version(ctx)
	if err != nil {
		return err
	}
	rts, err := r.src.ListRoutes(ctx)
	if err != nil {
		return err
	}
	if err := r.target.Load(rts); err != nil {
		return err
	}
	r.lastVersion = v
	return nil
}

// pollOnce reloads the target if the store's version advanced. It reports whether
// a reload happened. On a load failure lastVersion is left unchanged, so the next
// poll retries (no silent half-apply).
func (r *Reloader) pollOnce(ctx context.Context) (bool, error) {
	v, err := r.src.Version(ctx)
	if err != nil {
		return false, err
	}
	if v == r.lastVersion {
		return false, nil
	}
	rts, err := r.src.ListRoutes(ctx)
	if err != nil {
		return false, err
	}
	if err := r.target.Load(rts); err != nil {
		return false, err
	}
	r.lastVersion = v
	return true, nil
}

// Run polls every interval until ctx is cancelled, reloading on version changes.
// A reload failure is logged and retried on the next tick.
func (r *Reloader) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			switch reloaded, err := r.pollOnce(ctx); {
			case err != nil:
				r.logger.Error("config reload failed", "error", err)
			case reloaded:
				r.logger.Info("config reloaded", "version", r.lastVersion)
			}
		}
	}
}
