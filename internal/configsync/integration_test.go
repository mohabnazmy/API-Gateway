package configsync

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/registry"
	"github.com/mohabnazmy/API-Gateway/internal/store"
)

// Exercises the real store + real registry through the reloader, proving routes
// flow from SQLite into the live snapshot and that a store change hot-reloads.
func TestStoreToRegistryHotReload(t *testing.T) {
	ctx := context.Background()

	db, err := store.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mustUpsert(t, db, "users", "/api/users")

	reg := registry.New(quietLogger())
	r := New(db, reg, quietLogger())
	if err := r.LoadNow(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(reg.Current().Routes()); got != 1 {
		t.Fatalf("after LoadNow: registry has %d routes, want 1", got)
	}

	// Change the store, then poll: the live snapshot must pick up the new route.
	mustUpsert(t, db, "orders", "/api/orders")
	reloaded, err := r.pollOnce(ctx)
	if err != nil || !reloaded {
		t.Fatalf("poll after store change: reloaded=%v err=%v", reloaded, err)
	}
	if got := len(reg.Current().Routes()); got != 2 {
		t.Fatalf("after reload: registry has %d routes, want 2", got)
	}
}

func mustUpsert(t *testing.T, db *store.SQLite, name, prefix string) {
	t.Helper()
	if err := db.UpsertRoute(context.Background(), model.Route{
		Name: name, PathPrefix: prefix, Upstream: "http://localhost:9001", StripPrefix: true,
	}); err != nil {
		t.Fatal(err)
	}
}
