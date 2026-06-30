package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/config"
	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/registry"
	"github.com/mohabnazmy/API-Gateway/internal/store"
)

// planKeys resolves two keys to two consumers, both on a tiny plan (rps 1, burst 1).
type planKeys struct{}

func (planKeys) ResolveAPIKey(_ context.Context, hash string) (model.Identity, bool, error) {
	limit := model.RateLimitPolicy{Algorithm: "token_bucket", RPS: 1, Burst: 1}
	switch hash {
	case store.HashAPIKey("key-a"):
		return model.Identity{ConsumerID: 1, PlanID: 1, Limit: limit}, true, nil
	case store.HashAPIKey("key-b"):
		return model.Identity{ConsumerID: 2, PlanID: 1, Limit: limit}, true, nil
	}
	return model.Identity{}, false, nil
}

// A route with NO route-level limit still throttles per consumer by their plan,
// and consumers get independent buckets.
func TestConsumerKeyedRateLimit(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	routes := []model.Route{{
		Name: "api", PathPrefix: "/api", Upstream: backend.URL, StripPrefix: true,
		Auth: model.AuthPolicy{RequireAuth: true, Methods: []string{"api_key"}},
	}}
	cfg := &config.Config{ProxyAddr: ":0", MetricsPath: "/metrics", Routes: routes}
	reg := registry.New(discardLogger())
	if err := reg.Load(routes); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(cfg, reg, planKeys{}, discardLogger()).Handler)
	defer ts.Close()

	a := map[string]string{"X-API-Key": "key-a"}
	b := map[string]string{"X-API-Key": "key-b"}

	if got := get(t, ts.URL+"/api/x", a); got != http.StatusOK {
		t.Fatalf("consumer A first = %d, want 200", got)
	}
	if got := get(t, ts.URL+"/api/x", a); got != http.StatusTooManyRequests {
		t.Fatalf("consumer A second = %d, want 429 (plan burst=1)", got)
	}
	// Consumer B has its own bucket — unaffected by A being throttled.
	if got := get(t, ts.URL+"/api/x", b); got != http.StatusOK {
		t.Fatalf("consumer B = %d, want 200 (separate bucket)", got)
	}
	// A wrong key is unauthorized.
	if got := get(t, ts.URL+"/api/x", map[string]string{"X-API-Key": "nope"}); got != http.StatusUnauthorized {
		t.Fatalf("bad key = %d, want 401", got)
	}
}
