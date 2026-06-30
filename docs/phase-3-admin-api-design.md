# Design: Phase 3 — Admin API, consumers/plans/keys, admin auth

**Status:** approved — building as 4 increments · **Decisions:** D1 four PRs ·
D2 **store-only API keys** (no env-key fallback) · D3 admin bind `127.0.0.1:9000`
· D4 reload on write + poller · D5 bcrypt. · **Implements:**
technical-design.md §8a (consumers/plans), §13 (Admin API), §15 (admin auth),
plus the deferred §11 tables. Built **TDD**.

## Goal

Add the **control plane**: a private REST Admin API to manage routes, plans,
consumers, and API keys *live* (hot-reloaded, no restart), protected by admin
auth — and wire the data plane to resolve an **API key → consumer → plan** so
rate limits key on the customer, not the client IP. This is where API keys and
per-customer limits become first-class (the things deferred from Phases 1–2).

## Scope

In scope:
- Store: migration v2 (`plans`, `consumers`, `api_keys`, `admin_users`) + repos.
- A **private admin HTTP listener** (`:9000`), separate from the public proxy.
- **Admin auth**: bootstrap admin from env, `POST /login` → stateless JWT, JWT
  middleware guarding `/admin/api/*`.
- **REST CRUD** for routes, plans, consumers, API keys (§13), each write
  validated, transactional, and triggering hot-reload.
- **Data-plane consumer integration**: API-key auth resolves the consumer and
  the limiter keys on the consumer's plan.

Out of scope (later): the admin **UI** (Phase 4, HTMX), admin **RBAC** + audit
log, JWT-based consumer identity, daily-quota accounting (roadmap §20).

## Proposed increments (each its own PR, TDD)

Phase 3 is large; I propose four shippable slices rather than one PR:

- **3a — Store v2 + repositories.** Migration for the four tables; plan/consumer/
  api-key repos (key stored as **SHA-256 hash**, plaintext returned once);
  bootstrap-admin seeding. No HTTP yet. *Foundation; nothing user-visible.*
- **3b — Admin server + auth.** Private listener, login → admin JWT, auth
  middleware. One real endpoint (`/admin/api/health`) to prove the chain.
- **3c — Admin REST CRUD.** Handlers for routes/plans/consumers/api-keys with
  validation; writes bump `config_version` → hot-reload. This makes the gateway
  fully manageable over HTTP.
- **3d — Data-plane consumer binding.** API-key auth resolves key→consumer→plan;
  rate limiter keys on the consumer (falls back to client-IP when anonymous).

Recommended order: 3a → 3b → 3c → 3d. (3d can precede 3c if we want
consumer-limited keys working before full CRUD, but CRUD is how keys get created,
so 3c-then-3d is the natural path.)

## Schema additions (migration `0002`)

The four tables from §11 (`plans`, `consumers`, `api_keys`, `admin_users`) exactly
as documented. Notes:
- `api_keys.key_hash` = **SHA-256 of the key**; plaintext is never stored, shown
  once at creation.
- `consumers.plan_id` → `plans.id`; a consumer's effective limit comes from its
  plan.
- `admin_users.password_hash` = **bcrypt**; `token_version` for future revocation.
- Every write here also bumps `config_version` (consumers/keys affect the data
  plane), so the existing reloader picks changes up.

## Repository additions (`internal/store`)

Extend the `Store` interface (still one interface, SQLite behind it):

```go
// plans / consumers
ListPlans(ctx) ([]model.Plan, error)
UpsertPlan(ctx, model.Plan) (int64, error)
DeletePlan(ctx, id int64) (bool, error)
ListConsumers(ctx) ([]model.Consumer, error)
GetConsumer(ctx, id int64) (model.Consumer, error)
UpsertConsumer(ctx, model.Consumer) (int64, error)
DeleteConsumer(ctx, id int64) (bool, error)
// api keys (hashed)
ListConsumerKeys(ctx, consumerID int64) ([]model.APIKey, error) // metadata only
CreateAPIKey(ctx, consumerID int64, name, keyHash string) (int64, error)
RevokeAPIKey(ctx, id int64) (bool, error)
ResolveAPIKey(ctx, keyHash string) (model.Consumer, bool, error) // data-plane lookup
// admin users
GetAdminUser(ctx, username string) (model.AdminUser, bool, error)
UpsertAdminUser(ctx, model.AdminUser) error
```

