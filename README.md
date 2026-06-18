# API Gateway

A self-hostable API Gateway in Go. It fronts your backend services with
path-prefix routing, authentication, rate limiting, and observability.

See [`docs/technical-design.md`](docs/technical-design.md) for the full design
(including the planned control plane: SQLite store, REST admin API, and React UI)
and [`docs/concepts-and-auth-decisions.md`](docs/concepts-and-auth-decisions.md)
for a plain-language guide to the concepts and auth decisions.

> **Status — Phase 1 (data plane).** Reverse-proxy routing, JWT/API-key auth,
> per-route rate limiting, and observability are implemented. The config store,
> admin API, and UI (Phases 2–4) are not yet built; routes are loaded once at
> startup from `GATEWAY_ROUTES`.

## Quick start

The fastest way to start is from the example env file:

```bash
go build -o gateway ./cmd/gateway
cp .env.example .env          # then edit secrets/routes
set -a; source .env; set +a   # load into the shell
./gateway
```

Or configure inline:

```bash
# Build
go build -o gateway ./cmd/gateway

# Configure a route and a credential, then run
export GATEWAY_PROXY_ADDR=:8080
export GATEWAY_API_KEYS=secret123
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
curl -i -H "X-API-Key: secret123" localhost:8080/api/users/42
```

## Configuration (Phase 1)

Bootstrap configuration comes from environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `GATEWAY_PROXY_ADDR` | `:8080` | Listen address. |
| `GATEWAY_ROUTES` | `[]` | JSON array of routes (see below). |
| `GATEWAY_JWT_SECRET` | — | HMAC secret for Bearer-JWT validation. |
| `GATEWAY_API_KEYS` | — | Comma-separated accepted API keys. |
| `GATEWAY_RATE_LIMIT_RPS` | `100` | Default per-client rate (routes may override). |
| `GATEWAY_RATE_LIMIT_BURST` | `200` | Default burst (routes may override). |
| `GATEWAY_READ_TIMEOUT` / `_WRITE_TIMEOUT` / `_IDLE_TIMEOUT` | `15s` / `30s` / `60s` | HTTP server timeouts. |
| `GATEWAY_SHUTDOWN_TIMEOUT` | `15s` | Graceful-shutdown drain limit. |
| `GATEWAY_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `GATEWAY_METRICS_PATH` | `/metrics` | Prometheus scrape path. |

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
| `rate_limit.algorithm` | string | `token_bucket` (Phase 1). |
| `rate_limit.rps` / `.burst` | number | Sustained rate and burst. `rps <= 0` disables limiting. |

## Endpoints

- `GET /healthz` — liveness (`200 {"status":"ok"}`).
- `GET /metrics` — Prometheus metrics.
- everything else — proxied to the matching route's upstream (or `404`).

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

## Architecture (Phase 1)

```
cmd/gateway        entrypoint: load config, build registry, serve
internal/model     shared config types (Route, AuthPolicy, RateLimitPolicy)
internal/config    bootstrap config from env
internal/registry  live config snapshot, atomically swappable (hot-reload basis)
internal/proxy     route matching + reverse proxy (the data-plane core)
internal/ratelimit pluggable rate-limit algorithms behind a Limiter interface
internal/middleware request ID, recover, logging, metrics, auth, rate limit
internal/server    wires the middleware chain + operational endpoints
```

License: see [LICENSE](LICENSE).
