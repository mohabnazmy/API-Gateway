# API Gateway — Technical Design

**Status:** Draft · **Owner:** mohabnazmy · **Last updated:** 2026-06-16 · **Language:** Go

---

## 1. Overview

A lightweight, self-hostable **API Gateway** written in Go. It sits in front of
one or more backend services and provides a single ingress point that handles
cross-cutting concerns — routing, authentication, rate limiting, and
observability — so that backend services don't have to.

```
                 ┌───────────────────────────────────────────┐
   clients  ───► │                API Gateway                 │ ───►  upstream A
  (web, app,     │  routing · auth · rate-limit · observ.     │ ───►  upstream B
   service)      │                                            │ ───►  upstream C
                 └───────────────────────────────────────────┘
```

### Goals

- **Single ingress** for heterogeneous backends with path-prefix routing.
- **Authentication** at the edge via JWT (HS256/384/512) and/or static API keys.
- **Rate limiting** per client to protect upstreams from abuse and overload.
- **Observability**: structured logs, Prometheus metrics, request correlation IDs.
- **Operational simplicity**: a single static binary, configured entirely from
  the environment, with graceful shutdown and health checks.

### Non-goals (initial release)

- Dynamic service discovery (Consul/etcd/Kubernetes) — routes are static config.
- Load balancing across multiple upstream replicas — one upstream URL per route.
- Request/response body transformation, aggregation, or GraphQL federation.
- TLS termination — expected to be handled by an upstream edge (LB/ingress).
- Persistent or distributed rate-limit state — limits are per-instance, in-memory.

