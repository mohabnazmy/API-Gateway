package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// HashAPIKey returns the hex SHA-256 of a plaintext key. Only the hash is stored;
// the plaintext is shown once at creation and never persisted.
func HashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// --- plans ---

// UpsertPlan inserts (ID == 0) or updates a plan, and returns its id.
func (s *SQLite) UpsertPlan(ctx context.Context, p model.Plan) (int64, error) {
	return s.writeID(ctx, func(tx *sql.Tx) (int64, error) {
		if p.ID == 0 {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO plans (name, rps, burst, daily_quota, created_at) VALUES (?, ?, ?, ?, ?)`,
				p.Name, p.RPS, p.Burst, nullInt(p.DailyQuota), nowStamp())
			if err != nil {
				return 0, err
			}
			return res.LastInsertId()
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE plans SET name = ?, rps = ?, burst = ?, daily_quota = ? WHERE id = ?`,
			p.Name, p.RPS, p.Burst, nullInt(p.DailyQuota), p.ID)
		return p.ID, err
	}, true)
}

// ListPlans returns all plans, ordered by name.
func (s *SQLite) ListPlans(ctx context.Context) ([]model.Plan, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, rps, burst, daily_quota FROM plans ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPlan fetches a plan by id.
func (s *SQLite) GetPlan(ctx context.Context, id int64) (model.Plan, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, rps, burst, daily_quota FROM plans WHERE id = ?`, id)
	p, err := scanPlan(row)
	if err == sql.ErrNoRows {
		return model.Plan{}, false, nil
	}
	return p, err == nil, err
}

// DeletePlan removes a plan by id.
func (s *SQLite) DeletePlan(ctx context.Context, id int64) (bool, error) {
	return s.deleteByID(ctx, `DELETE FROM plans WHERE id = ?`, id)
}

// --- consumers ---

// UpsertConsumer inserts (ID == 0) or updates a consumer, and returns its id.
func (s *SQLite) UpsertConsumer(ctx context.Context, c model.Consumer) (int64, error) {
	return s.writeID(ctx, func(tx *sql.Tx) (int64, error) {
		now := nowStamp()
		if c.ID == 0 {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO consumers (name, plan_id, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
				c.Name, nullInt64(c.PlanID), boolToInt(c.Enabled), now, now)
			if err != nil {
				return 0, err
			}
			return res.LastInsertId()
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE consumers SET name = ?, plan_id = ?, enabled = ?, updated_at = ? WHERE id = ?`,
			c.Name, nullInt64(c.PlanID), boolToInt(c.Enabled), now, c.ID)
		return c.ID, err
	}, true)
}

