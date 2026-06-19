package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// serveOnce wires a snapshot of one route to a recording backend and returns the
// path/query the backend observed plus the gateway's response code.
func serveThrough(t *testing.T, route model.Route, target, method, reqPath string) (gotPath, gotQuery string, code int) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	if route.Upstream == "" {
		route.Upstream = backend.URL + target
	}
	s, err := NewSnapshot([]model.Route{route}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := Resolve(staticSource{s})(http.HandlerFunc(Dispatch))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, reqPath, nil))
	return gotPath, gotQuery, rec.Code
}

func TestStripPrefixExactMatchBecomesRoot(t *testing.T) {
	got, _, _ := serveThrough(t,
		model.Route{Name: "u", PathPrefix: "/api/users", StripPrefix: true}, "",
		http.MethodGet, "/api/users")
	if got != "/" {
		t.Fatalf("exact-prefix strip: backend path = %q, want %q", got, "/")
	}
}

func TestStripPrefixTrailingSlash(t *testing.T) {
	got, _, _ := serveThrough(t,
		model.Route{Name: "u", PathPrefix: "/api/users", StripPrefix: true}, "",
		http.MethodGet, "/api/users/")
	if got != "/" {
		t.Fatalf("trailing-slash strip: backend path = %q, want %q", got, "/")
	}
}

func TestUpstreamBasePathIsJoined(t *testing.T) {
	got, _, _ := serveThrough(t,
		model.Route{Name: "u", PathPrefix: "/api", StripPrefix: true}, "/v1",
		http.MethodGet, "/api/thing")
	if got != "/v1/thing" {
		t.Fatalf("upstream base path join: backend path = %q, want %q", got, "/v1/thing")
	}
}

func TestQueryStringPreserved(t *testing.T) {
	_, gotQ, _ := serveThrough(t,
		model.Route{Name: "u", PathPrefix: "/api", StripPrefix: true}, "",
		http.MethodGet, "/api/x?a=1&b=2&b=3")
	if gotQ != "a=1&b=2&b=3" {
		t.Fatalf("query string: backend saw %q, want %q", gotQ, "a=1&b=2&b=3")
	}
}

// WEAKNESS (characterization): the gateway matches and forwards the raw request
// path without normalizing "." / ".." segments. A backend that resolves them
// could be reached via a public route's prefix. Documented in docs/test-findings.md.
func TestDotSegmentsForwardedUnnormalized(t *testing.T) {
	got, _, code := serveThrough(t,
		model.Route{Name: "p", PathPrefix: "/public"}, "",
		http.MethodGet, "/public/../admin")
	t.Logf("request /public/../admin -> matched, backend received path %q (code %d)", got, code)
	if code == http.StatusNotFound {
		t.Skip("path was normalized before matching; not a concern here")
	}
	if !strings.Contains(got, "..") {
		t.Errorf("expected unnormalized %q to reach backend, got %q", "..", got)
	}
}

// Two routes share a prefix; only one restricts the method. A request using the
// other method must fall through to the unrestricted route, not 404.
func TestMethodRestrictedRouteFallsThrough(t *testing.T) {
	s, _ := NewSnapshot([]model.Route{
		{Name: "writes", PathPrefix: "/api", Upstream: "http://x:1", Methods: []string{"POST"}},
		{Name: "reads", PathPrefix: "/api", Upstream: "http://x:1"},
	}, discardLogger())
	defer s.Close()

	e, ok := s.Match(httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if !ok || e.Route().Name != "reads" {
		t.Fatalf("GET should fall through to 'reads', got ok=%v entry=%v", ok, e)
	}
	e2, _ := s.Match(httptest.NewRequest(http.MethodPost, "/api/x", nil))
	if e2 == nil || e2.Route().Name != "writes" {
		t.Fatalf("POST should match 'writes'")
	}
}

// WEAKNESS (characterization): a path that exists but whose method is not in the
// allow-list yields 404, not 405 Method Not Allowed (no distinct "method not
// allowed" signal to clients). Documented in docs/test-findings.md.
func TestDisallowedMethodReturns404Not405(t *testing.T) {
	_, _, code := serveThrough(t,
		model.Route{Name: "p", PathPrefix: "/p", Methods: []string{"POST"}}, "",
		http.MethodGet, "/p/x")
	if code != http.StatusNotFound {
		t.Fatalf("disallowed method: got %d, want 404 (documented weakness — ideally 405)", code)
	}
}
