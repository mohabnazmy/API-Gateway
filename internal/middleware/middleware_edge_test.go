package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Recover must convert a downstream panic into a 500 and keep serving.
func TestRecoverConvertsPanicTo500(t *testing.T) {
	h := Recover(discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic should yield 500, got %d", rec.Code)
	}
}

// RequestID generates an ID when absent and preserves a client-supplied one.
func TestRequestIDBehavior(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if seen == "" || rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected a generated request ID in context and response header")
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "client-123")
	h.ServeHTTP(rec2, req)
	if seen != "client-123" || rec2.Header().Get("X-Request-ID") != "client-123" {
		t.Fatalf("expected client-supplied request ID to be honored, got %q", seen)
	}
}

// clientIP documents the trust model: X-Forwarded-For is honored unconditionally
// (the source of the rate-limit spoofing weakness). These cases pin the parsing.
func TestClientIPParsing(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{"remote addr only", "1.2.3.4:5678", "", "1.2.3.4"},
		{"xff single", "1.2.3.4:5678", "9.9.9.9", "9.9.9.9"},
		{"xff first of chain", "1.2.3.4:5678", "9.9.9.9, 10.0.0.1", "9.9.9.9"},
		{"xff trimmed", "1.2.3.4:5678", "  9.9.9.9 , x", "9.9.9.9"},
		{"ipv6 remote", "[::1]:9999", "", "::1"},
		{"malformed remote falls back", "garbage", "", "garbage"},
		{"xff trusted over real ip", "203.0.113.1:443", "evil-spoof", "evil-spoof"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIP(r); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
