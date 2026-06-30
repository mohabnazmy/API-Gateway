# API Gateway

A self-hostable API Gateway in Go. It fronts your backend services with
path-prefix routing, authentication, rate limiting, and observability.

Documentation:
- [`docs/architecture.md`](docs/architecture.md) — application architecture with diagrams.
- [`docs/technical-design.md`](docs/technical-design.md) — full design (incl. the planned control plane: SQLite store, REST admin API, Go-templates admin UI).
- [`docs/concepts-and-auth-decisions.md`](docs/concepts-and-auth-decisions.md) — plain-language guide to the concepts and auth decisions.
- [`docs/test-findings.md`](docs/test-findings.md) — adversarial edge-case test results.

> **Status — Phases 1–2.** Data plane (reverse-proxy routing, JWT/API-key auth,
> per-route rate limiting, per-route upstream auth, observability) **and** the
> SQLite config store with hot-reload are implemented. Routes are seeded from
> `GATEWAY_ROUTES` into the store on first run, then the store is authoritative.
> The admin API and UI (Phases 3–4) are not yet built.

## Quick start

The fastest way to start is from the example env file. The gateway
**auto-loads `.env`** from the working directory on startup — no sourcing
needed:

```bash
go build -o gateway ./cmd/gateway
cp .env.example .env          # then edit secrets/routes
./gateway                     # .env is loaded automatically
```

Point it at a different file with `GATEWAY_ENV_FILE=/path/to/file`. Real
environment variables always take precedence over the file, and a missing
`.env` is fine (values fall back to defaults / direct env vars).

Or configure inline:

```bash
# Build
go build -o gateway ./cmd/gateway

# Configure a route, then run
export GATEWAY_PROXY_ADDR=:8080
export GATEWAY_ROUTES='[
  {
    "name": "users",
    "path_prefix": "/api/users",
    "upstream": "http://localhost:9001",
    "strip_prefix": true,
    "auth": { "require_auth": true, "methods": ["api_key"] },
    "rate_limit": { "algorithm": "token_bucket", "rps": 100, "burst": 200 }
  }
]'
./gateway

# Try it
curl -i localhost:8080/healthz
```

API keys now live in the config store and belong to a **consumer** — create them
via the Admin API (`POST /admin/api/consumers/{id}/api-keys`), which returns the
plaintext once. A request authenticated by such a key is rate-limited by the
consumer's **plan**, keyed on the consumer. See [Admin API](#admin-api-control-plane).

```bash
curl -i -H "X-API-Key: gwk_…" localhost:8080/api/users/42
```

## Configuration (Phase 1)

