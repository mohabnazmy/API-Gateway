package upstreamauth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// bearer attaches a static credential to a fixed header on every request — a
// pre-shared token or API key. The token is resolved once at construction.
type bearer struct {
	header string
	value  string
}

func newBearer(cfg model.UpstreamAuth) (*bearer, error) {
	if cfg.TokenRef == "" {
		return nil, fmt.Errorf("bearer: token_ref is required")
	}
	token, err := resolveSecret(cfg.TokenRef)
	if err != nil {
		return nil, fmt.Errorf("bearer: %w", err)
	}
	if token == "" {
		return nil, fmt.Errorf("bearer: resolved token is empty")
	}

	header := cfg.Header
	if header == "" {
		header = "Authorization"
	}
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = "Bearer"
	}

	// scheme "none" sends the raw token (e.g. an X-API-Key header).
	value := token
	if !strings.EqualFold(scheme, "none") {
		value = scheme + " " + token
	}
	return &bearer{header: header, value: value}, nil
}

func (b *bearer) Apply(ctx context.Context, out *http.Request) error {
	out.Header.Set(b.header, b.value)
	return nil
}
