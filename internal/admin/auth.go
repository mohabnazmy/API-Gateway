// Package admin is the control-plane HTTP surface: it serves the private Admin
// API on a separate listener and guards it with admin authentication (a
// short-lived, stateless HS256 session JWT, distinct from the data-plane JWT
// that authenticates API clients). See docs/phase-3-admin-api-design.md.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// ErrInvalidCredentials is returned for both unknown users and bad passwords, so
// the API never reveals which usernames exist.
var ErrInvalidCredentials = errors.New("invalid credentials")

var errUnauthorized = errors.New("unauthorized")

// Store is the admin-user lookup the service needs (satisfied by *store.SQLite).
type Store interface {
	GetAdminUser(ctx context.Context, username string) (model.AdminUser, bool, error)
}

// Service authenticates admins and issues/validates session tokens.
type Service struct {
	store    Store
	secret   []byte
	tokenTTL time.Duration
	logger   *slog.Logger
	now      func() time.Time
}

// NewService builds the admin service. secret signs session JWTs (HS256).
func NewService(store Store, secret string, tokenTTL time.Duration, logger *slog.Logger) *Service {
	return &Service{store: store, secret: []byte(secret), tokenTTL: tokenTTL, logger: logger, now: time.Now}
}

// Login verifies a username/password and returns a signed session token.
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	u, ok, err := s.store.GetAdminUser(ctx, username)
	if err != nil {
		return "", err
	}
	if !ok || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return "", ErrInvalidCredentials
	}
	return s.issueToken(u)
}

func (s *Service) issueToken(u model.AdminUser) (string, error) {
	claims := jwt.MapClaims{
		"sub": u.Username,
		"tv":  u.TokenVersion,
		"iat": s.now().Unix(),
		"exp": s.now().Add(s.tokenTTL).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// verify validates a session token's signature, expiry, and token_version, and
// returns the authenticated username.
func (s *Service) verify(ctx context.Context, tokenStr string) (string, error) {
	tok, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errUnauthorized
		}
		return s.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !tok.Valid {
		return "", errUnauthorized
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return "", errUnauthorized
	}
	username, _ := claims["sub"].(string)
	tv, _ := claims["tv"].(float64)
	if username == "" {
		return "", errUnauthorized
	}
	u, ok, err := s.store.GetAdminUser(ctx, username)
	if err != nil {
		return "", err
	}
	if !ok || u.TokenVersion != int64(tv) {
		return "", errUnauthorized
	}
	return username, nil
}

type ctxKey int

const userCtxKey ctxKey = 0

// Middleware rejects requests without a valid admin session token (401) and
// stashes the authenticated username in the request context.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr := bearerToken(r)
		if tokenStr == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		username, err := s.verify(r.Context(), tokenStr)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AdminUserFromContext returns the authenticated admin username, if any.
func AdminUserFromContext(ctx context.Context) (string, bool) {
	u, ok := ctx.Value(userCtxKey).(string)
	return u, ok && u != ""
}

func bearerToken(r *http.Request) string {
	const prefix = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}
