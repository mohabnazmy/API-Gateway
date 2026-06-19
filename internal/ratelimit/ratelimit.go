// Package ratelimit provides per-route rate limiting behind a pluggable
// algorithm interface. Four algorithms are available and selected per route via
// the rate-limit policy's `algorithm` field: token_bucket (default),
// fixed_window, sliding_window, and leaky_bucket.
package ratelimit

import (
	"fmt"
	"sync"
	"time"

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

// bucket is one key's algorithm instance. Implementations guard their own state.
type bucket interface {
	allow() bool
}

// New builds a Limiter for the given policy. It returns (nil, nil) when the
// policy disables limiting, and an error for an unknown algorithm.
//
// Parameter mapping:
//   - token_bucket / leaky_bucket: rps = rate, burst = bucket capacity.
//   - fixed_window / sliding_window: the per-window limit is rps × window, where
//     window = window_sec (default 1s). So rps is the sustained rate either way.
func New(p model.RateLimitPolicy) (Limiter, error) {
	if !p.Enabled() {
		return nil, nil
	}
	algorithm := p.Algorithm
	if algorithm == "" {
		algorithm = "token_bucket"
	}

	window := time.Duration(p.WindowSec) * time.Second
	if window <= 0 {
		window = time.Second
	}
	windowLimit := int(p.RPS * window.Seconds())
	if windowLimit < 1 {
		windowLimit = 1
	}

	switch algorithm {
	case "token_bucket":
		return newKeyed(func() bucket { return newTokenBucket(p.RPS, p.Burst) }), nil
	case "leaky_bucket":
		return newKeyed(func() bucket { return newLeakyBucket(p.RPS, p.Burst) }), nil
	case "fixed_window":
		return newKeyed(func() bucket { return newFixedWindow(windowLimit, window) }), nil
	case "sliding_window":
		return newKeyed(func() bucket { return newSlidingWindow(windowLimit, window) }), nil
	default:
		return nil, fmt.Errorf("unknown rate-limit algorithm %q", algorithm)
	}
}

// keyed maintains an independent algorithm instance per key, evicting idle keys
// to bound memory. It is shared by every algorithm so the per-key map, idle
// eviction, and cleanup goroutine live in one place.
type keyed struct {
	factory func() bucket

	mu       sync.Mutex
	visitors map[string]*visitor
	done     chan struct{}
}

type visitor struct {
	bucket   bucket
	lastSeen time.Time
}

func newKeyed(factory func() bucket) *keyed {
	k := &keyed{
		factory:  factory,
		visitors: make(map[string]*visitor),
		done:     make(chan struct{}),
	}
	go k.cleanupLoop()
	return k
}

func (k *keyed) Allow(key string) bool {
	k.mu.Lock()
	v, ok := k.visitors[key]
	if !ok {
		v = &visitor{bucket: k.factory()}
		k.visitors[key] = v
	}
	v.lastSeen = time.Now()
	b := v.bucket
	k.mu.Unlock()
	return b.allow()
}

func (k *keyed) Stop() { close(k.done) }

func (k *keyed) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-k.done:
			return
		case <-ticker.C:
			k.mu.Lock()
			for key, v := range k.visitors {
				if time.Since(v.lastSeen) > 3*time.Minute {
					delete(k.visitors, key)
				}
			}
			k.mu.Unlock()
		}
	}
}
