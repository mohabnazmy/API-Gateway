package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

type fakeStore struct{ users map[string]model.AdminUser }

func (f *fakeStore) GetAdminUser(_ context.Context, username string) (model.AdminUser, bool, error) {
	u, ok := f.users[username]
	return u, ok, nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestService(t *testing.T, ttl time.Duration) (*Service, *fakeStore) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeStore{users: map[string]model.AdminUser{
		"root": {ID: 1, Username: "root", PasswordHash: string(hash), TokenVersion: 1},
	}}
	return NewService(fs, "test-secret", ttl, quietLogger()), fs
}

func TestLoginSuccessAndVerify(t *testing.T) {
	s, _ := newTestService(t, 30*time.Minute)
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
	s, _ := newTestService(t, 30*time.Minute)
	if _, err := s.Login(context.Background(), "root", "wrong"); err != ErrInvalidCredentials {
		t.Fatalf("wrong password err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := s.Login(context.Background(), "ghost", "secret"); err != ErrInvalidCredentials {
		t.Fatalf("unknown user err = %v, want ErrInvalidCredentials", err)
	}
}

func TestVerifyRejectsExpiredAndTampered(t *testing.T) {
	s, _ := newTestService(t, -time.Minute) // issues an already-expired token
	tok, err := s.Login(context.Background(), "root", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.verify(context.Background(), tok); err == nil {
		t.Fatal("expired token accepted")
	}

	s2, _ := newTestService(t, 30*time.Minute)
	good, _ := s2.Login(context.Background(), "root", "secret")
	if _, err := s2.verify(context.Background(), good+"x"); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestVerifyRejectsStaleTokenVersion(t *testing.T) {
	s, fs := newTestService(t, 30*time.Minute)
	tok, _ := s.Login(context.Background(), "root", "secret")
	// Bump token_version (e.g. password change / forced logout) → old token invalid.
	u := fs.users["root"]
	u.TokenVersion = 2
	fs.users["root"] = u
	if _, err := s.verify(context.Background(), tok); err == nil {
		t.Fatal("stale token_version accepted")
	}
}

func TestRouterAuthFlow(t *testing.T) {
	s, _ := newTestService(t, 30*time.Minute)
	router := s.Router()

	// health is public
	if code := do(router, "GET", "/admin/api/health", "", ""); code != http.StatusOK {
		t.Fatalf("health = %d, want 200", code)
	}
	// protected route without a token → 401
	if code := do(router, "GET", "/admin/api/me", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("me without token = %d, want 401", code)
	}

	// login → token
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/admin/api/auth/login", strings.NewReader(`{"username":"root","password":"secret"}`))
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200", rec.Code)
	}
	var lr struct{ Token string }
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	if lr.Token == "" {
		t.Fatal("login returned no token")
	}

	// protected route with token → 200 and the username
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/admin/api/me", nil)
	req2.Header.Set("Authorization", "Bearer "+lr.Token)
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("me with token = %d, want 200", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), `"root"`) {
		t.Fatalf("me body missing username: %s", rec2.Body.String())
	}

	// wrong-password login → 401
	if code := do(router, "POST", "/admin/api/auth/login", `{"username":"root","password":"nope"}`, ""); code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", code)
	}
}

func do(h http.Handler, method, path, body, token string) int {
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
	return rec.Code
}