Bootstrap configuration comes from environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `GATEWAY_PROXY_ADDR` | `:8080` | Listen address. |
| `GATEWAY_ROUTES` | `[]` | JSON array of routes (see below). |
| `GATEWAY_JWT_SECRET` | — | HMAC secret for Bearer-JWT validation. |
| `GATEWAY_RATE_LIMIT_RPS` | `100` | Default per-client rate (routes may override). |
| `GATEWAY_RATE_LIMIT_BURST` | `200` | Default burst (routes may override). |
| `GATEWAY_TRUSTED_PROXIES` | — | Comma-separated CIDRs/IPs whose `X-Forwarded-For` is trusted. Empty = trust none (XFF ignored). |
| `GATEWAY_UPSTREAM_DIAL_TIMEOUT` | `10s` | Connection dial timeout to upstreams. |
| `GATEWAY_UPSTREAM_RESPONSE_TIMEOUT` | `30s` | Max wait for upstream response headers. |
| `GATEWAY_READ_TIMEOUT` / `_WRITE_TIMEOUT` / `_IDLE_TIMEOUT` | `15s` / `30s` / `60s` | HTTP server timeouts. |
| `GATEWAY_SHUTDOWN_TIMEOUT` | `15s` | Graceful-shutdown drain limit. |
| `GATEWAY_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `GATEWAY_METRICS_PATH` | `/metrics` | Prometheus scrape path. |
| `GATEWAY_DB_PATH` | `./gateway.db` | SQLite config store. Seeded from `GATEWAY_ROUTES` on first run, then authoritative. Mount on a volume to persist. |
| `GATEWAY_CONFIG_POLL_INTERVAL` | `0` | When > 0 (e.g. `5s`), poll the store and hot-reload routes on change without a restart. `0` disables polling. |
| `GATEWAY_ADMIN_ADDR` | `127.0.0.1:9000` | Private admin-API listener (control plane). Keep off the public network. |
| `GATEWAY_ADMIN_JWT_SECRET` | — | HS256 secret for admin **session** tokens (separate from the data-plane JWT). The admin API starts only when this is set. |
| `GATEWAY_ADMIN_USER` / `GATEWAY_ADMIN_PASSWORD` | — | First-run bootstrap admin (seeded once; bcrypt-hashed). |
| `GATEWAY_ADMIN_TOKEN_TTL` | `30m` | Admin session-token lifetime. |

### Route object

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Label used in logs and metrics. |
| `path_prefix` | string | Matched against the request path (longest match wins). |
| `upstream` | string | Backend base URL (scheme + host required). |
| `strip_prefix` | bool | Remove `path_prefix` before forwarding. |
| `methods` | string[] | Optional HTTP method allow-list. |
| `auth.require_auth` | bool | Gate the route behind authentication. |
| `auth.methods` | string[] | Accepted credentials: `jwt`, `api_key` (empty = any configured). |
| `rate_limit.algorithm` | string | `token_bucket` (default), `leaky_bucket`, `fixed_window`, or `sliding_window`. |
| `rate_limit.rps` | number | Sustained requests/sec. `rps <= 0` disables limiting. |
| `rate_limit.burst` | number | Bucket capacity (token/leaky bucket). |
| `rate_limit.window_sec` | number | Window length for the window algorithms (default `1`); per-window limit = `rps × window_sec`. |
| `upstream_auth.type` | string | How the gateway authenticates to the upstream (see below); `none` (default). |

#### Upstream authentication

By default the gateway forwards requests to the upstream unauthenticated
(`upstream_auth.type: "none"`). Set a mode when the upstream itself requires the
gateway to authenticate. Secret-bearing fields end in `_ref` and accept
`env:NAME`, `file:/path`, or a literal value, so secrets stay out of route JSON.

| Mode | Behavior | Fields |
|------|----------|--------|
| `none` *(default)* | Forward as-is; no credentials attached. | — |
| `bearer` | Attach a static token/API key to a header. | `token_ref`; optional `header` (default `Authorization`), `scheme` (default `Bearer`; `none` = raw value) |
| `google_oidc` | Attach a Google-signed identity token (audience = upstream origin) as a `Bearer`, to call a **private Cloud Run** service. Requires running on GCP (metadata server reachable). | optional `audience` |
| `oauth2_client_credentials` | Fetch+cache a token via the OAuth2 client-credentials grant, attach as `Bearer`. Works with Auth0/Okta/Keycloak/Azure AD. | `token_url`, `client_id`, `client_secret_ref`; optional `scopes`, `audience` |
| `aws_sigv4` | Sign requests with AWS SigV4 to call private AWS targets (API Gateway, Lambda URLs). Credentials from the standard AWS chain (env/role). | `region`; optional `service` (default `execute-api`) |
| `mtls` | Present a client certificate at the transport layer (mutual TLS). | `cert_ref`, `key_ref` |

```jsonc
"upstream_auth": { "type": "oauth2_client_credentials",
  "token_url": "https://issuer/oauth/token",
  "client_id": "gw", "client_secret_ref": "env:OAUTH_SECRET",
  "scopes": ["api.read"] }
