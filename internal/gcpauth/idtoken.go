// Package gcpauth fetches Google-signed OIDC identity tokens from the GCP
// metadata server, so the gateway (running on Cloud Run / GCE) can call private
// Cloud Run services. Tokens are cached per audience and refreshed before expiry.
package gcpauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const metadataIdentityURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity"

// IDTokenSource mints and caches OIDC identity tokens keyed by audience.
type IDTokenSource struct {
	client *http.Client

	fetchMu sync.Mutex // serializes metadata fetches (single-flight)
	mu      sync.Mutex // guards cache; never held during a fetch
	cache   map[string]cachedToken
}

type cachedToken struct {
	token  string
	expiry time.Time
}

// NewIDTokenSource returns a source using a short-timeout HTTP client for the
// metadata server (which is local to the instance and fast).
func NewIDTokenSource() *IDTokenSource {
	return &IDTokenSource{
		client: &http.Client{Timeout: 5 * time.Second},
		cache:  make(map[string]cachedToken),
	}
}

// Token returns a valid identity token for the given audience, fetching a new
// one when the cache is empty or near expiry.
func (s *IDTokenSource) Token(ctx context.Context, audience string) (string, error) {
	if t, ok := s.cached(audience); ok {
		return t, nil
	}
	// Single-flight: only one goroutine fetches at a time; others wait, then hit
	// the cache populated below. The cache lock is never held during the fetch.
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	if t, ok := s.cached(audience); ok {
		return t, nil
	}

	token, err := s.fetch(ctx, audience)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cache[audience] = cachedToken{token: token, expiry: tokenExpiry(token)}
	s.mu.Unlock()
	return token, nil
}

func (s *IDTokenSource) cached(audience string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.cache[audience]; ok && time.Until(t.expiry) > time.Minute {
		return t.token, true
	}
	return "", false
}

func (s *IDTokenSource) fetch(ctx context.Context, audience string) (string, error) {
	u := metadataIdentityURL + "?audience=" + url.QueryEscape(audience) + "&format=full"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("metadata identity request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata identity status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return strings.TrimSpace(string(body)), nil
}

// tokenExpiry parses the JWT's exp claim; falls back to ~45 min if unparsable.
func tokenExpiry(token string) time.Time {
	fallback := time.Now().Add(45 * time.Minute)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fallback
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fallback
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return fallback
	}
	return time.Unix(claims.Exp, 0)
}
