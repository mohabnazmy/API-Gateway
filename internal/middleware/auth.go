package middleware

import (
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mohabnazmy/API-Gateway/internal/proxy"
)

// Authenticator validates requests for routes that set RequireAuth. It accepts
// either a Bearer JWT (validated against JWTSecret) or an API key supplied via
// the X-API-Key header.
type Authenticator struct {
	jwtSecret []byte
	apiKeys   map[string]struct{}
}

// NewAuthenticator builds an Authenticator. Either credential source may be
// empty; routes requiring auth simply won't accept that credential type.
func NewAuthenticator(jwtSecret string, apiKeys map[string]struct{}) *Authenticator {
	return &Authenticator{
		jwtSecret: []byte(jwtSecret),
		apiKeys:   apiKeys,
	}
}

// Middleware enforces authentication on routes whose policy requires it,
// honoring the route's accepted credential types. Public routes pass through.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry, ok := proxy.EntryFromContext(r.Context())
		if !ok || !entry.Route().Auth.RequireAuth {
			next.ServeHTTP(w, r)
			return
		}
		policy := entry.Route().Auth
		if (policy.AcceptsAPIKey() && a.validAPIKey(r)) ||
			(policy.AcceptsJWT() && a.validJWT(r)) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer, X-API-Key`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func (a *Authenticator) validAPIKey(r *http.Request) bool {
	if len(a.apiKeys) == 0 {
		return false
	}
	key := r.Header.Get("X-API-Key")
	if key == "" {
		return false
	}
	_, ok := a.apiKeys[key]
	return ok
}

func (a *Authenticator) validJWT(r *http.Request) bool {
	if len(a.jwtSecret) == 0 {
		return false
	}
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	tokenStr := strings.TrimSpace(auth[len(prefix):])
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return a.jwtSecret, nil
	}, jwt.WithValidMethods([]string{"HS256", "HS384", "HS512"}))
	return err == nil && token.Valid
}