```

The legacy bare-string form (`"upstream_auth": "google_oidc"`) is still accepted.
See [`docs/upstream-auth-design.md`](docs/upstream-auth-design.md) for the design.

On a route with `upstream_auth` set, the gateway:
- **fails closed** — if it cannot mint/sign the credential, the request returns
  `502` and is never forwarded uncredentialed; and
- **strips the caller's inbound `Authorization` / `X-API-Key`** before forwarding,
  so the gateway credential the client used is not leaked to the upstream.

#### Rate-limit algorithms

| Algorithm | Behavior | Params used |
|-----------|----------|-------------|
| `token_bucket` *(default)* | Steady refill + burst allowance; tolerant of short spikes. | `rps`, `burst` |
| `leaky_bucket` | Constant drain rate; strict traffic shaping, no bursts. | `rps`, `burst` (capacity) |
| `fixed_window` | Count per fixed window; simplest, allows boundary bursts. | `rps`, `window_sec` |
| `sliding_window` | Rolling window; smooths the fixed-window boundary burst. | `rps`, `window_sec` |

#### Rate-limit response headers

Every response on a rate-limited route reports the client's consumption (the
`Limit` reflects the route's configured allowance):

| Header | Meaning |
|--------|---------|
| `RateLimit-Limit` / `X-RateLimit-Limit` | configured allowance (burst, or per-window limit) |
| `RateLimit-Remaining` / `X-RateLimit-Remaining` | remaining allowance for this client |
| `RateLimit-Reset` / `X-RateLimit-Reset` | seconds until the allowance replenishes |
| `Retry-After` | on `429` only — seconds the client should wait before retrying |

## Endpoints

Public proxy listener (`GATEWAY_PROXY_ADDR`, default `:8080`):
- `GET /healthz` — liveness (`200 {"status":"ok"}`).
- `GET /metrics` — Prometheus metrics.
- everything else — proxied to the matching route's upstream (or `404`).

### Admin API (control plane)

Private listener (`GATEWAY_ADMIN_ADDR`, default `127.0.0.1:9000`); starts only when
`GATEWAY_ADMIN_JWT_SECRET` is set. All `/admin/api/*` routes except `health` and
`auth/login` require a Bearer **admin session token** from login. Writes validate,
persist transactionally, and **hot-reload the data plane immediately**.

```
POST   /admin/api/auth/login                    → { "token": "..." }   (username/password)
GET    /admin/api/health
GET    /admin/api/me

GET    /admin/api/routes              POST   /admin/api/routes
GET    /admin/api/routes/{name}       PUT    /admin/api/routes/{name}    DELETE …/{name}

GET    /admin/api/plans               POST   /admin/api/plans
PUT    /admin/api/plans/{id}          DELETE /admin/api/plans/{id}

GET    /admin/api/consumers           POST   /admin/api/consumers
GET    /admin/api/consumers/{id}      PUT    /admin/api/consumers/{id}   DELETE …/{id}

GET    /admin/api/consumers/{id}/api-keys     # list (metadata only)
POST   /admin/api/consumers/{id}/api-keys     # issue → returns the key ONCE
DELETE /admin/api/api-keys/{id}               # revoke
```

API keys are stored as SHA-256 hashes; the plaintext is shown once at creation.
The admin UI (Phase 4) and consumer-keyed rate limiting (Phase 3d) build on this.

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

## Architecture

```
cmd/gateway          entrypoint: open store, seed, load registry, serve
internal/model       shared config types (Route, AuthPolicy, RateLimitPolicy)
internal/config      bootstrap config from env
internal/store       SQLite config store (durable source of truth) behind a repository interface
internal/configsync  loads the store into the registry; polls + hot-reloads on change
internal/registry    live config snapshot, atomically swappable (hot-reload basis)
internal/proxy       route matching + reverse proxy (the data-plane core)
internal/upstreamauth per-route upstream authentication (bearer/oidc/oauth2/sigv4/mtls)
internal/ratelimit   pluggable rate-limit algorithms behind a Limiter interface
internal/middleware  request ID, recover, logging, metrics, auth, rate limit
internal/server      wires the middleware chain + operational endpoints
```

License: see [LICENSE](LICENSE).
