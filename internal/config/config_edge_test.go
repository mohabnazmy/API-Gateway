package config

import "testing"

func TestLoadRejectsInvalidRoutesJSON(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES", "{ not valid json ]")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for malformed GATEWAY_ROUTES")
	}
}

func TestLoadRejectsRequireAuthWithoutCredentials(t *testing.T) {
	t.Setenv("GATEWAY_JWT_SECRET", "")
	t.Setenv("GATEWAY_API_KEYS", "")
	t.Setenv("GATEWAY_ROUTES", `[{"name":"a","path_prefix":"/a","upstream":"http://x:1","auth":{"require_auth":true}}]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected an error: require_auth set but no credentials configured")
	}
}

func TestLoadRejectsRelativePathPrefix(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES", `[{"name":"a","path_prefix":"api","upstream":"http://x:1"}]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected an error: path_prefix must start with '/'")
	}
}

// WEAKNESS (characterization): duplicate route names are accepted. They are used
// as Prometheus metric labels and in logs, so two routes named the same collide
// in metrics. config does not enforce name uniqueness. See docs/test-findings.md.
func TestLoadAcceptsDuplicateRouteNames(t *testing.T) {
	t.Setenv("GATEWAY_JWT_SECRET", "")
	t.Setenv("GATEWAY_API_KEYS", "")
	t.Setenv("GATEWAY_ROUTES", `[
		{"name":"dup","path_prefix":"/a","upstream":"http://x:1"},
		{"name":"dup","path_prefix":"/b","upstream":"http://x:1"}
	]`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("got %d routes, want 2", len(cfg.Routes))
	}
}

// WEAKNESS (characterization): two routes may declare the *same* path_prefix.
// config accepts it; at match time a stable sort makes the first-listed win, so
// the second is silently unreachable (for the same method set). No warning.
func TestLoadAcceptsDuplicatePathPrefix(t *testing.T) {
	t.Setenv("GATEWAY_JWT_SECRET", "")
	t.Setenv("GATEWAY_API_KEYS", "")
	t.Setenv("GATEWAY_ROUTES", `[
		{"name":"first","path_prefix":"/same","upstream":"http://x:1"},
		{"name":"second","path_prefix":"/same","upstream":"http://y:1"}
	]`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("got %d routes, want 2 (the second is silently shadowed at match time)", len(cfg.Routes))
	}
}
