package model

// Identity is the consumer a request is attributed to after API-key auth,
// together with the rate limit derived from the consumer's plan. Limit is
// disabled (zero) when the consumer has no plan. The data plane keys rate
// limiting on the consumer, not the client IP, so each customer gets a bucket
// sized to their tier.
type Identity struct {
	ConsumerID   int64
	ConsumerName string
	PlanID       int64
	Limit        RateLimitPolicy
}
