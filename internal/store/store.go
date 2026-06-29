// Package store is the durable config source of truth (embedded SQLite). All DB
// access goes through the Store interface so the data plane, registry, and (later)
// admin API depend on the interface, not on SQLite — keeping a future Postgres/etcd
// swap a contained change. See docs/phase-2-store-design.md.
package store

import (
	"context"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// Store persists routes (and, in later phases, consumers/plans/keys). Every write
// bumps a monotonic config version in the same transaction, so a reader never sees
// a version newer than the data it would read.
type Store interface {
	// ListRoutes returns all enabled routes, ordered by name.
	ListRoutes(ctx context.Context) ([]model.Route, error)
	// UpsertRoute inserts a route or updates the existing one with the same name.
	UpsertRoute(ctx context.Context, r model.Route) error
	// DeleteRoute removes a route by name; the bool reports whether one existed.
	DeleteRoute(ctx context.Context, name string) (bool, error)
	// Version is the current config version (0 on a fresh store, +1 per write).
	Version(ctx context.Context) (int64, error)
	// Close releases the underlying database.
	Close() error
}

// SeedRoutes imports the given routes only when the store is empty, so a fresh
// deployment can bootstrap from GATEWAY_ROUTES while the DB remains authoritative
// thereafter. It returns the number of routes inserted (0 when already populated).
func SeedRoutes(ctx context.Context, s Store, routes []model.Route) (int, error) {
	existing, err := s.ListRoutes(ctx)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, nil
	}
	for _, r := range routes {
		if err := s.UpsertRoute(ctx, r); err != nil {
			return 0, err
		}
	}
	return len(routes), nil
}
