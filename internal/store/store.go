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

	// Plans.
	ListPlans(ctx context.Context) ([]model.Plan, error)
	GetPlan(ctx context.Context, id int64) (model.Plan, bool, error)
	UpsertPlan(ctx context.Context, p model.Plan) (int64, error)
	DeletePlan(ctx context.Context, id int64) (bool, error)

	// Consumers.
	ListConsumers(ctx context.Context) ([]model.Consumer, error)
	GetConsumer(ctx context.Context, id int64) (model.Consumer, bool, error)
	UpsertConsumer(ctx context.Context, c model.Consumer) (int64, error)
	DeleteConsumer(ctx context.Context, id int64) (bool, error)

	// API keys (stored hashed; plaintext shown once by the admin API).
	ListConsumerKeys(ctx context.Context, consumerID int64) ([]model.APIKey, error)
	CreateAPIKey(ctx context.Context, consumerID int64, name, keyHash string) (int64, error)
	RevokeAPIKey(ctx context.Context, id int64) (bool, error)
	ResolveAPIKey(ctx context.Context, keyHash string) (model.Identity, bool, error)

	// Admin users (control-plane; writes here do not bump the config version).
	ListAdminUsers(ctx context.Context) ([]model.AdminUser, error)
	GetAdminUser(ctx context.Context, username string) (model.AdminUser, bool, error)
	UpsertAdminUser(ctx context.Context, u model.AdminUser) (int64, error)
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
