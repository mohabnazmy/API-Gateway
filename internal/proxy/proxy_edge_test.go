package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// serveThrough wires a snapshot of one route to a recording backend and returns
// the path/query the backend observed plus the gateway's response code.
func serveThrough(t *testing.T, route model.Route, target, method, reqPath string, opts ...Options) (gotPath, gotQuery string, code int) {
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
	s, err := NewSnapshot([]model.Route{route}, discardLogger(), opts...)
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

// W2 fixed: dot-segments within a route's prefix are normalized before
// forwarding, so the backend never sees ".." / ".".
func TestDotSegmentsNormalizedWithinPrefix(t *testing.T) {
	got, _, code := serveThrough(t,
		model.Route{Name: "api", PathPrefix: "/api", StripPrefix: true}, "",
		http.MethodGet, "/api/a/../b")
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	if got != "/b" {
		t.Fatalf("normalized path: backend saw %q, want %q", got, "/b")
	}
}

// W2 fixed: a traversal that escapes the route's prefix no longer matches it.
// "/public/../admin" canonicalizes to "/admin", which has no route → 404.
func TestTraversalEscapingPrefixNoLongerMatches(t *testing.T) {
	_, _, code := serveThrough(t,
		model.Route{Name: "p", PathPrefix: "/public"}, "",
		http.MethodGet, "/public/../admin")
	if code != http.StatusNotFound {
		t.Fatalf("traversal escaping /public: got %d, want 404 (no /admin route)", code)
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

// W5 fixed: a path that exists but whose method is not allowed returns 405 with
// an Allow header listing the accepted methods.
func TestDisallowedMethodReturns405WithAllow(t *testing.T) {
	s, _ := NewSnapshot([]model.Route{
		{Name: "p", PathPrefix: "/p", Upstream: "http://x:1", Methods: []string{"POST", "PUT"}},
	}, discardLogger())
	defer s.Close()

	h := Resolve(staticSource{s})(http.HandlerFunc(Dispatch))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/x", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("disallowed method: got %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "POST, PUT" {
		t.Fatalf("Allow header = %q, want %q", got, "POST, PUT")
	}
}

// W3 fixed: a slow upstream that doesn't send response headers within the
// transport's ResponseHeaderTimeout is cut off and returned as 502.
func TestSlowUpstreamTimesOutAs502(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	opts := Options{Transport: NewTransport(2*time.Second, 50*time.Millisecond)}
	s, _ := NewSnapshot([]model.Route{
		{Name: "slow", PathPrefix: "/s", Upstream: backend.URL, StripPrefix: true},
	}, discardLogger(), opts)
	defer s.Close()

	h := Resolve(staticSource{s})(http.HandlerFunc(Dispatch))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s/x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("slow upstream: got %d, want 502", rec.Code)
	}
}
