// Package proxy is the data-plane core: it compiles routes into an immutable
// Snapshot of reverse proxies + limiters, matches requests against it, and
// forwards them upstream. Snapshots are built once and read lock-free; the
// registry owns swapping them (hot-reload in a later phase).
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/ratelimit"
)

type ctxKey int

const entryCtxKey ctxKey = iota

// Entry is one compiled route: its config plus the reverse proxy and limiter
// built from it.
type Entry struct {
	route   model.Route
	proxy   *httputil.ReverseProxy
	limiter ratelimit.Limiter // nil when the route has no rate limit
}

// Route returns the route's configuration.
func (e *Entry) Route() model.Route { return e.route }

// Limiter returns the route's limiter, or nil if rate limiting is disabled.
func (e *Entry) Limiter() ratelimit.Limiter { return e.limiter }

// Snapshot is an immutable, ready-to-serve set of routes, sorted so the longest
// path prefix matches first.
type Snapshot struct {
	entries []*Entry
}

// SnapshotSource supplies the currently active snapshot. The registry implements
// it; the data-plane middleware depends only on this interface.
type SnapshotSource interface {
	Current() *Snapshot
}

// NewSnapshot compiles routes into a Snapshot, building one reverse proxy and
// limiter per route. It returns an error if any route is invalid; on error, any
// limiters already created are stopped so nothing leaks.
func NewSnapshot(routes []model.Route, logger *slog.Logger) (*Snapshot, error) {
	s := &Snapshot{}
	for _, r := range routes {
		entry, err := compile(r, logger)
		if err != nil {
			s.Close() // stop limiters created so far
			return nil, err
		}
		s.entries = append(s.entries, entry)
	}
	sort.SliceStable(s.entries, func(i, j int) bool {
		return len(s.entries[i].route.PathPrefix) > len(s.entries[j].route.PathPrefix)
	})
	return s, nil
}

func compile(r model.Route, logger *slog.Logger) (*Entry, error) {
	target, err := url.Parse(r.Upstream)
	if err != nil {
		return nil, fmt.Errorf("route %q: invalid upstream %q: %w", r.Name, r.Upstream, err)
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("route %q: upstream %q must include scheme and host", r.Name, r.Upstream)
	}
	limiter, err := ratelimit.New(r.RateLimit)
	if err != nil {
		return nil, fmt.Errorf("route %q: %w", r.Name, err)
	}
	return &Entry{
		route:   r,
		proxy:   newReverseProxy(target, r, logger),
		limiter: limiter,
	}, nil
}

// Routes returns the compiled routes in match-precedence order.
func (s *Snapshot) Routes() []model.Route {
	out := make([]model.Route, len(s.entries))
	for i, e := range s.entries {
		out[i] = e.route
	}
	return out
}

// Close stops every limiter in the snapshot. Call it when discarding a snapshot.
func (s *Snapshot) Close() {
	for _, e := range s.entries {
		if e.limiter != nil {
			e.limiter.Stop()
		}
	}
}

// Match returns the first (longest-prefix) entry whose prefix and method match.
func (s *Snapshot) Match(r *http.Request) (*Entry, bool) {
	for _, e := range s.entries {
		if !pathMatches(r.URL.Path, e.route.PathPrefix) {
			continue
		}
		if len(e.route.Methods) > 0 && !methodAllowed(r.Method, e.route.Methods) {
			continue
		}
		return e, true
	}
	return nil, false
}

// Resolve is middleware that matches each request against the current snapshot
// and stores the matched entry (or nil) in the context. It never short-circuits,
// so logging and metrics observe unmatched requests too; Dispatch emits the 404.
func Resolve(src SnapshotSource) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var matched *Entry
			if e, ok := src.Current().Match(r); ok {
				matched = e
			}
			ctx := context.WithValue(r.Context(), entryCtxKey, matched)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Dispatch is the terminal handler: it proxies to the matched upstream, or
// returns 404 when nothing matched.
func Dispatch(w http.ResponseWriter, r *http.Request) {
	e, _ := r.Context().Value(entryCtxKey).(*Entry)
	if e == nil {
		http.Error(w, "no route configured for this path", http.StatusNotFound)
		return
	}
	e.proxy.ServeHTTP(w, r)
}

// EntryFromContext returns the entry matched by Resolve, if any.
func EntryFromContext(ctx context.Context) (*Entry, bool) {
	e, ok := ctx.Value(entryCtxKey).(*Entry)
	if !ok || e == nil {
		return nil, false
	}
	return e, true
}

func newReverseProxy(target *url.URL, route model.Route, logger *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			outPath := pr.In.URL.Path
			if route.StripPrefix {
				outPath = strings.TrimPrefix(outPath, strings.TrimSuffix(route.PathPrefix, "/"))
				if outPath == "" || outPath[0] != '/' {
					outPath = "/" + outPath
				}
			}
			pr.SetURL(target)
			pr.Out.URL.Path = singleJoiningSlash(target.Path, outPath)
			pr.Out.URL.RawPath = ""
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("upstream request failed",
				"route", route.Name,
				"upstream", target.String(),
				"path", r.URL.Path,
				"error", err)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
}

// pathMatches reports whether path falls under prefix on a path-segment
// boundary, so "/api" matches "/api" and "/api/x" but not "/apiv2".
func pathMatches(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	return strings.HasSuffix(prefix, "/") || path[len(prefix)] == '/'
}

func methodAllowed(method string, allowed []string) bool {
	for _, m := range allowed {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
