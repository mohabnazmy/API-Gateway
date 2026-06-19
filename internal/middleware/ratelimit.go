package middleware

import (
	"net/http"

	"github.com/mohabnazmy/API-Gateway/internal/proxy"
)

// RateLimit applies the matched route's limiter, keyed by the resolved client IP
// (see RealIP). Routes without a limiter (or unmatched requests) pass through;
// over-limit requests get 429.
func RateLimit(ip *RealIP) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if entry, ok := proxy.EntryFromContext(r.Context()); ok {
				if limiter := entry.Limiter(); limiter != nil && !limiter.Allow(ip.From(r)) {
					http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
