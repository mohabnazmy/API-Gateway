package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/store"
)

type fakeReloader struct{ calls int }

func (f *fakeReloader) LoadNow(context.Context) error { f.calls++; return nil }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestService builds a service over a real temp SQLite store with a seeded
// "root"/"secret" admin, plus a reloader spy.
func newTestService(t *testing.T, ttl time.Duration) (*Service, *store.SQLite, *fakeReloader) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeedAdminUser(context.Background(), db,
		model.AdminUser{Username: "root", PasswordHash: string(hash), TokenVersion: 1}); err != nil {
		t.Fatal(err)
	}
	rl := &fakeReloader{}
	return NewService(db, rl, "test-secret", ttl, quietLogger()), db, rl
}

// login returns a session token for root/secret.
func login(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/admin/api/auth/login", strings.NewReader(`{"username":"root","password":"secret"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d", rec.Code)
	}
	var lr struct{ Token string }
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	if lr.Token == "" {
		t.Fatal("no token")
	}
	return lr.Token
}

func TestLoginSuccessAndVerify(t *testing.T) {
	s, _, _ := newTestService(t, 30*time.Minute)
	tok, err := s.Login(context.Background(), "root", "secret")
	if err != nil || tok == "" {
		t.Fatalf("login: tok=%q err=%v", tok, err)
	}
	user, err := s.verify(context.Background(), tok)
	if err != nil || user != "root" {
		t.Fatalf("verify: user=%q err=%v", user, err)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	s, _, _ := newTestService(t, 30*time.Minute)
	if _, err := s.Login(context.Background(), "root", "wrong"); err != ErrInvalidCredentials {
		t.Fatalf("wrong password err = %v", err)
	}
	if _, err := s.Login(context.Background(), "ghost", "secret"); err != ErrInvalidCredentials {
		t.Fatalf("unknown user err = %v", err)
	}
}

func TestVerifyRejectsExpiredAndTampered(t *testing.T) {
	s, _, _ := newTestService(t, -time.Minute)
	tok, _ := s.Login(context.Background(), "root", "secret")
	if _, err := s.verify(context.Background(), tok); err == nil {
		t.Fatal("expired token accepted")
	}
	s2, _, _ := newTestService(t, 30*time.Minute)
	good, _ := s2.Login(context.Background(), "root", "secret")
	if _, err := s2.verify(context.Background(), good+"x"); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestVerifyRejectsStaleTokenVersion(t *testing.T) {
	s, db, _ := newTestService(t, 30*time.Minute)
	tok, _ := s.Login(context.Background(), "root", "secret")
	u, _, _ := db.GetAdminUser(context.Background(), "root")
	u.TokenVersion = 2
	if _, err := db.UpsertAdminUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if _, err := s.verify(context.Background(), tok); err == nil {
		t.Fatal("stale token_version accepted")
	}
}

func TestRouterAuthGuards(t *testing.T) {
	s, _, _ := newTestService(t, 30*time.Minute)
	router := s.Router()

	if code := do(router, "GET", "/admin/api/health", "", ""); code != http.StatusOK {
		t.Fatalf("health = %d, want 200", code)
	}
	if code := do(router, "GET", "/admin/api/me", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("me without token = %d, want 401", code)
	}
	tok := login(t, router)
	if code := do(router, "GET", "/admin/api/me", "", tok); code != http.StatusOK {
		t.Fatalf("me with token = %d, want 200", code)
	}
	if code := do(router, "POST", "/admin/api/auth/login", `{"username":"root","password":"nope"}`, ""); code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", code)
	}
}

func do(h http.Handler, method, path, body, token string) int {
	return doRec(h, method, path, body, token).Code
}

func doRec(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(rec, r)
	return rec
}