// ListConsumers returns all consumers, ordered by name.
func (s *SQLite) ListConsumers(ctx context.Context) ([]model.Consumer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, plan_id, enabled FROM consumers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Consumer
	for rows.Next() {
		c, err := scanConsumer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConsumer fetches a consumer by id.
func (s *SQLite) GetConsumer(ctx context.Context, id int64) (model.Consumer, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, plan_id, enabled FROM consumers WHERE id = ?`, id)
	c, err := scanConsumer(row)
	if err == sql.ErrNoRows {
		return model.Consumer{}, false, nil
	}
	return c, err == nil, err
}

// DeleteConsumer removes a consumer (and cascades its keys) by id.
func (s *SQLite) DeleteConsumer(ctx context.Context, id int64) (bool, error) {
	return s.deleteByID(ctx, `DELETE FROM consumers WHERE id = ?`, id)
}

// --- api keys ---

// CreateAPIKey stores a key (by hash) for a consumer and returns its id.
func (s *SQLite) CreateAPIKey(ctx context.Context, consumerID int64, name, keyHash string) (int64, error) {
	return s.writeID(ctx, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO api_keys (consumer_id, name, key_hash, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
			consumerID, name, keyHash, nowStamp())
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}, true)
}

// ListConsumerKeys returns a consumer's keys as metadata only (never the hash).
func (s *SQLite) ListConsumerKeys(ctx context.Context, consumerID int64) ([]model.APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, consumer_id, name, enabled, revoked_at FROM api_keys WHERE consumer_id = ? ORDER BY id`, consumerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.APIKey
	for rows.Next() {
		var (
			k       model.APIKey
			enabled int
			revoked sql.NullString
		)
		if err := rows.Scan(&k.ID, &k.ConsumerID, &k.Name, &enabled, &revoked); err != nil {
			return nil, err
		}
		k.Enabled = enabled != 0
		k.Revoked = revoked.Valid
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeAPIKey disables a key by id; the version bumps only when one was revoked.
func (s *SQLite) RevokeAPIKey(ctx context.Context, id int64) (bool, error) {
	return s.deleteByID(ctx,
		`UPDATE api_keys SET enabled = 0, revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ? AND revoked_at IS NULL`, id)
}

// ResolveAPIKey maps a key hash to its consumer, ignoring revoked/disabled keys
// and disabled consumers. This is the data-plane hot-path lookup.
func (s *SQLite) ResolveAPIKey(ctx context.Context, keyHash string) (model.Consumer, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT c.id, c.name, c.plan_id, c.enabled
FROM api_keys k JOIN consumers c ON c.id = k.consumer_id
WHERE k.key_hash = ? AND k.enabled = 1 AND k.revoked_at IS NULL AND c.enabled = 1`, keyHash)
	c, err := scanConsumer(row)
	if err == sql.ErrNoRows {
		return model.Consumer{}, false, nil
	}
	return c, err == nil, err
}

// --- admin users (do NOT bump config_version: not data-plane config) ---

// GetAdminUser fetches an admin by username.
func (s *SQLite) GetAdminUser(ctx context.Context, username string) (model.AdminUser, bool, error) {
	var u model.AdminUser
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, token_version FROM admin_users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.TokenVersion)
	if err == sql.ErrNoRows {
		return model.AdminUser{}, false, nil
	}
	return u, err == nil, err
}

// ListAdminUsers returns admin metadata (no password hashes), ordered by username.
func (s *SQLite) ListAdminUsers(ctx context.Context) ([]model.AdminUser, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, token_version FROM admin_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.AdminUser
	for rows.Next() {
		var u model.AdminUser
		if err := rows.Scan(&u.ID, &u.Username, &u.TokenVersion); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpsertAdminUser inserts (ID == 0) or updates an admin and returns its id.
func (s *SQLite) UpsertAdminUser(ctx context.Context, u model.AdminUser) (int64, error) {
	if u.ID == 0 {
		tv := u.TokenVersion
		if tv == 0 {
			tv = 1
		}
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO admin_users (username, password_hash, token_version, created_at) VALUES (?, ?, ?, ?)`,
			u.Username, u.PasswordHash, tv, nowStamp())
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_users SET username = ?, password_hash = ?, token_version = ? WHERE id = ?`,
		u.Username, u.PasswordHash, u.TokenVersion, u.ID)
	return u.ID, err
}

// SeedAdminUser inserts a bootstrap admin only when no admin exists yet. Returns
// whether it inserted one.
func SeedAdminUser(ctx context.Context, s Store, u model.AdminUser) (bool, error) {
	admins, err := s.ListAdminUsers(ctx)
	if err != nil {
		return false, err
	}
	if len(admins) > 0 {
		return false, nil
	}
	if _, err := s.UpsertAdminUser(ctx, u); err != nil {
		return false, err
	}
	return true, nil
}

// --- shared helpers ---

// writeID runs fn in a transaction, optionally bumping the config version, and
// returns fn's id.
func (s *SQLite) writeID(ctx context.Context, fn func(*sql.Tx) (int64, error), bump bool) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := fn(tx)
	if err != nil {
		return 0, err
	}
	if bump {
		if err := bumpVersion(ctx, tx); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// deleteByID runs a delete/disable statement and bumps the version only when a
// row changed.
func (s *SQLite) deleteByID(ctx context.Context, query string, id int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, query, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := bumpVersion(ctx, tx); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPlan(sc rowScanner) (model.Plan, error) {
	var (
		p     model.Plan
		quota sql.NullInt64
	)
	if err := sc.Scan(&p.ID, &p.Name, &p.RPS, &p.Burst, &quota); err != nil {
		return model.Plan{}, err
	}
	p.DailyQuota = int(quota.Int64)
	return p, nil
}

func scanConsumer(sc rowScanner) (model.Consumer, error) {
	var (
		c       model.Consumer
		planID  sql.NullInt64
		enabled int
	)
	if err := sc.Scan(&c.ID, &c.Name, &planID, &enabled); err != nil {
		return model.Consumer{}, err
	}
	c.PlanID = planID.Int64
	c.Enabled = enabled != 0
	return c, nil
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
