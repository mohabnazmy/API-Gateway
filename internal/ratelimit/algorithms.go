package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// --- token_bucket -----------------------------------------------------------
//
// Tokens refill at a steady rate up to a burst capacity; each request spends
// one. Allows controlled bursts after idle periods, then settles to the rate.
// Backed by the Go team's golang.org/x/time/rate (internally synchronized).

type tokenBucket struct {
	limiter *rate.Limiter
	rps     float64
	burst   int
}

func newTokenBucket(rps float64, burst int) *tokenBucket {
	if burst < 1 {
		burst = 1
	}
	return &tokenBucket{limiter: rate.NewLimiter(rate.Limit(rps), burst), rps: rps, burst: burst}
}

func (t *tokenBucket) allow() (bool, Result) {
	now := time.Now()
	r := t.limiter.ReserveN(now, 1)
	delay := r.DelayFrom(now)
	if !r.OK() || delay > 0 {
		r.Cancel() // don't consume a token we're rejecting
		return false, Result{Limit: t.burst, Remaining: 0, Reset: t.refill(0), RetryAfter: delay}
	}
	tokens := t.limiter.TokensAt(now)
	remaining := int(tokens)
	if remaining < 0 {
		remaining = 0
	}
	return true, Result{Limit: t.burst, Remaining: remaining, Reset: t.refill(tokens)}
}

// refill returns the time for the bucket to climb from `tokens` back to full.
func (t *tokenBucket) refill(tokens float64) time.Duration {
	if t.rps <= 0 {
		return 0
	}
	return time.Duration((float64(t.burst) - tokens) / t.rps * float64(time.Second))
}

// --- leaky_bucket -----------------------------------------------------------
//
// A bucket whose level "leaks" out at a constant rate; each request adds one
// unit and is rejected if it would overflow the capacity. Enforces a strictly
// constant drain (the dual of token bucket), good for hard traffic shaping.

type leakyBucket struct {
	mu       sync.Mutex
	capacity float64
	leakRate float64 // units drained per second
	level    float64
	last     time.Time
}

func newLeakyBucket(rps float64, capacity int) *leakyBucket {
	if capacity < 1 {
		capacity = 1
	}
	return &leakyBucket{capacity: float64(capacity), leakRate: rps}
}

func (l *leakyBucket) allow() (bool, Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.last.IsZero() {
		l.last = now
	}
	l.level -= now.Sub(l.last).Seconds() * l.leakRate
	if l.level < 0 {
		l.level = 0
	}
	l.last = now

	limit := int(l.capacity)
	if l.level+1 > l.capacity {
		var retry time.Duration
		if l.leakRate > 0 {
			retry = time.Duration((l.level + 1 - l.capacity) / l.leakRate * float64(time.Second))
		}
		return false, Result{Limit: limit, Remaining: 0, Reset: l.drain(), RetryAfter: retry}
	}
	l.level++
	remaining := int(l.capacity - l.level)
	return true, Result{Limit: limit, Remaining: remaining, Reset: l.drain()}
}

// drain returns the time for the bucket to empty completely at the leak rate.
func (l *leakyBucket) drain() time.Duration {
	if l.leakRate <= 0 {
		return 0
	}
	return time.Duration(l.level / l.leakRate * float64(time.Second))
}

// --- fixed_window -----------------------------------------------------------
//
// Counts requests within fixed, non-overlapping windows; the counter resets at
// each boundary. Simple, but allows up to 2× the limit across a boundary.

type fixedWindow struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	start  time.Time
	count  int
}

func newFixedWindow(limit int, window time.Duration) *fixedWindow {
	return &fixedWindow{limit: limit, window: window}
}

func (f *fixedWindow) allow() (bool, Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if f.start.IsZero() || now.Sub(f.start) >= f.window {
		f.start = now
		f.count = 0
	}
	reset := f.window - now.Sub(f.start)
	if f.count >= f.limit {
		return false, Result{Limit: f.limit, Remaining: 0, Reset: reset, RetryAfter: reset}
	}
	f.count++
	return true, Result{Limit: f.limit, Remaining: f.limit - f.count, Reset: reset}
}

// --- sliding_window ---------------------------------------------------------
//
// Approximates a rolling window by weighting the previous fixed window's count
// by how much of it still overlaps "now". Smooths out the fixed-window boundary
// burst without storing per-request timestamps (the Cloudflare approach).

type slidingWindow struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	curIndex  int64
	curCount  int
	prevCount int
}

func newSlidingWindow(limit int, window time.Duration) *slidingWindow {
	return &slidingWindow{limit: limit, window: window}
}

func (s *slidingWindow) allow() (bool, Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixNano()
	win := int64(s.window)
	index := now / win

	switch {
	case index == s.curIndex:
		// same window
	case index == s.curIndex+1:
		s.prevCount, s.curCount = s.curCount, 0
		s.curIndex = index
	default: // gap of two or more windows: previous is fully stale
		s.prevCount, s.curCount = 0, 0
		s.curIndex = index
	}

	elapsed := float64(now%win) / float64(win)
	estimated := float64(s.prevCount)*(1-elapsed) + float64(s.curCount)
	reset := time.Duration(win - now%win)
	if estimated+1 > float64(s.limit) {
		return false, Result{Limit: s.limit, Remaining: 0, Reset: reset, RetryAfter: reset}
	}
	s.curCount++
	remaining := s.limit - int(estimated) - 1
	if remaining < 0 {
		remaining = 0
	}
	return true, Result{Limit: s.limit, Remaining: remaining, Reset: reset}
}