New `model` types: `Plan`, `Consumer`, `APIKey` (metadata), `AdminUser`. `ResolveAPIKey`
is the hot path used by the data plane; it can be cached in the snapshot later.

## Admin server & auth (3b)

- New `internal/admin` package: a chi router mounted on a **second
  `http.Server`** bound to `GATEWAY_ADMIN_ADDR` (default `127.0.0.1:9000` —
  private by default; operators expose it via VPN/bastion/ingress).
- **Bootstrap:** on first run, if `admin_users` is empty and
  `GATEWAY_ADMIN_USER`/`GATEWAY_ADMIN_PASSWORD` are set, seed one bcrypt admin.
- **Login:** `POST /admin/api/auth/login` verifies the password, returns a
  short-lived **HS256 JWT** signed with `GATEWAY_ADMIN_JWT_SECRET` (separate from
  the data-plane JWT secret).
- **Middleware:** validate the admin JWT (signature + exp + `token_version`) on
  every `/admin/api/*` route except login/health.
- `main` runs both servers and shuts them down together on SIGINT/SIGTERM.

## Data-plane consumer binding (3d)

- API-key auth (`middleware/auth.go`) changes from "is this key in the env
  allow-list?" to "hash the key, `ResolveAPIKey` → consumer". On success the
  consumer (+ plan limits) is stored in the request context.
- The rate-limit middleware keys on the **consumer** when present (using the
  plan's rps/burst), else falls back to the route limit keyed by client IP (§9).
- **D2 — store-only keys (breaking, intentional):** `GATEWAY_API_KEYS` is
  **removed** in 3d; every API key lives in the store and belongs to a consumer.
  Until 3d lands, env keys keep working (Phases 1–2 behavior); 3d is the cutover.
  Operators must create keys via the Admin API before relying on api-key routes.

## Validation & hot-reload

Every mutating handler validates before persisting (path-prefix shape, upstream
URL, known algorithm, referenced secret exists, plan exists for a consumer),
writes in a transaction, bumps `config_version`. With `GATEWAY_CONFIG_POLL_INTERVAL`
set, the Phase 2 reloader applies it; **3c also calls `registry`/reloader
directly** on write for instant apply in the single-node case (§12).

## Config additions

| Variable | Default | Description |
|----------|---------|-------------|
| `GATEWAY_ADMIN_ADDR` | `127.0.0.1:9000` | Private admin listener. Keep off the public network. |
| `GATEWAY_ADMIN_JWT_SECRET` | — | HS256 secret for admin **session** tokens (≠ data-plane JWT). |
| `GATEWAY_ADMIN_USER` / `GATEWAY_ADMIN_PASSWORD` | — | First-run bootstrap admin. |
| `GATEWAY_ADMIN_TOKEN_TTL` | `30m` | Admin session token lifetime. |

## Decisions that want your review

- **D1 — Increments.** ✅ Four PRs (3a→3d), each shippable.
- **D2 — Env API keys.** ✅ **Store-only.** `GATEWAY_API_KEYS` removed at 3d; all
  keys live in the store, owned by a consumer.
- **D3 — Admin listener default bind.** ✅ `127.0.0.1:9000` (private by default).
- **D4 — Hot-reload on write.** ✅ Direct reload after each admin write, plus the
  version poller.
- **D5 — Password hash.** ✅ bcrypt (`golang.org/x/crypto/bcrypt`).

## TDD plan

Per increment, tests first:
- **3a** store: plan/consumer/key CRUD round-trips; `CreateAPIKey` stores only the
  hash; `ResolveAPIKey` matches by hash and ignores revoked/disabled; bootstrap
  admin seeded once; version bumps.
- **3b** auth: login rejects bad creds; issues a verifiable JWT; middleware allows
  valid / rejects missing-expired-tampered tokens; admin listener separate from
  proxy.
- **3c** handlers: each endpoint's happy path + validation failures (422) +
  not-found (404) + unauth (401); a write triggers reload.
- **3d** data plane: API-key request resolves the consumer; limiter keys on the
  consumer's plan; anonymous/env key falls back to IP keying.

## Non-goals

- Admin UI (Phase 4), RBAC, audit log, JWT-based consumer identity, distributed
  rate limiting — all roadmap (§20).
