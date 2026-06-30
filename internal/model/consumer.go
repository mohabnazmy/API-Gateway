package model

// Plan is a named tier (free, pro, enterprise, …) that sizes a consumer's rate
// limit and quota by volume.
type Plan struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	RPS        float64 `json:"rps"`
	Burst      int     `json:"burst"`
	DailyQuota int     `json:"daily_quota,omitempty"` // 0 = unmetered
}

// Consumer is a customer or application that calls the API. It owns one or more
// API keys and is assigned a plan; rate limiting keys on the consumer.
type Consumer struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	PlanID  int64  `json:"plan_id,omitempty"` // 0 = no plan assigned
	Enabled bool   `json:"enabled"`
}

// APIKey is a consumer's credential. Only metadata is ever returned; the secret
// is stored as a hash and shown in plaintext once at creation (by the admin API).
type APIKey struct {
	ID         int64  `json:"id"`
	ConsumerID int64  `json:"consumer_id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Revoked    bool   `json:"revoked"`
}

// AdminUser is a control-plane operator. PasswordHash is bcrypt; TokenVersion
// gates session-token revocation (bump it to invalidate a user's tokens).
type AdminUser struct {
	ID           int64
	Username     string
	PasswordHash string
	TokenVersion int64
}
