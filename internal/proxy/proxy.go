// Package proxy is the data-plane core: it compiles routes into an immutable
// Snapshot of reverse proxies + limiters, matches requests against it, and
// forwards them upstream. Snapshots are built once and read lock-free; the
// registry owns swapping them (hot-reload in a later phase).
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	stdpath "path"
	"sort"
	"strings"
	"time"

	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/ratelimit"
)

type ctxKey int

const (
	entryCtxKey ctxKey = iota
	allowedMethodsCtxKey
)

// Options configures how snapshots build their reverse proxies.
type Options struct {
	// Transport is used by every route's reverse proxy. When nil, a default
	// transport with sane dial/response timeouts is used.
	Transport http.RoundTripper
}

// NewTransport builds an upstream transport with the given dial and
// response-header timeouts, so a slow or hung upstream can't tie up the gateway.
func NewTransport(dialTimeout, responseHeaderTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
}

// defaultTransport is used when Options.Transport is nil (e.g. in tests).
var defaultTransport = NewTransport(10*time.Second, 30*time.Second)

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
func NewSnapshot(routes []model.Route, logger *slog.Logger, opts ...Options) (*Snapshot, error) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	transport := o.Transport
	if transport == nil {
		transport = defaultTransport
	}

	s := &Snapshot{}
	for _, r := range routes {
		entry, err := compile(r, logger, transport)
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

func compile(r model.Route, logger *slog.Logger, transport http.RoundTripper) (*Entry, error) {
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
		proxy:   newReverseProxy(target, r, logger, transport),
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

// allowedMethods returns the union of methods accepted by routes whose prefix
// matches the path. It is only meaningful when Match found no entry: a
// prefix-matching route with no method restriction would have matched any
// method, so a non-empty result here means every matching route restricts
// methods and the request's method is in none of them (→ 405).
func (s *Snapshot) allowedMethods(path string) []string {
	set := make(map[string]struct{})
	for _, e := range s.entries {
		if !pathMatches(path, e.route.PathPrefix) || len(e.route.Methods) == 0 {
			continue
		}
		for _, m := range e.route.Methods {
			set[strings.ToUpper(m)] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Resolve is middleware that normalizes the path, matches the request against
// the current snapshot, and stores the matched entry (or nil) in the context. It
// never short-circuits, so logging and metrics observe unmatched requests too;
// Dispatch emits the final 404/405.
func Resolve(src SnapshotSource) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// W2: collapse "." / ".." segments so matching and forwarding act on
			// a canonical path (prevents traversal under a route's prefix).
			if cleaned := cleanPath(r.URL.Path); cleaned != r.URL.Path {
				r.URL.Path = cleaned
				r.URL.RawPath = ""
			}

			snap := src.Current()
			ctx := r.Context()
			if e, ok := snap.Match(r); ok {
				ctx = context.WithValue(ctx, entryCtxKey, e)
			} else {
				ctx = context.WithValue(ctx, entryCtxKey, (*Entry)(nil))
				if methods := snap.allowedMethods(r.URL.Path); len(methods) > 0 {
					ctx = context.WithValue(ctx, allowedMethodsCtxKey, methods)
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Dispatch is the terminal handler: it proxies to the matched upstream, returns
// 405 (with an Allow header) when the path matched but the method didn't, or 404
// when nothing matched.
func Dispatch(w http.ResponseWriter, r *http.Request) {
	if e, _ := r.Context().Value(entryCtxKey).(*Entry); e != nil {
		e.proxy.ServeHTTP(w, r)
		return
	}
	if methods, ok := r.Context().Value(allowedMethodsCtxKey).([]string); ok && len(methods) > 0 {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.Error(w, "no route configured for this path", http.StatusNotFound)
}

// EntryFromContext returns the entry matched by Resolve, if any.
func EntryFromContext(ctx context.Context) (*Entry, bool) {
	e, ok := ctx.Value(entryCtxKey).(*Entry)
	if !ok || e == nil {
		return nil, false
	}
	return e, true
}

func newReverseProxy(target *url.URL, route model.Route, logger *slog.Logger, transport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: transport,
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

// cleanPath canonicalizes p: it ensures a leading slash, collapses "." / ".."
// and duplicate slashes via path.Clean, and preserves a trailing slash.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	cp := stdpath.Clean(p)
	if strings.HasSuffix(p, "/") && cp != "/" && !strings.HasSuffix(cp, "/") {
		cp += "/"
	}
	return cp
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
