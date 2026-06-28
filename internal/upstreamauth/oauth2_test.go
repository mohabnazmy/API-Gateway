package upstreamauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

func TestOAuth2ClientCredentials(t *testing.T) {
	var hits int32
	var gotUser, gotPass, gotScope, gotGrant, gotAudience string
	var audienceSent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		gotUser, gotPass, _ = basicAuth(r)
		_ = r.ParseForm()
		gotScope = r.Form.Get("scope")
		gotGrant = r.Form.Get("grant_type")
		gotAudience = r.Form.Get("audience")
		_, audienceSent = r.Form["audience"]
		w.Header().Set("Content-Type", "application/json")
		// expires_in as a STRING — some issuers do this; must still parse.
		_, _ = w.Write([]byte(`{"access_token":"at-1","token_type":"Bearer","expires_in":"3600"}`))
	}))
	defer srv.Close()

	a, err := newOAuth2(model.UpstreamAuth{
		TokenURL: srv.URL,
		ClientID: "cid",
		Scopes:   []string{"api.read", "api.write"},
	})
	if err != nil {
		t.Fatal(err)
	}

	apply := func() string {
		req, _ := http.NewRequest(http.MethodGet, "https://up/x", nil)
		if err := a.Apply(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}

	if got := apply(); got != "Bearer at-1" {
		t.Fatalf("Authorization = %q", got)
	}
	// Second call within TTL must be served from cache (no extra fetch).
	_ = apply()
	if h := atomic.LoadInt32(&hits); h != 1 {
		t.Fatalf("token endpoint hit %d times, want 1 (caching)", h)
	}
	if gotGrant != "client_credentials" || gotScope != "api.read api.write" || gotUser != "cid" || gotPass != "" {
		t.Fatalf("request: grant=%q scope=%q user=%q pass=%q", gotGrant, gotScope, gotUser, gotPass)
	}
	// No audience configured → the audience param must NOT be sent.
	if audienceSent {
		t.Fatalf("audience sent without being configured: %q", gotAudience)
	}

	// Expire the cache: the next call refetches.
	a.mu.Lock()
	a.expiry = time.Unix(0, 0)
	a.mu.Unlock()
	_ = apply()
	if h := atomic.LoadInt32(&hits); h != 2 {
		t.Fatalf("after expiry: hits = %d, want 2", h)
	}
}

func TestOAuth2AudienceSentWhenConfigured(t *testing.T) {
	var gotAudience string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotAudience = r.Form.Get("audience")
		_, _ = w.Write([]byte(`{"access_token":"at","expires_in":3600}`))
	}))
	defer srv.Close()

	a, err := newOAuth2(model.UpstreamAuth{TokenURL: srv.URL, ClientID: "cid", Audience: "https://api.internal"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://up/x", nil)
	if err := a.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if gotAudience != "https://api.internal" {
		t.Fatalf("audience = %q, want the configured value", gotAudience)
	}
}

func basicAuth(r *http.Request) (string, string, bool) { return r.BasicAuth() }

func TestOAuth2Validation(t *testing.T) {
	if _, err := newOAuth2(model.UpstreamAuth{ClientID: "x"}); err == nil {
		t.Fatal("expected error for missing token_url")
	}
	if _, err := newOAuth2(model.UpstreamAuth{TokenURL: "https://t"}); err == nil {
		t.Fatal("expected error for missing client_id")
	}
}
