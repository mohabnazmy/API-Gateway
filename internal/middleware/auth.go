package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/proxy"
	"github.com/mohabnazmy/API-Gateway/internal/store"
)

// KeyResolver maps an API-key hash to the consumer it authenticates (satisfied by
// *store.SQLite). API keys live in the config store, owned by a consumer.
type KeyResolver interface {
	ResolveAPIKey(ctx context.Context, keyHash string) (model.Identity, bool, error)
}

// Authenticator validates requests for routes that set RequireAuth. It accepts
// either a Bearer JWT (validated against JWTSecret) or an API key (X-API-Key)
// resolved against the store to a consumer.
type Authenticator struct {
	jwtSecret []byte
	keys      KeyResolver
}

// NewAuthenticator builds an Authenticator. Either credential source may be
// empty; routes requiring auth simply won't accept that credential type.
func NewAuthenticator(jwtSecret string, keys KeyResolver) *Authenticator {
	return &Authenticator{
		jwtSecret: []byte(jwtSecret),
		keys:      keys,
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
		// An API key resolves a consumer; attribute the request to it so rate
		// limiting can key on the consumer's plan.
		if policy.AcceptsAPIKey() {
			if id, ok := a.resolveAPIKey(r); ok {
				next.ServeHTTP(w, r.WithContext(WithConsumer(r.Context(), id)))
				return
			}
		}
		if policy.AcceptsJWT() && a.validJWT(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer, X-API-Key`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func (a *Authenticator) resolveAPIKey(r *http.Request) (model.Identity, bool) {
	if a.keys == nil {
		return model.Identity{}, false
	}
	key := r.Header.Get("X-API-Key")
	if key == "" {
		return model.Identity{}, false
	}
	id, ok, err := a.keys.ResolveAPIKey(r.Context(), store.HashAPIKey(key))
	if err != nil || !ok {
		return model.Identity{}, false
	}
	return id, true
}

func (a *Authenticator) validJWT(r *http.Request) bool {
	if len(a.jwtSecret) == 0 {
		return false
	}
	auth := r.Header.Get("Authorization")
	// RFC 6750: the auth scheme is case-insensitive ("Bearer" == "bearer").
	const prefix = "bearer "
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
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