These are tracked in [§13 Roadmap](#13-roadmap).

---

## 2. Requirements

### Functional

| ID | Requirement |
|----|-------------|
| F1 | Route incoming requests to a configured upstream by longest-matching path prefix. |
| F2 | Optionally strip the matched prefix before forwarding upstream. |
| F3 | Restrict a route to specific HTTP methods. |
| F4 | Enforce authentication on routes flagged `require_auth`. |
| F5 | Accept a Bearer JWT **or** an `X-API-Key` for authenticated routes. |
| F6 | Apply a per-client-IP request rate limit with burst allowance. |
| F7 | Expose `/healthz` (liveness) and a Prometheus metrics endpoint. |
| F8 | Attach/propagate an `X-Request-ID` to every request. |

### Non-functional

| ID | Requirement |
|----|-------------|
| N1 | Add < 1 ms of overhead per request at the median (excluding upstream time). |
| N2 | Survive upstream failures: return `502` without crashing; recover from panics. |
| N3 | Graceful shutdown — drain in-flight requests within a configurable timeout. |
| N4 | Bounded memory under churn — evict idle rate-limit state. |
| N5 | Zero-config startup (sane defaults); fail fast on invalid config. |
| N6 | Single static binary, no external runtime dependencies. |

---

## 3. Technology Choices

| Concern | Choice | Rationale |
|---------|--------|-----------|
| Language | Go 1.22+ | Strong stdlib `net/http`, easy static binaries, great concurrency. |
| Router | [`go-chi/chi`](https://github.com/go-chi/chi) | Idiomatic, `net/http`-native, first-class middleware chains — exactly the gateway's model. |
| Reverse proxy | `net/http/httputil.ReverseProxy` | Battle-tested stdlib proxy with streaming, `X-Forwarded-*`, and hooks. |
| JWT | [`golang-jwt/jwt/v5`](https://github.com/golang-jwt/jwt) | De-facto standard; explicit algorithm allow-listing. |
| Rate limiting | [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate) | Token-bucket limiter from the Go team. |
| Metrics | [`prometheus/client_golang`](https://github.com/prometheus/client_golang) | Standard metrics exposition. |
| Logging | `log/slog` (stdlib) | Structured logging without a third-party dependency. |
| Config | Environment variables | 12-factor; container-friendly. Route table carried as JSON in one var. |

---

## 4. High-Level Architecture

The gateway is a single process exposing one HTTP listener. Two classes of
endpoints share the listener:

1. **Operational endpoints** (`/healthz`, `/metrics`) — served directly, exempt
   from proxying, auth, and rate limiting.
2. **Proxied traffic** (everything else) — passes through the gateway middleware
   chain and is forwarded to an upstream.

```
                         ┌──────────────────────── chi.Router ────────────────────────┐
                         │                                                             │
  HTTP request  ───────► │  GET /healthz  ─────────────────────────► health handler   │
                         │  GET /metrics  ─────────────────────────► promhttp handler  │
                         │                                                             │
                         │  /*  ──► [middleware chain] ──► proxy.Engine.ServeHTTP       │
                         │                                      │                       │
                         └──────────────────────────────────────┼───────────────────────┘
                                                                ▼
                                                       httputil.ReverseProxy ──► upstream
```

### Package layout

```
gateway/
├── cmd/
│   └── gateway/
│       └── main.go            # entrypoint: config load, signal handling, graceful shutdown
├── internal/
│   ├── config/
│   │   └── config.go          # env → Config; validation; route-table JSON parsing
│   ├── proxy/
│   │   └── proxy.go           # route matching + httputil.ReverseProxy per route
│   ├── middleware/
│   │   ├── middleware.go      # responseRecorder, RequestID, shared helpers
│   │   ├── recover.go         # panic recovery
│   │   ├── logging.go         # structured per-request logging
│   │   ├── metrics.go         # Prometheus instrumentation
│   │   ├── auth.go            # JWT + API-key authentication
│   │   └── ratelimit.go       # per-IP token-bucket limiting
│   └── server/
│       └── server.go          # wires router + middleware + proxy
├── docs/
│   └── technical-design.md    # this document
├── go.mod
├── go.sum
└── README.md
```

**Why `internal/`?** Everything is implementation detail behind `cmd/gateway`.
Using `internal/` prevents accidental import by external modules and keeps the
public surface to the binary itself.

---

## 5. Request Lifecycle

Order in the middleware chain is **significant**. `Resolve` runs first so that
every downstream layer — logging, metrics, auth — can read the matched route
from the request context. Unmatched requests are *not* short-circuited at
resolution; they flow through (logged + metered) and the proxy engine emits the
final `404`.

```
request
  │
  ▼
┌─────────────┐  assigns X-Request-ID (honor inbound, else generate)
│ RequestID   │  → ctx + response header
└─────────────┘
  │
  ▼
┌─────────────┐  defer/recover panics → 500, process stays alive
│ Recover     │
└─────────────┘
  │
  ▼
┌─────────────┐  longest-prefix match → store *route in ctx (nil if no match)
│ Resolve     │
└─────────────┘
  │
  ▼
┌─────────────┐  structured access log on completion (route, status, latency)
│ Logging     │
└─────────────┘
  │
  ▼
┌─────────────┐  counters + latency histogram + in-flight gauge (labelled by route)
│ Metrics     │
└─────────────┘
  │
  ▼
┌─────────────┐  per-client-IP token bucket → 429 when exhausted
│ RateLimit   │
└─────────────┘
  │
  ▼
┌─────────────┐  if route.RequireAuth: validate JWT or X-API-Key → 401 on failure
│ Auth        │
└─────────────┘
  │
  ▼
┌─────────────┐  matched → ReverseProxy to upstream; unmatched → 404
│ proxy.Engine│
└─────────────┘
  │
  ▼
upstream  ──(stream response back through the chain)──►  client
```

### Design notes on ordering

- **RequestID before Recover** so panic logs carry the correlation ID.
- **Resolve before Logging/Metrics/Auth** so all three see the route. This is the
  key constraint that shaped the chain: middleware can only read context values
  set *upstream* of it, because `context.WithValue` propagates downward only.
- **RateLimit before Auth** so unauthenticated floods are shed cheaply, before
  spending CPU on signature verification.
- **Metrics counts unmatched traffic** under a `route="unmatched"` label, giving
  visibility into 404 storms.

---

## 6. Configuration Model

All configuration comes from the environment. Operational knobs are plain
scalars; the route table — the one inherently structured input — is carried as a
JSON array in `GATEWAY_ROUTES`.

| Variable | Default | Description |
|----------|---------|-------------|
| `GATEWAY_ADDR` | `:8080` | Listen address. |
| `GATEWAY_ROUTES` | `[]` | JSON array of route objects (see below). |
| `GATEWAY_JWT_SECRET` | — | HMAC secret for JWT validation. Empty disables JWT. |
| `GATEWAY_API_KEYS` | — | Comma-separated list of accepted API keys. |
| `GATEWAY_RATE_LIMIT_RPS` | `100` | Sustained requests/sec per client IP. |
| `GATEWAY_RATE_LIMIT_BURST` | `200` | Burst bucket size per client IP. |
| `GATEWAY_READ_TIMEOUT` | `15s` | HTTP server read timeout. |
| `GATEWAY_WRITE_TIMEOUT` | `30s` | HTTP server write timeout. |
| `GATEWAY_IDLE_TIMEOUT` | `60s` | Keep-alive idle timeout. |
| `GATEWAY_SHUTDOWN_TIMEOUT` | `15s` | Max drain time on shutdown. |
| `GATEWAY_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `GATEWAY_METRICS_PATH` | `/metrics` | Prometheus scrape path. |

### Route object

```jsonc
{
  "name":         "users-api",        // label for logs & metrics
  "path_prefix":  "/api/users",       // matched against request path (required)
  "upstream":     "http://users:9001",// backend base URL, scheme+host required
  "strip_prefix": true,               // remove path_prefix before forwarding
  "require_auth": true,               // gate behind JWT/API-key
  "methods":      ["GET","POST"]      // optional method allow-list
}
```

Example:

```bash
export GATEWAY_ROUTES='[
  {"name":"users","path_prefix":"/api/users","upstream":"http://localhost:9001","strip_prefix":true,"require_auth":true},
  {"name":"public","path_prefix":"/public","upstream":"http://localhost:9002","strip_prefix":false}
]'
```

### Validation (fail-fast at startup)

- `path_prefix` is required and must begin with `/`.
- `upstream` is required and must parse to a URL with scheme **and** host.
- A route with `require_auth: true` requires at least one configured credential
  source (`GATEWAY_JWT_SECRET` or `GATEWAY_API_KEYS`); otherwise startup aborts.

---

## 7. Routing & Reverse Proxy

### Matching

Routes are sorted **once at startup** by descending `path_prefix` length, so the
first match is always the longest (most specific) prefix. Matching respects
path-segment boundaries: `/api` matches `/api` and `/api/x` but **not** `/apiv2`.
If a route declares `methods`, the request method must be in the list or the
route is skipped (allowing a different route to claim the path).

### Forwarding

Each route owns one `httputil.ReverseProxy` built around its upstream URL, using
the `Rewrite` hook (Go 1.20+):

- Sets scheme/host to the upstream.
- Strips `path_prefix` when `strip_prefix` is true.
- Sets `X-Forwarded-For` / `-Host` / `-Proto` via `SetXForwarded()`.

An `ErrorHandler` logs upstream failures (DNS, refused connection, timeout) with
the route name and returns **`502 Bad Gateway`** — the gateway never crashes on
upstream errors. The `responseRecorder` implements `http.Flusher`, so streaming
and Server-Sent Events upstreams pass through unbuffered.

---

## 8. Authentication

Applied only to routes with `require_auth: true`; all others pass through. A
request is authorized if **either** credential validates:

- **API key** — `X-API-Key` header matched against the in-memory key set
  (`GATEWAY_API_KEYS`). O(1) lookup.
- **JWT** — `Authorization: Bearer <token>`, validated against
  `GATEWAY_JWT_SECRET`. The signing algorithm is **explicitly allow-listed** to
  `HS256/384/512` to defend against `alg: none` and algorithm-confusion attacks.

Failure returns **`401`** with a `WWW-Authenticate` header. The gateway only
*authenticates* (verifies signature/validity); fine-grained authorization
(scopes/roles/claims enforcement) is upstream's responsibility in this release —
see [§13 Roadmap](#13-roadmap).

> **Security note:** API keys and JWT secrets are read from the environment. In
> production these should be injected from a secrets manager, never baked into
> images or committed. The secret comparison for JWT is handled by the library;
> API-key lookup is a map hit (constant-time comparison is a roadmap item if
> timing side-channels become a concern).

---

## 9. Rate Limiting

A **token-bucket per client IP** (`golang.org/x/time/rate`):

- Sustained rate `GATEWAY_RATE_LIMIT_RPS`, burst `GATEWAY_RATE_LIMIT_BURST`.
- Client identity = `X-Forwarded-For` (first hop) when present, else
  `RemoteAddr`. **Assumes the gateway is the edge or sits behind a trusted proxy
  that sets XFF** — otherwise clients can spoof it. Trust boundary is documented
  for operators.
- Limiters are held in a `map[ip]*visitor` guarded by a mutex; a background
  sweeper evicts visitors idle > 3 minutes, bounding memory (N4).
- Over-limit requests get **`429 Too Many Requests`**.

**Limitation:** state is per-instance. Running N replicas yields ~N× the
effective global limit. Distributed limiting (Redis) is a roadmap item.

---

## 10. Observability

### Logging
`log/slog` JSON to stdout. One structured line per request:
`request_id, method, path, status, bytes, duration_ms, remote`. Level via
`GATEWAY_LOG_LEVEL`. Panics and upstream errors log at `error`.

### Metrics
Prometheus on `GATEWAY_METRICS_PATH`, exposed on a **dedicated registry**
(self-contained, testable):

| Metric | Type | Labels |
|--------|------|--------|
| `gateway_requests_total` | counter | `route`, `method`, `status` |
| `gateway_request_duration_seconds` | histogram | `route`, `method`, `status` |
| `gateway_requests_in_flight` | gauge | `route` |

Plus default Go runtime collectors (goroutines, GC, memory).

### Correlation
`X-Request-ID` is honored from inbound requests or generated (8 random bytes,
hex). Echoed in the response header and attached to every log line, enabling
end-to-end tracing across the gateway and upstreams that propagate it.

### Health
`GET /healthz` → `200 {"status":"ok"}`. Liveness only; readiness/upstream health
checks are a roadmap item.

---

## 11. Error Handling & Resilience

| Condition | Response | Behavior |
|-----------|----------|----------|
| No route matches path/method | `404` | Logged + metered under `route="unmatched"`. |
| Auth required and missing/invalid | `401` | `WWW-Authenticate` header set. |
| Rate limit exceeded | `429` | Cheap rejection before auth. |
| Upstream unreachable/errors | `502` | `ErrorHandler` logs route + cause. |
| Handler panic | `500` | `Recover` logs stack context; process survives. |
| Invalid config at startup | exit 1 | Fail fast with a descriptive error. |
| SIGINT/SIGTERM | graceful | Drain in-flight within `SHUTDOWN_TIMEOUT`. |

---

## 12. Deployment

- **Artifact:** single static binary (`CGO_ENABLED=0 go build`), shipped in a
  `scratch`/`distroless` image.
- **Config:** environment variables (12-factor); secrets injected at runtime.
- **Scaling:** stateless — scale horizontally behind an L4/L7 load balancer.
  (Note the per-instance rate-limit caveat in §9.)
- **Probes:** liveness → `/healthz`; metrics scraped from `/metrics`.
- **TLS:** terminated by the upstream edge; the gateway speaks plain HTTP inside
  the trust boundary.

---

## 13. Roadmap

Ordered roughly by value:

1. **Per-route rate limits** and limit-by-API-key/JWT-subject (not just IP).
2. **Distributed rate limiting** via Redis for correct multi-replica limits.
3. **Upstream load balancing** (multiple targets, round-robin / least-conn) and
   active **health checks** with passive ejection.
4. **Authorization** — claim/scope/role enforcement, not just authentication.
5. **Dynamic configuration** — hot-reload routes (file watch or control API)
   without restart; service discovery integration.
6. **Distributed tracing** — OpenTelemetry spans propagated to upstreams.
7. **Resilience** — circuit breakers, retries with budgets, outlier detection.
8. **TLS termination** and mTLS to upstreams as an option.
9. **Request/response transforms** — header injection, body rewriting.

---

## 14. Open Questions

- **Config source:** stay env-only, or add an optional config *file* once the
  route table grows beyond what's comfortable in a single JSON env var?
- **Trusted-proxy model:** should XFF parsing be gated behind an explicit
  trusted-CIDR allow-list to prevent spoofed client IPs?
- **Auth extensibility:** is HMAC-only JWT sufficient, or do we need RS256/JWKS
  (asymmetric, key-rotation) in the first GA?
- **Multi-tenancy:** do limits/auth need to be scoped per tenant/consumer?
```

