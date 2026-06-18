package middleware

import (
	"log/slog"
	"net/http"
)

// Recover converts panics in downstream handlers into 500 responses and logs
// them, keeping the gateway process alive.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"request_id", RequestIDFromContext(r.Context()),
						"method", r.Method,
						"path", r.URL.Path,
						"panic", rec)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
