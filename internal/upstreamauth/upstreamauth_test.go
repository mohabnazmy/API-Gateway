package upstreamauth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

type fakeSource struct {
	token string
	err   error
}

func (f fakeSource) Token(ctx context.Context, audience string) (string, error) {
	return f.token, f.err
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     model.UpstreamAuth
		wantNil bool
		wantErr bool
	}{
		{"empty is none", model.UpstreamAuth{}, true, false},
		{"explicit none", model.UpstreamAuth{Type: "none"}, true, false},
		{"google_oidc", model.UpstreamAuth{Type: "google_oidc"}, false, false},
		{"unknown type", model.UpstreamAuth{Type: "bogus"}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := New(tt.cfg, "https://up.example")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if (a == nil) != tt.wantNil {
				t.Fatalf("authenticator nil = %v, wantNil %v", a == nil, tt.wantNil)
			}
		})
	}
}

func TestNewGoogleAudience(t *testing.T) {
	// Audience defaults to the upstream origin...
	a, err := New(model.UpstreamAuth{Type: "google_oidc"}, "https://up.example")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.(*googleOIDC).audience; got != "https://up.example" {
		t.Fatalf("default audience = %q", got)
	}
	// ...and the route may override it.
	b, err := New(model.UpstreamAuth{Type: "google_oidc", Audience: "custom-aud"}, "https://up.example")
	if err != nil {
		t.Fatal(err)
	}
	if got := b.(*googleOIDC).audience; got != "custom-aud" {
		t.Fatalf("override audience = %q", got)
	}
}

func TestGoogleOIDCApply(t *testing.T) {
	g := &googleOIDC{audience: "https://up.example", source: fakeSource{token: "tok123"}}
	req, _ := http.NewRequest(http.MethodGet, "https://up.example/x", nil)
	if err := g.Apply(context.Background(), req); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer tok123")
	}
}

func TestGoogleOIDCApplyError(t *testing.T) {
	g := &googleOIDC{audience: "https://up.example", source: fakeSource{err: errors.New("boom")}}
	req, _ := http.NewRequest(http.MethodGet, "https://up.example/x", nil)
	if err := g.Apply(context.Background(), req); err == nil {
		t.Fatal("expected error from token source")
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization set despite token error: %q", got)
	}
}
