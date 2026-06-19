package middleware

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

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

// W1 fixed: RealIP honors X-Forwarded-For only when the peer is a trusted proxy;
// otherwise it uses RemoteAddr and ignores XFF (so spoofing can't change identity).
func TestRealIPTrustModel(t *testing.T) {
	trustLocal := []*net.IPNet{mustCIDR(t, "1.2.3.0/24")}

	cases := []struct {
		name       string
		trusted    []*net.IPNet
		remoteAddr string
		xff        string
		want       string
	}{
		{"untrusted: xff ignored", nil, "1.2.3.4:5678", "9.9.9.9", "1.2.3.4"},
		{"untrusted: spoof ignored", nil, "203.0.113.1:443", "evil-spoof", "203.0.113.1"},
		{"untrusted: no xff", nil, "1.2.3.4:5678", "", "1.2.3.4"},
		{"trusted peer: xff honored", trustLocal, "1.2.3.4:5678", "9.9.9.9", "9.9.9.9"},
		{"trusted peer: leftmost of chain", trustLocal, "1.2.3.4:5678", "9.9.9.9, 10.0.0.1", "9.9.9.9"},
		{"trusted peer: no xff falls to remote", trustLocal, "1.2.3.4:5678", "", "1.2.3.4"},
		{"peer not in trusted set: xff ignored", trustLocal, "8.8.8.8:5678", "9.9.9.9", "8.8.8.8"},
		{"ipv6 remote, untrusted", nil, "[::1]:9999", "", "::1"},
		{"malformed remote falls back", nil, "garbage", "", "garbage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ri := NewRealIP(tc.trusted)
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := ri.From(r); got != tc.want {
				t.Errorf("From() = %q, want %q", got, tc.want)
			}
		})
	}
}
