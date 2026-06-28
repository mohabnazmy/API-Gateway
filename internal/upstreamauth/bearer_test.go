package upstreamauth

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

func TestResolveSecret(t *testing.T) {
	t.Run("literal", func(t *testing.T) {
		got, err := resolveSecret("sk-literal")
		if err != nil || got != "sk-literal" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv("UP_SECRET", "  envtok\n")
		got, err := resolveSecret("env:UP_SECRET")
		if err != nil || got != "envtok" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("env unset", func(t *testing.T) {
		if _, err := resolveSecret("env:DEFINITELY_UNSET_XYZ"); err == nil {
			t.Fatal("expected error for unset env")
		}
	})
	t.Run("file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "tok")
		if err := os.WriteFile(p, []byte("filetok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolveSecret("file:" + p)
		if err != nil || got != "filetok" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

func TestBearer(t *testing.T) {
	t.Run("default scheme", func(t *testing.T) {
		a, err := newBearer(model.UpstreamAuth{TokenRef: "abc"})
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, "https://up/x", nil)
		if err := a.Apply(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer abc" {
			t.Fatalf("Authorization = %q", got)
		}
	})
	t.Run("custom header, raw scheme", func(t *testing.T) {
		a, err := newBearer(model.UpstreamAuth{TokenRef: "k123", Header: "X-API-Key", Scheme: "none"})
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, "https://up/x", nil)
		_ = a.Apply(context.Background(), req)
		if got := req.Header.Get("X-API-Key"); got != "k123" {
			t.Fatalf("X-API-Key = %q", got)
		}
	})
	t.Run("missing token_ref", func(t *testing.T) {
		if _, err := newBearer(model.UpstreamAuth{}); err == nil {
			t.Fatal("expected error")
		}
	})
}
