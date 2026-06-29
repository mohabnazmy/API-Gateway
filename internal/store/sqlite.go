package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; keeps CGO_ENABLED=0 static builds working

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// SQLite is the embedded, single-node Store implementation.
type SQLite struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and runs migrations.
func Open(path string) (*SQLite, error) {
	// foreign_keys(1) makes ON DELETE CASCADE work; busy_timeout avoids spurious
	// SQLITE_BUSY under brief contention.
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite serializes writes; a single connection sidesteps lock contention and
	// is ample for config-scale write volume.
	db.SetMaxOpenConns(1)

	s := &SQLite{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *SQLite) Close() error { return s.db.Close() }

type migration struct {
	version int
	sql     string
}

// migrations are applied in order; never edit an applied one — add a new entry.
var migrations = []migration{
	{1, `
CREATE TABLE routes (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  path_prefix   TEXT NOT NULL,
  upstream      TEXT NOT NULL,
  strip_prefix  INTEGER NOT NULL DEFAULT 0,
  methods       TEXT,                       -- JSON array, nullable
  upstream_auth TEXT,                       -- JSON object, nullable
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE TABLE auth_policies (
  route_id      INTEGER NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
  require_auth  INTEGER NOT NULL DEFAULT 0,
  methods       TEXT                        -- JSON array, nullable (null = any)
);
CREATE TABLE rate_limit_policies (
  route_id      INTEGER NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
  algorithm     TEXT NOT NULL DEFAULT 'token_bucket',
  rps           REAL NOT NULL,
  burst         INTEGER NOT NULL,
  window_sec    INTEGER
);
CREATE TABLE config_version (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  version       INTEGER NOT NULL
);
INSERT INTO config_version (id, version) VALUES (1, 0);
`},
}

func (s *SQLite) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}
	var cur int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&cur); err != nil {
		return fmt.Errorf("migrate version: %w", err)
	}
	for _, m := range migrations {
		if m.version <= cur {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, m.version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Version returns the current config version.
func (s *SQLite) Version(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT version FROM config_version WHERE id = 1`).Scan(&v)
	return v, err
}

// UpsertRoute inserts or updates a route (by name) and its policies, then bumps
// the config version — all in one transaction.
func (s *SQLite) UpsertRoute(ctx context.Context, r model.Route) error {
	methods, err := marshalSlice(r.Methods)
	if err != nil {
		return err
	}
	upstreamAuth, err := marshalUpstreamAuth(r.UpstreamAuth)
	if err != nil {
		return err
	}
	authMethods, err := marshalSlice(r.Auth.Methods)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO routes (name, path_prefix, upstream, strip_prefix, methods, upstream_auth, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT(name) DO UPDATE SET
  path_prefix   = excluded.path_prefix,
  upstream      = excluded.upstream,
  strip_prefix  = excluded.strip_prefix,
  methods       = excluded.methods,
  upstream_auth = excluded.upstream_auth,
  enabled       = 1,
  updated_at    = excluded.updated_at`,
		r.Name, r.PathPrefix, r.Upstream, boolToInt(r.StripPrefix), methods, upstreamAuth, now, now); err != nil {
		return fmt.Errorf("upsert route %q: %w", r.Name, err)
	}

	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM routes WHERE name = ?`, r.Name).Scan(&id); err != nil {
		return err
	}
	// Replace child policy rows so an update never leaves stale ones behind.
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_policies WHERE route_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rate_limit_policies WHERE route_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO auth_policies (route_id, require_auth, methods) VALUES (?, ?, ?)`,
		id, boolToInt(r.Auth.RequireAuth), authMethods); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO rate_limit_policies (route_id, algorithm, rps, burst, window_sec) VALUES (?, ?, ?, ?, ?)`,
		id, r.RateLimit.Algorithm, r.RateLimit.RPS, r.RateLimit.Burst, windowSec(r.RateLimit.WindowSec)); err != nil {
		return err
	}
	if err := bumpVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteRoute removes a route by name; child rows cascade. The version bumps only
// when a row was actually deleted.
func (s *SQLite) DeleteRoute(ctx context.Context, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE name = ?`, name)
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

// ListRoutes returns all enabled routes with their policies, ordered by name.
func (s *SQLite) ListRoutes(ctx context.Context) ([]model.Route, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.name, r.path_prefix, r.upstream, r.strip_prefix, r.methods, r.upstream_auth,
       a.require_auth, a.methods, rl.algorithm, rl.rps, rl.burst, rl.window_sec
FROM routes r
LEFT JOIN auth_policies a        ON a.route_id = r.id
LEFT JOIN rate_limit_policies rl ON rl.route_id = r.id
WHERE r.enabled = 1
ORDER BY r.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []model.Route
	for rows.Next() {
		var (
			r            model.Route
			stripPrefix  int
			methods      sql.NullString
			upstreamAuth sql.NullString
			requireAuth  sql.NullInt64
			authMethods  sql.NullString
			algorithm    sql.NullString
			rps          sql.NullFloat64
			burst        sql.NullInt64
			windowSecCol sql.NullInt64
		)
		if err := rows.Scan(&r.Name, &r.PathPrefix, &r.Upstream, &stripPrefix, &methods, &upstreamAuth,
			&requireAuth, &authMethods, &algorithm, &rps, &burst, &windowSecCol); err != nil {
			return nil, err
		}

		r.StripPrefix = stripPrefix != 0
		if r.Methods, err = unmarshalSlice(methods); err != nil {
			return nil, err
		}
		if r.UpstreamAuth, err = unmarshalUpstreamAuth(upstreamAuth); err != nil {
			return nil, err
		}
		r.Auth.RequireAuth = requireAuth.Valid && requireAuth.Int64 != 0
		if r.Auth.Methods, err = unmarshalSlice(authMethods); err != nil {
			return nil, err
		}
		r.RateLimit = model.RateLimitPolicy{
			Algorithm: algorithm.String,
			RPS:       rps.Float64,
			Burst:     int(burst.Int64),
			WindowSec: int(windowSecCol.Int64),
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func bumpVersion(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE config_version SET version = version + 1 WHERE id = 1`)
	return err
}

// --- value helpers: empty/disabled values round-trip as SQL NULL → zero value ---

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func windowSec(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func marshalSlice(v []string) (any, error) {
	if len(v) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func unmarshalSlice(ns sql.NullString) ([]string, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	var v []string
	if err := json.Unmarshal([]byte(ns.String), &v); err != nil {
		return nil, err
	}
	return v, nil
}

func marshalUpstreamAuth(u model.UpstreamAuth) (any, error) {
	if !u.Enabled() {
		return nil, nil
	}
	b, err := json.Marshal(u)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func unmarshalUpstreamAuth(ns sql.NullString) (model.UpstreamAuth, error) {
	var u model.UpstreamAuth
	if !ns.Valid || ns.String == "" {
		return u, nil
	}
	err := json.Unmarshal([]byte(ns.String), &u)
	return u, err
}
