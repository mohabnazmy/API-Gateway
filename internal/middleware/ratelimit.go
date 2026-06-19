package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/mohabnazmy/API-Gateway/internal/proxy"
)

// RateLimit applies the matched route's limiter, keyed by client IP. Routes
// without a limiter (or unmatched requests) pass through; over-limit requests
// get 429. The limiter and its algorithm come from the route's policy, compiled
// into the snapshot entry.
func RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if entry, ok := proxy.EntryFromContext(r.Context()); ok {
			if limiter := entry.Limiter(); limiter != nil && !limiter.Allow(clientIP(r)) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the originating client IP, honoring X-Forwarded-For when
// present (the gateway is expected to sit behind a trusted edge or be the edge).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
