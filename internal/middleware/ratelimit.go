package middleware

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/mohabnazmy/API-Gateway/internal/proxy"
	"github.com/mohabnazmy/API-Gateway/internal/ratelimit"
)

// RateLimit applies the matched route's limiter, keyed by the resolved client IP
// (see RealIP). On every rate-limited request it sets standard rate-limit
// headers (RateLimit-Limit/Remaining/Reset, plus X-RateLimit-* for
// compatibility) so clients can see their consumption and when capacity
// returns. Over-limit requests get 429 with a Retry-After header.
func RateLimit(ip *RealIP) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			entry, ok := proxy.EntryFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			limiter := entry.Limiter()
			if limiter == nil {
				next.ServeHTTP(w, r)
				return
			}

			allowed, res := limiter.Allow(ip.From(r))
			setRateLimitHeaders(w, res)
			if !allowed {
				if res.RetryAfter > 0 {
					w.Header().Set("Retry-After", secondsString(res.RetryAfter))
				}
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func setRateLimitHeaders(w http.ResponseWriter, res ratelimit.Result) {
	h := w.Header()
	limit := strconv.Itoa(res.Limit)
	remaining := strconv.Itoa(res.Remaining)
	reset := secondsString(res.Reset)

	// IETF draft-ietf-httpapi-ratelimit-headers field names.
	h.Set("RateLimit-Limit", limit)
	h.Set("RateLimit-Remaining", remaining)
	h.Set("RateLimit-Reset", reset)
	// Widely-used legacy variants for client compatibility.
	h.Set("X-RateLimit-Limit", limit)
	h.Set("X-RateLimit-Remaining", remaining)
	h.Set("X-RateLimit-Reset", reset)
}

// secondsString renders a duration as whole seconds (rounded up, min 0), the
// unit used by Retry-After and the RateLimit-* reset field.
func secondsString(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	return strconv.Itoa(int(math.Ceil(d.Seconds())))
}
