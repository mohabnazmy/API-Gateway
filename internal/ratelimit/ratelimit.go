// Package ratelimit provides per-route rate limiting behind a pluggable
// algorithm interface. Phase 1 ships the token-bucket algorithm; additional
// algorithms (fixed/sliding window, leaky bucket) slot in behind Limiter.
package ratelimit

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// Limiter decides whether a request identified by key (e.g. client IP) is
// allowed. Implementations are safe for concurrent use. Stop releases any
// background resources and must be called when the limiter is discarded (e.g.
// when a config snapshot is replaced).
type Limiter interface {
	Allow(key string) bool
	Stop()
}

// New builds a Limiter for the given policy. It returns (nil, nil) when the
// policy disables limiting, and an error for an unknown algorithm.
func New(p model.RateLimitPolicy) (Limiter, error) {
	if !p.Enabled() {
		return nil, nil
	}
	algorithm := p.Algorithm
	if algorithm == "" {
		algorithm = "token_bucket"
	}
	switch algorithm {
	case "token_bucket":
		return newTokenBucket(p.RPS, p.Burst), nil
	default:
		return nil, fmt.Errorf("unknown rate-limit algorithm %q", algorithm)
	}
}

// tokenBucket maintains an independent token-bucket limiter per key, evicting
// idle keys to bound memory.
type tokenBucket struct {
	rps   rate.Limit
	burst int

	mu       sync.Mutex
	visitors map[string]*visitor
	done     chan struct{}
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newTokenBucket(rps float64, burst int) *tokenBucket {
	if burst < 1 {
		burst = 1
	}
	tb := &tokenBucket{
		rps:      rate.Limit(rps),
		burst:    burst,
		visitors: make(map[string]*visitor),
		done:     make(chan struct{}),
	}
	go tb.cleanupLoop()
	return tb
}

func (tb *tokenBucket) Allow(key string) bool {
	tb.mu.Lock()
	v, ok := tb.visitors[key]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(tb.rps, tb.burst)}
		tb.visitors[key] = v
	}
	v.lastSeen = time.Now()
	limiter := v.limiter
	tb.mu.Unlock()
	return limiter.Allow()
}

func (tb *tokenBucket) Stop() { close(tb.done) }

func (tb *tokenBucket) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-tb.done:
			return
		case <-ticker.C:
			tb.mu.Lock()
			for key, v := range tb.visitors {
				if time.Since(v.lastSeen) > 3*time.Minute {
					delete(tb.visitors, key)
				}
			}
			tb.mu.Unlock()
		}
	}
}
