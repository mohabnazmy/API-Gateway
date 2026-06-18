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
