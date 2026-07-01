package server

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
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

func gatewayWith(t *testing.T, apiKeys map[string]struct{}, trusted []*net.IPNet, routes []model.Route, backend http.HandlerFunc) (*httptest.Server, func()) {
	t.Helper()
	be := httptest.NewServer(backend)
	for i := range routes {
		if routes[i].Upstream == "BACKEND" {
			routes[i].Upstream = be.URL
		}
	}
	cfg := &config.Config{
		ProxyAddr: ":0", MetricsPath: "/metrics",
		JWTSecret: testSecret, TrustedProxies: trusted, Routes: routes,
	}
	reg := registry.New(discardLogger())
	if err := reg.Load(cfg.Routes); err != nil {
		t.Fatalf("load routes: %v", err)
	}
	srv := New(cfg, reg, keysFromPlaintext(apiKeys), discardLogger())
	ts := httptest.NewServer(srv.Handler)
	return ts, func() { ts.Close(); be.Close() }
}

func getCode(t *testing.T, url string, headers map[string]string) int {
	t.Helper()
	return doGet(t, url, headers).StatusCode
}

func doGet(t *testing.T, url string, headers map[string]string) *http.Response {
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
	return resp
}

// Rate-limited responses carry standard headers so clients can see remaining
// allowance and when capacity returns; 429s include Retry-After.
func TestRateLimitHeaders(t *testing.T) {
	routes := []model.Route{
		{Name: "lim", PathPrefix: "/lim", Upstream: "BACKEND", StripPrefix: true,
			RateLimit: model.RateLimitPolicy{Algorithm: "token_bucket", RPS: 1, Burst: 1}},
	}
	ts, cleanup := gatewayWith(t, nil, nil, routes, okBackend)
	defer cleanup()

	first := doGet(t, ts.URL+"/lim/x", nil)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first request = %d, want 200", first.StatusCode)
	}
	if got := first.Header.Get("RateLimit-Limit"); got != "1" {
		t.Errorf("RateLimit-Limit = %q, want %q (from config)", got, "1")
	}
	if first.Header.Get("RateLimit-Remaining") == "" || first.Header.Get("X-RateLimit-Remaining") == "" {
		t.Error("expected RateLimit-Remaining and X-RateLimit-Remaining on an allowed request")
	}

	second := doGet(t, ts.URL+"/lim/x", nil)
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", second.StatusCode)
	}
	if got := second.Header.Get("Retry-After"); got == "" || got == "0" {
		t.Errorf("429 Retry-After = %q, want a positive seconds value", got)
	}
	if got := second.Header.Get("RateLimit-Remaining"); got != "0" {
		t.Errorf("429 RateLimit-Remaining = %q, want %q", got, "0")
	}
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
	ts, cleanup := gatewayWith(t, map[string]struct{}{"valid-key": {}}, nil, routes, okBackend)
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

		// W4 fixed — the auth scheme is now matched case-insensitively (RFC 6750),
		// so a valid token sent with a lowercase scheme is accepted.
		{"lowercase bearer scheme", "/any", hdr("Authorization", "bearer "+valid), 200, "W4 fixed: case-insensitive scheme"},
		{"uppercase BEARER scheme", "/any", hdr("Authorization", "BEARER "+valid), 200, "W4 fixed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := getCode(t, ts.URL+tc.path, tc.headers); got != tc.want {
				t.Errorf("%s: got %d, want %d (%s)", tc.path, got, tc.want, tc.note)
			}
		})
	}
}

func limitedRoute() []model.Route {
	return []model.Route{
		{Name: "lim", PathPrefix: "/lim", Upstream: "BACKEND", StripPrefix: true,
			RateLimit: model.RateLimitPolicy{Algorithm: "token_bucket", RPS: 1, Burst: 1}},
	}
}

// W1 fixed: with no trusted proxies (the default), X-Forwarded-For is ignored, so
// a client forging a unique XFF per request can no longer evade the per-IP limit
// — every request maps to the same RemoteAddr bucket.
func TestSpoofedXFFNoLongerBypassesRateLimit(t *testing.T) {
	ts, cleanup := gatewayWith(t, nil, nil, limitedRoute(), okBackend)
	defer cleanup()

	bypassed := 0
	for i := 0; i < 10; i++ {
		spoof := map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i)}
		if getCode(t, ts.URL+"/lim/x", spoof) != http.StatusTooManyRequests {
			bypassed++
		}
	}
	// burst=1 → exactly one request (the first) should get through; the rest 429.
	if bypassed != 1 {
		t.Fatalf("spoofed XFF: %d/10 got through, want 1 (XFF must be ignored when untrusted)", bypassed)
	}
}

// With a trusted proxy configured, XFF is honored — so the gateway throttles by
// the forwarded client identity, as intended behind a trusted edge.
func TestTrustedProxyXFFIsHonored(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("127.0.0.0/8") // httptest peer is loopback
	ts, cleanup := gatewayWith(t, nil, []*net.IPNet{trusted}, limitedRoute(), okBackend)
	defer cleanup()

	// Same forwarded client: limited after its burst of 1.
	same := map[string]string{"X-Forwarded-For": "203.0.113.7"}
	if got := getCode(t, ts.URL+"/lim/x", same); got != http.StatusOK {
		t.Fatalf("first request = %d, want 200", got)
	}
	if got := getCode(t, ts.URL+"/lim/x", same); got != http.StatusTooManyRequests {
		t.Fatalf("second same-client request = %d, want 429", got)
	}
	// A different forwarded client gets its own bucket (trusted edge is responsible).
	other := map[string]string{"X-Forwarded-For": "203.0.113.8"}
	if got := getCode(t, ts.URL+"/lim/x", other); got != http.StatusOK {
		t.Fatalf("distinct trusted client = %d, want 200", got)
	}
}
