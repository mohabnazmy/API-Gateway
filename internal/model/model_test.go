package model

import (
	"encoding/json"
	"testing"
)

func TestUpstreamAuthUnmarshal(t *testing.T) {
	t.Run("legacy bare string", func(t *testing.T) {
		var r Route
		if err := json.Unmarshal([]byte(`{"upstream_auth":"google_oidc"}`), &r); err != nil {
			t.Fatal(err)
		}
		if r.UpstreamAuth.Type != "google_oidc" || r.UpstreamAuth.Audience != "" {
			t.Fatalf("got %+v", r.UpstreamAuth)
		}
		if !r.UpstreamAuth.Enabled() {
			t.Fatal("should be enabled")
		}
	})

	t.Run("object form with audience", func(t *testing.T) {
		var r Route
		in := `{"upstream_auth":{"type":"google_oidc","audience":"https://x"}}`
		if err := json.Unmarshal([]byte(in), &r); err != nil {
			t.Fatal(err)
		}
		if r.UpstreamAuth.Type != "google_oidc" || r.UpstreamAuth.Audience != "https://x" {
			t.Fatalf("got %+v", r.UpstreamAuth)
		}
	})

	t.Run("absent is disabled", func(t *testing.T) {
		var r Route
		if err := json.Unmarshal([]byte(`{"name":"x"}`), &r); err != nil {
			t.Fatal(err)
		}
		if r.UpstreamAuth.Enabled() {
			t.Fatal("absent upstream_auth should be disabled")
		}
	})
}
