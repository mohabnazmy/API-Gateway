package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

func openTemp(t *testing.T) *SQLite {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sampleRoute() model.Route {
	return model.Route{
		Name:        "users",
		PathPrefix:  "/api/users",
		Upstream:    "http://localhost:9001",
		StripPrefix: true,
		Methods:     []string{"GET", "POST"},
		Auth:        model.AuthPolicy{RequireAuth: true, Methods: []string{"api_key"}},
		RateLimit:   model.RateLimitPolicy{Algorithm: "token_bucket", RPS: 100, Burst: 200},
		UpstreamAuth: model.UpstreamAuth{
			Type: "bearer", Header: "X-Up", Scheme: "none", TokenRef: "env:UP",
		},
	}
}

func TestEmptyStore(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	routes, err := db.ListRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("fresh store should be empty, got %d routes", len(routes))
	}
	if v, _ := db.Version(ctx); v != 0 {
		t.Fatalf("fresh store version = %d, want 0", v)
	}
}

func TestUpsertRoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	in := sampleRoute()

	if err := db.UpsertRoute(ctx, in); err != nil {
		t.Fatal(err)
	}
	routes, err := db.ListRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}
	if !reflect.DeepEqual(routes[0], in) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", routes[0], in)
	}
	if v, _ := db.Version(ctx); v != 1 {
		t.Fatalf("version after one write = %d, want 1", v)
	}
}

func TestUpsertUpdatesInPlace(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	r := sampleRoute()
	if err := db.UpsertRoute(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Upstream = "http://localhost:9999"
	r.RateLimit.RPS = 5
	r.Methods = nil // clearing a slice must round-trip as nil, not []
	if err := db.UpsertRoute(ctx, r); err != nil {
		t.Fatal(err)
	}

	routes, err := db.ListRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("upsert created a duplicate: %d routes", len(routes))
	}
	if !reflect.DeepEqual(routes[0], r) {
		t.Fatalf("updated round-trip mismatch:\n got %+v\nwant %+v", routes[0], r)
	}
	if v, _ := db.Version(ctx); v != 2 {
		t.Fatalf("version after two writes = %d, want 2", v)
	}
}

func TestDeleteRoute(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := db.UpsertRoute(ctx, sampleRoute()); err != nil {
		t.Fatal(err)
	}

	ok, err := db.DeleteRoute(ctx, "users")
	if err != nil || !ok {
		t.Fatalf("delete existing: ok=%v err=%v", ok, err)
	}
	routes, _ := db.ListRoutes(ctx)
	if len(routes) != 0 {
		t.Fatalf("route not deleted: %d remain", len(routes))
	}

	ok, err = db.DeleteRoute(ctx, "users")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("deleting a missing route should report ok=false")
	}
	if v, _ := db.Version(ctx); v != 2 { // upsert + one successful delete
		t.Fatalf("version = %d, want 2 (no bump for missing delete)", v)
	}
}

func TestListSortedByName(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	for _, n := range []string{"zebra", "alpha", "mike"} {
		r := sampleRoute()
		r.Name, r.PathPrefix = n, "/"+n
		if err := db.UpsertRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	routes, _ := db.ListRoutes(ctx)
	got := []string{routes[0].Name, routes[1].Name, routes[2].Name}
	want := []string{"alpha", "mike", "zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.db")
	ctx := context.Background()

	db1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db1.UpsertRoute(ctx, sampleRoute()); err != nil {
		t.Fatal(err)
	}
	_ = db1.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db2.Close() }()
	routes, _ := db2.ListRoutes(ctx)
	if len(routes) != 1 {
		t.Fatalf("data did not persist: %d routes after reopen", len(routes))
	}
	if v, _ := db2.Version(ctx); v != 1 {
		t.Fatalf("version did not persist: %d", v)
	}
}

func TestSeedRoutesOnlyWhenEmpty(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	seed := []model.Route{sampleRoute()}

	n, err := SeedRoutes(ctx, db, seed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("first seed inserted %d, want 1", n)
	}
	// Second seed must be a no-op (store already has routes).
	n, err = SeedRoutes(ctx, db, seed)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second seed inserted %d, want 0 (no-op)", n)
	}
	if v, _ := db.Version(ctx); v != 1 {
		t.Fatalf("version = %d, want 1 (seed once)", v)
	}
}
