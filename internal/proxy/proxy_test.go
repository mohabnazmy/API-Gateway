package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type staticSource struct{ s *Snapshot }

func (ss staticSource) Current() *Snapshot { return ss.s }

func TestPathMatches(t *testing.T) {
	cases := []struct {
		path, prefix string
		want         bool
	}{
		{"/api", "/api", true},
		{"/api/x", "/api", true},
		{"/apiv2", "/api", false}, // segment boundary
		{"/api/", "/api/", true},  // prefix with trailing slash
		{"/", "/api", false},      // shorter than prefix
		{"/api/users/42", "/api/users", true},
		{"/apiusers", "/api/users", false},
	}
	for _, c := range cases {
		if got := pathMatches(c.path, c.prefix); got != c.want {
			t.Errorf("pathMatches(%q, %q) = %v, want %v", c.path, c.prefix, got, c.want)
		}
	}
}

func TestSnapshotMatchLongestPrefix(t *testing.T) {
	routes := []model.Route{
		{Name: "api", PathPrefix: "/api", Upstream: "http://example:1"},
		{Name: "users", PathPrefix: "/api/users", Upstream: "http://example:1"},
	}
	s, err := NewSnapshot(routes, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	e, ok := s.Match(httptest.NewRequest(http.MethodGet, "/api/users/42", nil))
	if !ok || e.Route().Name != "users" {
		t.Fatalf("expected longest-prefix match 'users', got ok=%v entry=%v", ok, e)
	}
}

func TestSnapshotMatchMethodAllowList(t *testing.T) {
	routes := []model.Route{
		{Name: "writes", PathPrefix: "/api", Upstream: "http://example:1", Methods: []string{"POST"}},
	}
	s, _ := NewSnapshot(routes, discardLogger())
	defer s.Close()

	if _, ok := s.Match(httptest.NewRequest(http.MethodGet, "/api/x", nil)); ok {
		t.Error("GET should not match a POST-only route")
	}
	if _, ok := s.Match(httptest.NewRequest(http.MethodPost, "/api/x", nil)); !ok {
		t.Error("POST should match a POST-only route")
	}
}

func TestResolveDispatchStripsPrefix(t *testing.T) {
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	s, _ := NewSnapshot([]model.Route{
		{Name: "u", PathPrefix: "/api/users", Upstream: backend.URL, StripPrefix: true},
	}, discardLogger())
	defer s.Close()

	h := Resolve(staticSource{s})(http.HandlerFunc(Dispatch))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/users/42", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/42" {
		t.Fatalf("upstream path = %q, want /42 (prefix stripped)", gotPath)
	}
}

func TestDispatchUnmatched404(t *testing.T) {
	s, _ := NewSnapshot(nil, discardLogger())
	defer s.Close()

	h := Resolve(staticSource{s})(http.HandlerFunc(Dispatch))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestNewSnapshotRejectsBadUpstream(t *testing.T) {
	_, err := NewSnapshot([]model.Route{
		{Name: "bad", PathPrefix: "/x", Upstream: "not-a-url"},
	}, discardLogger())
	if err == nil {
		t.Fatal("expected error for upstream without scheme/host")
	}
}
