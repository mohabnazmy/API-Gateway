package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mohabnazmy/API-Gateway/internal/config"
	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/registry"
)

const testSecret = "test-secret"

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestGateway wires a gateway in front of a stub upstream and returns a live
// test server plus a cleanup func.
func newTestGateway(t *testing.T, routes []model.Route) (*httptest.Server, func()) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "upstream-ok")
	}))

	// Resolve the literal "BACKEND" placeholder to the stub's URL.
	for i := range routes {
		if routes[i].Upstream == "BACKEND" {
			routes[i].Upstream = backend.URL
		}
	}

	cfg := &config.Config{
		ProxyAddr:   ":0",
		MetricsPath: "/metrics",
		JWTSecret:   testSecret,
		APIKeys:     map[string]struct{}{"valid-key": {}},
		Routes:      routes,
	}
	reg := registry.New(discardLogger())
	if err := reg.Load(cfg.Routes); err != nil {
		t.Fatalf("load routes: %v", err)
	}
	srv := New(cfg, reg, discardLogger())
	ts := httptest.NewServer(srv.Handler)

	return ts, func() {
		ts.Close()
		backend.Close()
	}
}

func get(t *testing.T, url string, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
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

func signedJWT(t *testing.T) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "user42"})
	s, err := tok.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGatewayRoutingAndAuth(t *testing.T) {
	ts, cleanup := newTestGateway(t, []model.Route{
		{Name: "public", PathPrefix: "/public", Upstream: "BACKEND", StripPrefix: true},
		{Name: "secure", PathPrefix: "/secure", Upstream: "BACKEND", StripPrefix: true,
			Auth: model.AuthPolicy{RequireAuth: true}},
		{Name: "dead", PathPrefix: "/dead", Upstream: "http://127.0.0.1:1", StripPrefix: true},
	})
	defer cleanup()

	tests := []struct {
		name    string
		path    string
		headers map[string]string
		want    int
	}{
		{"public route forwards", "/public/x", nil, http.StatusOK},
		{"secure without creds", "/secure/x", nil, http.StatusUnauthorized},
		{"secure with api key", "/secure/x", map[string]string{"X-API-Key": "valid-key"}, http.StatusOK},
		{"secure with bad api key", "/secure/x", map[string]string{"X-API-Key": "nope"}, http.StatusUnauthorized},
		{"secure with jwt", "/secure/x", map[string]string{"Authorization": "Bearer " + signedJWT(t)}, http.StatusOK},
		{"secure with bad jwt", "/secure/x", map[string]string{"Authorization": "Bearer garbage"}, http.StatusUnauthorized},
		{"unmatched path", "/nope", nil, http.StatusNotFound},
		{"dead upstream", "/dead/x", nil, http.StatusBadGateway},
		{"healthz", "/healthz", nil, http.StatusOK},
		{"metrics", "/metrics", nil, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := get(t, ts.URL+tc.path, tc.headers); got != tc.want {
				t.Errorf("%s %s = %d, want %d", http.MethodGet, tc.path, got, tc.want)
			}
		})
	}
}

func TestGatewayRateLimit(t *testing.T) {
	ts, cleanup := newTestGateway(t, []model.Route{
		{Name: "limited", PathPrefix: "/limited", Upstream: "BACKEND", StripPrefix: true,
			RateLimit: model.RateLimitPolicy{Algorithm: "token_bucket", RPS: 1, Burst: 1}},
	})
	defer cleanup()

	if got := get(t, ts.URL+"/limited/x", nil); got != http.StatusOK {
		t.Fatalf("first request = %d, want 200", got)
	}
	if got := get(t, ts.URL+"/limited/x", nil); got != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", got)
	}
}
