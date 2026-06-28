package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// recordingRT records whether it was called and returns a stub 200.
type recordingRT struct{ called bool }

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.called = true
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

type stubAuth struct {
	err    error
	header string
}

func (s stubAuth) Apply(ctx context.Context, out *http.Request) error {
	if s.err != nil {
		return s.err
	}
	if s.header != "" {
		out.Header.Set(s.header, "applied")
	}
	return nil
}

func TestAuthTransportFailsClosed(t *testing.T) {
	base := &recordingRT{}
	at := &authTransport{
		base:   base,
		authn:  stubAuth{err: errors.New("token mint failed")},
		route:  "r",
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	req, _ := http.NewRequest(http.MethodGet, "https://up/x", nil)
	resp, err := at.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error so the request fails closed")
	}
	if resp != nil {
		t.Fatal("no response should be returned on auth failure")
	}
	if base.called {
		t.Fatal("base transport was called — request was NOT failed closed")
	}
}

func TestAuthTransportAppliesThenForwards(t *testing.T) {
	base := &recordingRT{}
	at := &authTransport{base: base, authn: stubAuth{header: "X-Token"}, route: "r",
		logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req, _ := http.NewRequest(http.MethodGet, "https://up/x", nil)
	if _, err := at.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if !base.called {
		t.Fatal("base transport should have been called")
	}
	if req.Header.Get("X-Token") != "applied" {
		t.Fatal("authn was not applied before forwarding")
	}
}

// TestStripInboundCredentials proxies through a real snapshot and asserts the
// caller's gateway credentials never reach the upstream when upstream_auth is set.
func TestStripInboundCredentials(t *testing.T) {
	var gotAuth, gotAPIKey, gotUp string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-API-Key")
		gotUp = r.Header.Get("X-Up")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	routes := []model.Route{{
		Name:       "r",
		PathPrefix: "/",
		Upstream:   backend.URL,
		UpstreamAuth: model.UpstreamAuth{
			Type: "bearer", Header: "X-Up", Scheme: "none", TokenRef: "uptoken",
		},
	}}
	snap, err := NewSnapshot(routes, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer caller-jwt")
	req.Header.Set("X-API-Key", "caller-key")
	e, ok := snap.Match(req)
	if !ok {
		t.Fatal("route did not match")
	}
	e.proxy.ServeHTTP(httptest.NewRecorder(), req)

	if gotAuth != "" {
		t.Errorf("inbound Authorization leaked upstream: %q", gotAuth)
	}
	if gotAPIKey != "" {
		t.Errorf("inbound X-API-Key leaked upstream: %q", gotAPIKey)
	}
	if gotUp != "uptoken" {
		t.Errorf("upstream credential not attached: X-Up = %q", gotUp)
	}
}
