package ratelimit

import (
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

func TestNewDisabledReturnsNil(t *testing.T) {
	l, err := New(model.RateLimitPolicy{RPS: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if l != nil {
		t.Fatal("expected nil limiter for disabled policy")
	}
}

func TestNewUnknownAlgorithm(t *testing.T) {
	if _, err := New(model.RateLimitPolicy{Algorithm: "nope", RPS: 1, Burst: 1}); err == nil {
		t.Fatal("expected error for unknown algorithm")
	}
}

func TestTokenBucketBurstThenDeny(t *testing.T) {
	l, err := New(model.RateLimitPolicy{Algorithm: "token_bucket", RPS: 1, Burst: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()

	if !l.Allow("ip-a") || !l.Allow("ip-a") {
		t.Fatal("first two requests should be allowed (burst = 2)")
	}
	if l.Allow("ip-a") {
		t.Fatal("third immediate request should be denied")
	}
	// A different key has an independent bucket.
	if !l.Allow("ip-b") {
		t.Fatal("a different key should have its own bucket")
	}
}

func TestDefaultAlgorithmIsTokenBucket(t *testing.T) {
	l, err := New(model.RateLimitPolicy{RPS: 1, Burst: 1}) // Algorithm omitted
	if err != nil {
		t.Fatalf("empty algorithm should default, got error: %v", err)
	}
	if l == nil {
		t.Fatal("expected a limiter")
	}
	l.Stop()
}

// allowN reports how many of n immediate Allow(key) calls succeeded.
func allowN(l Limiter, key string, n int) int {
	allowed := 0
	for i := 0; i < n; i++ {
		if l.Allow(key) {
			allowed++
		}
	}
	return allowed
}

func TestLeakyBucketCapacityThenDeny(t *testing.T) {
	// Negligible leak during the test (rps tiny); capacity = burst = 2.
	l, err := New(model.RateLimitPolicy{Algorithm: "leaky_bucket", RPS: 0.001, Burst: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()

	if got := allowN(l, "ip", 5); got != 2 {
		t.Fatalf("leaky_bucket allowed %d, want 2 (capacity)", got)
	}
	if got := allowN(l, "other", 5); got != 2 {
		t.Fatal("a different key should have its own bucket")
	}
}

func TestFixedWindowLimitThenDeny(t *testing.T) {
	// limit = rps × window = 0.3 × 10s = 3, with a 10s window so the test stays
	// inside a single window (no boundary flakiness).
	l, err := New(model.RateLimitPolicy{Algorithm: "fixed_window", RPS: 0.3, WindowSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()

	if got := allowN(l, "ip", 6); got != 3 {
		t.Fatalf("fixed_window allowed %d, want 3 (limit)", got)
	}
}

func TestSlidingWindowLimitThenDeny(t *testing.T) {
	// Fresh key → prevCount is 0, so the weighting is irrelevant and the limit
	// is exactly rps × window = 0.3 × 10s = 3.
	l, err := New(model.RateLimitPolicy{Algorithm: "sliding_window", RPS: 0.3, WindowSec: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()

	if got := allowN(l, "ip", 6); got != 3 {
		t.Fatalf("sliding_window allowed %d, want 3 (limit)", got)
	}
}

func TestAllAlgorithmsConstructable(t *testing.T) {
	for _, algo := range []string{"token_bucket", "leaky_bucket", "fixed_window", "sliding_window"} {
		l, err := New(model.RateLimitPolicy{Algorithm: algo, RPS: 5, Burst: 5, WindowSec: 1})
		if err != nil {
			t.Fatalf("algorithm %q: unexpected error: %v", algo, err)
		}
		if l == nil {
			t.Fatalf("algorithm %q: expected a limiter", algo)
		}
		l.Stop()
	}
}
