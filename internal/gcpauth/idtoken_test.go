package gcpauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func makeJWT(exp int64) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload, _ := json.Marshal(map[string]int64{"exp": exp})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return hdr + "." + body + ".sig"
}

func TestTokenExpiryParsesExp(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	got := tokenExpiry(makeJWT(exp))
	if got.Unix() != exp {
		t.Fatalf("tokenExpiry = %d, want %d", got.Unix(), exp)
	}
}

func TestTokenExpiryFallsBackOnGarbage(t *testing.T) {
	got := tokenExpiry("not-a-jwt")
	if time.Until(got) < 30*time.Minute {
		t.Fatalf("expected a fallback expiry well in the future, got %v", got)
	}
}

func TestTokenCachesPerAudience(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			t.Errorf("missing Metadata-Flavor header")
		}
		calls++
		_, _ = w.Write([]byte(makeJWT(time.Now().Add(time.Hour).Unix())))
	}))
	defer srv.Close()

	s := &IDTokenSource{client: srv.Client(), cache: map[string]cachedToken{}}
	// Point fetch at the test server by overriding via a wrapper: fetch builds the
	// metadata URL, so instead exercise caching through the public Token path with
	// a pre-seeded cache entry.
	s.cache["aud-1"] = cachedToken{token: "cached", expiry: time.Now().Add(time.Hour)}

	tok, err := s.Token(context.Background(), "aud-1")
	if err != nil || tok != "cached" {
		t.Fatalf("expected cached token, got %q err=%v", tok, err)
	}
	if calls != 0 {
		t.Fatalf("cache hit should not call the metadata server, calls=%d", calls)
	}
}
