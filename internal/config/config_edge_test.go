package config

import "testing"

func TestLoadRejectsInvalidRoutesJSON(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES", "{ not valid json ]")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for malformed GATEWAY_ROUTES")
	}
}

// API keys now live in the config store (created via the Admin API), so a
// require_auth route no longer needs startup-time credentials configured —
// credential validity is a runtime concern, not a load-time one.
func TestLoadAcceptsRequireAuthWithoutStartupCredentials(t *testing.T) {
	t.Setenv("GATEWAY_JWT_SECRET", "")
	t.Setenv("GATEWAY_ROUTES", `[{"name":"a","path_prefix":"/a","upstream":"http://x:1","auth":{"require_auth":true}}]`)
	if _, err := Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsRelativePathPrefix(t *testing.T) {
	t.Setenv("GATEWAY_ROUTES", `[{"name":"a","path_prefix":"api","upstream":"http://x:1"}]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected an error: path_prefix must start with '/'")
	}
}

// W6 fixed: duplicate route names are rejected (they would collide as metric
// labels / log fields).
func TestLoadRejectsDuplicateRouteNames(t *testing.T) {
	t.Setenv("GATEWAY_JWT_SECRET", "")
	t.Setenv("GATEWAY_ROUTES", `[
		{"name":"dup","path_prefix":"/a","upstream":"http://x:1"},
		{"name":"dup","path_prefix":"/b","upstream":"http://x:1"}
	]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for duplicate route names")
	}
}

// W7 fixed: two routes with the same path_prefix and overlapping methods are
// rejected, since the second would be silently unreachable.
func TestLoadRejectsShadowingPathPrefix(t *testing.T) {
	t.Setenv("GATEWAY_JWT_SECRET", "")
	t.Setenv("GATEWAY_ROUTES", `[
		{"name":"first","path_prefix":"/same","upstream":"http://x:1"},
		{"name":"second","path_prefix":"/same","upstream":"http://y:1"}
	]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a shadowing duplicate path_prefix")
	}
}

// Same prefix but disjoint methods is legitimate (e.g. GET vs POST to different
// upstreams) and must be accepted.
func TestLoadAcceptsSamePrefixDisjointMethods(t *testing.T) {
	t.Setenv("GATEWAY_JWT_SECRET", "")
	t.Setenv("GATEWAY_ROUTES", `[
		{"name":"reads","path_prefix":"/same","upstream":"http://x:1","methods":["GET"]},
		{"name":"writes","path_prefix":"/same","upstream":"http://y:1","methods":["POST"]}
	]`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("got %d routes, want 2", len(cfg.Routes))
	}
}
