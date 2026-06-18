// Package middleware provides the gateway's HTTP middleware chain:
// panic recovery, request IDs, logging, metrics, auth and rate limiting.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type ctxKey int

const requestIDCtxKey ctxKey = iota

// responseRecorder captures the status code and response size for logging and
// metrics while transparently delegating writes to the underlying writer.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if rr.status == 0 {
		rr.status = http.StatusOK
	}
	n, err := rr.ResponseWriter.Write(b)
	rr.bytes += n
	return n, err
}

// Flush implements http.Flusher so streaming/SSE upstreams keep working.
func (rr *responseRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func newRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{ResponseWriter: w, status: 0}
}

// RequestID assigns a unique ID to each request, exposing it via the
// X-Request-ID response header and the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDCtxKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the request ID set by RequestID, if any.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDCtxKey).(string); ok {
		return id
	}
	return ""
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}
