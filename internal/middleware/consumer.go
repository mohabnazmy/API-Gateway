package middleware

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/ratelimit"
)

type consumerCtxKey int

const consumerKey consumerCtxKey = 0

// WithConsumer stores the authenticated consumer identity in the context.
func WithConsumer(ctx context.Context, id model.Identity) context.Context {
	return context.WithValue(ctx, consumerKey, id)
}

// ConsumerFromContext returns the consumer a request was attributed to, if any.
func ConsumerFromContext(ctx context.Context) (model.Identity, bool) {
	id, ok := ctx.Value(consumerKey).(model.Identity)
	return id, ok
}

// consumerRateKey is the limiter key for a consumer (shared across that
// consumer's keys and across all routes).
func consumerRateKey(id model.Identity) string {
	return "consumer:" + strconv.FormatInt(id.ConsumerID, 10)
}

// ConsumerLimiters maintains one plan-sized limiter per plan, keyed internally by
// consumer. It rebuilds a plan's limiter when that plan's limit changes (detected
// by a signature), so admin edits to a plan take effect without a restart.
type ConsumerLimiters struct {
	mu     sync.Mutex
	byPlan map[int64]*planLimiter
}

type planLimiter struct {
	sig string
	lim ratelimit.Limiter
}

// NewConsumerLimiters returns an empty manager.
func NewConsumerLimiters() *ConsumerLimiters {
	return &ConsumerLimiters{byPlan: make(map[int64]*planLimiter)}
}

// For returns the limiter for a consumer's plan, or nil when the consumer has no
// plan limit (callers then fall back to the route limit).
func (c *ConsumerLimiters) For(id model.Identity) ratelimit.Limiter {
	if id.PlanID == 0 || !id.Limit.Enabled() {
		return nil
	}
	sig := fmt.Sprintf("%s|%g|%d|%d", id.Limit.Algorithm, id.Limit.RPS, id.Limit.Burst, id.Limit.WindowSec)

	c.mu.Lock()
	defer c.mu.Unlock()
	if pl := c.byPlan[id.PlanID]; pl != nil && pl.sig == sig {
		return pl.lim
	} else if pl != nil && pl.lim != nil {
		pl.lim.Stop() // the plan's limit changed; replace the limiter
	}
	lim, err := ratelimit.New(id.Limit)
	if err != nil || lim == nil {
		return nil
	}
	c.byPlan[id.PlanID] = &planLimiter{sig: sig, lim: lim}
	return lim
}

// Stop releases every plan limiter's background resources.
func (c *ConsumerLimiters) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, pl := range c.byPlan {
		if pl.lim != nil {
			pl.lim.Stop()
		}
	}
}
