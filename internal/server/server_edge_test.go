package server

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mohabnazmy/API-Gateway/internal/config"
	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/registry"
)

func okBackend(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func gatewayWith(t *testing.T, apiKeys map[string]struct{}, routes []model.Route, backend http.HandlerFunc) (*httptest.Server, func()) {
	t.Helper()
	be := httptest.NewServer(backend)
	for i := range routes {
		if routes[i].Upstream == "BACKEND" {
			routes[i].Upstream = be.URL
		}
	}
	cfg := &config.Config{
		ProxyAddr: ":0", MetricsPath: "/metrics",
		JWTSecret: testSecret, APIKeys: apiKeys, Routes: routes,
	}
	reg := registry.New(discardLogger())
	if err := reg.Load(cfg.Routes); err != nil {
		t.Fatalf("load routes: %v", err)
	}
	srv := New(cfg, reg, discardLogger())
	ts := httptest.NewServer(srv.Handler)
	return ts, func() { ts.Close(); be.Close() }
}

func getCode(t *testing.T, url string, headers map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func hsToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func noneToken(t *testing.T) string {
	t.Helper()
	s, err := jwt.New(jwt.SigningMethodNone).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func rs256Token(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"sub": "x"}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAuthAttacksAndEnforcement probes the auth middleware with forged/edge
// credentials. Most assert *strength* (attacks rejected); two flag documented
// weaknesses (see docs/test-findings.md).
func TestAuthAttacksAndEnforcement(t *testing.T) {
	routes := []model.Route{
		{Name: "any", PathPrefix: "/any", Upstream: "BACKEND", StripPrefix: true,
			Auth: model.AuthPolicy{RequireAuth: true}},
		{Name: "jwtonly", PathPrefix: "/jwtonly", Upstream: "BACKEND", StripPrefix: true,
			Auth: model.AuthPolicy{RequireAuth: true, Methods: []string{"jwt"}}},
		{Name: "keyonly", PathPrefix: "/keyonly", Upstream: "BACKEND", StripPrefix: true,
			Auth: model.AuthPolicy{RequireAuth: true, Methods: []string{"api_key"}}},
	}
	ts, cleanup := gatewayWith(t, map[string]struct{}{"valid-key": {}}, routes, okBackend)
	defer cleanup()

	valid := hsToken(t, testSecret, jwt.MapClaims{"sub": "x"})
	expired := hsToken(t, testSecret, jwt.MapClaims{"sub": "x", "exp": time.Now().Add(-time.Hour).Unix()})
	wrongSecret := hsToken(t, "not-the-secret", jwt.MapClaims{"sub": "x"})

	hdr := func(k, v string) map[string]string { return map[string]string{k: v} }

	tests := []struct {
		name    string
		path    string
		headers map[string]string
		want    int
		note    string
	}{
		// Strengths — attacks must be rejected.
		{"no credentials", "/any", nil, 401, "strength"},
		{"valid jwt", "/any", hdr("Authorization", "Bearer "+valid), 200, "strength"},
		{"expired jwt", "/any", hdr("Authorization", "Bearer "+expired), 401, "strength"},
		{"wrong-secret jwt", "/any", hdr("Authorization", "Bearer "+wrongSecret), 401, "strength"},
		{"alg=none jwt", "/any", hdr("Authorization", "Bearer "+noneToken(t)), 401, "strength: alg allow-listing"},
		{"alg-confusion RS256", "/any", hdr("Authorization", "Bearer "+rs256Token(t)), 401, "strength: RS->HS rejected"},
		{"garbage bearer", "/any", hdr("Authorization", "Bearer not.a.jwt"), 401, "strength"},
		{"empty api key", "/any", hdr("X-API-Key", ""), 401, "strength"},
		{"wrong api key", "/any", hdr("X-API-Key", "nope"), 401, "strength"},
		{"valid api key", "/any", hdr("X-API-Key", "valid-key"), 200, "strength"},

		// Per-route credential-type enforcement (strength).
		{"jwtonly rejects api key", "/jwtonly", hdr("X-API-Key", "valid-key"), 401, "strength: method scoping"},
		{"jwtonly accepts jwt", "/jwtonly", hdr("Authorization", "Bearer "+valid), 200, "strength"},
		{"keyonly rejects jwt", "/keyonly", hdr("Authorization", "Bearer "+valid), 401, "strength: method scoping"},
		{"keyonly accepts api key", "/keyonly", hdr("X-API-Key", "valid-key"), 200, "strength"},

		// WEAKNESS — the "Bearer" scheme is matched case-sensitively, but RFC 6750
		// says the auth scheme is case-insensitive. A valid token with a
		// lowercase scheme is wrongly rejected.
		{"lowercase bearer scheme", "/any", hdr("Authorization", "bearer "+valid), 401, "WEAKNESS: case-sensitive scheme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := getCode(t, ts.URL+tc.path, tc.headers); got != tc.want {
				t.Errorf("%s: got %d, want %d (%s)", tc.path, got, tc.want, tc.note)
			}
		})
	}
}

// WEAKNESS: rate limiting keys on the client IP, which is taken from
// X-Forwarded-For when present. A caller that forges a unique XFF per request
// gets a fresh bucket each time, completely bypassing the limit. Documented in
// docs/test-findings.md.
func TestRateLimitBypassedBySpoofedXFF(t *testing.T) {
	routes := []model.Route{
		{Name: "lim", PathPrefix: "/lim", Upstream: "BACKEND", StripPrefix: true,
			RateLimit: model.RateLimitPolicy{Algorithm: "token_bucket", RPS: 1, Burst: 1}},
	}
	ts, cleanup := gatewayWith(t, nil, routes, okBackend)
	defer cleanup()

	// Control: a single fixed client is limited after its burst.
	fixed := map[string]string{"X-Forwarded-For": "203.0.113.7"}
	if got := getCode(t, ts.URL+"/lim/x", fixed); got != http.StatusOK {
		t.Fatalf("first fixed-IP request = %d, want 200", got)
	}
	limited := 0
	for i := 0; i < 4; i++ {
		if getCode(t, ts.URL+"/lim/x", fixed) == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("expected the fixed client to be rate-limited after its burst")
	}

	// Attack: a unique spoofed XFF per request bypasses the limit entirely.
	bypassed := 0
	for i := 0; i < 10; i++ {
		spoof := map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i)}
		if getCode(t, ts.URL+"/lim/x", spoof) != http.StatusTooManyRequests {
			bypassed++
		}
	}
	if bypassed != 10 {
		t.Fatalf("XFF spoofing: %d/10 got through, want 10 (documented weakness)", bypassed)
	}
}
