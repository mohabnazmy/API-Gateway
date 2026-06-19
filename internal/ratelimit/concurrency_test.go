package ratelimit

import (
	"fmt"
	"sync"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// TestLimiterConcurrentAllow hammers each algorithm from many goroutines across
// a few shared keys. Run with -race to surface data races in the per-key state
// or the shared keyed wrapper.
func TestLimiterConcurrentAllow(t *testing.T) {
	algos := []string{"token_bucket", "leaky_bucket", "fixed_window", "sliding_window"}
	for _, algo := range algos {
		algo := algo
		t.Run(algo, func(t *testing.T) {
			l, err := New(model.RateLimitPolicy{Algorithm: algo, RPS: 1000, Burst: 1000, WindowSec: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer l.Stop()

			var wg sync.WaitGroup
			for g := 0; g < 24; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					for i := 0; i < 250; i++ {
						l.Allow(fmt.Sprintf("ip-%d", (g+i)%6))
					}
				}(g)
			}
			wg.Wait()
		})
	}
}

// TestTokenBucketBurstExactUnderConcurrency checks that, with a burst of N and a
// negligible refill, exactly N concurrent requests for the same key are allowed
// — no over-admission from races.
func TestTokenBucketBurstExactUnderConcurrency(t *testing.T) {
	const burst = 50
	l, err := New(model.RateLimitPolicy{Algorithm: "token_bucket", RPS: 0.0001, Burst: burst})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Stop()

	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Allow("same-key"); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != burst {
		t.Fatalf("token bucket admitted %d under concurrency, want exactly %d", allowed, burst)
	}
}
