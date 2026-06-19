# API Gateway — Technical Design

**Status:** Draft · **Owner:** mohabnazmy · **Last updated:** 2026-06-19 · **Language:** Go + React

---

## 1. Overview

A self-hostable **API Gateway** with a built-in **management control plane**. It
has two parts:

- **Data plane** — the gateway proper: it sits in front of backend services and
  handles routing, authentication, rate limiting, and observability for every
  request.
- **Control plane** — a config store, a REST **Admin API**, and a **React admin
  UI** for managing routes and policies *live*, without restarting or
  redeploying the gateway.

```
            ┌───────────────────── CONTROL PLANE ─────────────────────┐
            │                                                          │
            │   React Admin UI  ──►  Admin API (REST)  ──►  Config     │
            │   (operator login)     (authenticated)        Store      │
            │                                              (SQLite)     │
            └───────────────────────────────────────────────┬──────────┘
                                                            │ hot-reload
            ┌──────────────────────  DATA PLANE  ───────────▼──────────┐
   clients  │                       API Gateway                        │ ─► upstream A
  (web,app, │   routing · auth · rate-limit · observability            │ ─► upstream B
   service) │   live config snapshot, atomically swapped on change     │ ─► upstream C
            └──────────────────────────────────────────────────────────┘
```

### Goals

- **Single ingress** for heterogeneous backends with path-prefix routing.
- **Authentication** at the edge via JWT and/or API keys, configurable per route.
- **Rate limiting** per client with a **pluggable algorithm** chosen per route.
- **Observability**: structured logs, Prometheus metrics, request correlation IDs.
- **Live management**: create/edit/delete routes and policies through a UI and
  REST API; changes take effect via **hot-reload**, no restart.
- **Operational simplicity**: a single Go binary (with the UI embedded) and an
  embedded **SQLite** store — no external database or services to run.

### Non-goals (initial release)

- Multi-replica *shared* config — SQLite is embedded per node; horizontal scaling
  with shared config is a roadmap item (Postgres/etcd). See [§19](#19-roadmap).
- Distributed rate-limit state — limits are per-instance, in-memory.
- Upstream load balancing across replicas — one upstream URL per route.
- Request/response body transformation, aggregation, or GraphQL federation.
- TLS termination on the data plane — handled by an upstream edge (LB/ingress).

---

## 2. Architecture: The Two Planes

The single most important concept in this design is the **control-plane /
data-plane split**, borrowed from how production gateways (Kong, Envoy, Tyk) are
built.

| | Data plane | Control plane |
|---|---|---|
| **Job** | Proxy live traffic, fast | Manage configuration |
| **Talks to** | Clients ↔ upstreams | Operators (UI/API) |
| **Hot path?** | Yes — every request | No — only on config edits |
| **Listener** | Public proxy port (`:8080`) | Private admin port (`:9000`) |
| **State** | In-memory live snapshot | SQLite (durable source of truth) |

The two planes run in **one Go process** but on **two separate HTTP listeners**,
so the admin surface is never exposed on the public proxy port. They are
connected by a **registry**: an in-memory snapshot of the active config that the
data plane reads on every request, and that the control plane **atomically
swaps** whenever config changes (see [§11 Hot-Reload](#11-control-plane--hot-reload)).

```
  Admin API writes ──► SQLite ──► registry.Reload() ──► atomic pointer swap
                                                              │
  every proxied request ──► registry.Current() ──────────────┘ (lock-free read)
```

---

## 3. Requirements

### Functional — data plane

| ID | Requirement |
|----|-------------|
| F1 | Route requests to a configured upstream by longest-matching path prefix. |
| F2 | Optionally strip the matched prefix before forwarding upstream. |
| F3 | Restrict a route to specific HTTP methods. |
| F4 | Enforce authentication on routes that require it (per-route). |
| F5 | Accept a Bearer JWT and/or an `X-API-Key`, per the route's auth policy. |
| F6 | Apply a rate limit per client, with the **algorithm selectable per route**. |
| F7 | Expose `/healthz` (liveness) and a Prometheus metrics endpoint. |
| F8 | Attach/propagate an `X-Request-ID` to every request. |

### Functional — control plane

| ID | Requirement |
|----|-------------|
| C1 | Persist routes and policies durably in an embedded SQLite store. |
| C2 | Expose a REST Admin API for CRUD on routes, auth policies, rate-limit policies, and API keys. |
| C3 | Apply config changes to the running data plane via hot-reload (no restart). |
| C4 | Authenticate the Admin API/UI; never expose it on the public proxy port. |
| C5 | Manage API keys (create / list / revoke); store key **hashes**, never plaintext. |
| C6 | Serve a React admin UI for all of the above. |
| C7 | Model **consumers** (customers) and **plans** (tiers); each API key belongs to a consumer, and rate limits/quotas derive from the consumer's plan. |

### Non-functional

| ID | Requirement |
|----|-------------|
| N1 | < 1 ms data-plane overhead per request at the median (excl. upstream + lock-free config read). |
| N2 | Survive upstream failures (`502`) and handler panics (`500`) without crashing. |
| N3 | Graceful shutdown — drain in-flight requests within a configurable timeout. |
| N4 | Bounded memory under churn — evict idle rate-limit state. |
| N5 | Config edits are atomic and consistent; a bad edit never half-applies. |
| N6 | Single deploy artifact: one Go binary with the UI embedded; SQLite file on a volume. |

---

## 4. Technology Choices

| Concern | Choice | Rationale |
|---------|--------|-----------|
| Language (backend) | Go 1.22+ | Strong stdlib `net/http`, static binaries, concurrency. |
| Router | [`go-chi/chi`](https://github.com/go-chi/chi) | Idiomatic `net/http` middleware chains — for both data-plane and admin routers. |
| Reverse proxy | `net/http/httputil.ReverseProxy` | Battle-tested stdlib proxy with streaming + hooks. |
| JWT | [`golang-jwt/jwt/v5`](https://github.com/golang-jwt/jwt) | Standard; explicit algorithm allow-listing. |
| Rate limiting | [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate) + custom | Token bucket from the Go team; other algorithms behind an interface. |
| Metrics | [`prometheus/client_golang`](https://github.com/prometheus/client_golang) | Standard metrics exposition. |
| Logging | `log/slog` (stdlib) | Structured logging, no third-party dep. |
| **Config store** | **SQLite** (`modernc.org/sqlite`, pure-Go) | Embedded, zero external deps, durable. Pure-Go driver keeps `CGO_ENABLED=0` static builds. |
| **DB access** | `database/sql` + hand-written SQL (or `sqlc`) | Simple, explicit, testable. |
| **Admin UI** | **React SPA** (Vite + TypeScript) | Standard admin-dashboard UX. |
| **UI delivery** | Go `embed.FS` | Built React assets embedded into the binary → still one artifact. |
| Bootstrap config | Environment variables | Only for secrets, ports, and DB path (see [§16](#16-bootstrap-configuration)). |

---

## 5. Project Layout

```
gateway/
├── cmd/
│   └── gateway/
│       └── main.go            # bootstrap; start data-plane + admin servers; signals
├── internal/
│   ├── model/                 # shared config model: Route, AuthPolicy, RateLimitPolicy
│   ├── config/                # bootstrap config from env (ports, secrets, db path)
│   ├── store/                 # SQLite: schema, migrations, CRUD repositories
│   ├── registry/              # in-memory live snapshot + atomic hot-reload
│   ├── proxy/                 # DATA PLANE: route matching + httputil.ReverseProxy
│   ├── middleware/            # auth, ratelimit, logging, metrics, recover, requestid
│   ├── ratelimit/             # pluggable algorithms behind a Limiter interface
│   ├── admin/                 # CONTROL PLANE: REST handlers, admin auth, validation
│   └── server/                # wires the public proxy server + private admin server
├── web/                       # React admin UI (Vite + TS)
│   ├── src/…
│   └── dist/                  # build output, embedded via embed.FS
├── docs/
│   ├── technical-design.md
│   └── concepts-and-auth-decisions.md
├── go.mod
└── README.md
```

`internal/` keeps everything private to the binary. `model/` is shared by the
store, registry, data plane, and admin API so all four agree on one schema.

---

## 6. Data Plane — Request Lifecycle

Middleware order is **significant**. `Resolve` runs first so every downstream
layer — logging, metrics, auth — can read the matched route from context.
Unmatched requests flow through (logged + metered); the proxy emits the final
`404`. Every layer reads the active config from the **registry snapshot**, so a
hot-reload mid-flight never tears a request.

```
request
  ▼  RequestID   — honor/generate X-Request-ID → ctx + response header
  ▼  Recover     — recover panics → 500, process survives
  ▼  Resolve     — longest-prefix match against the live snapshot → route in ctx
  ▼  Logging     — structured access log on completion
  ▼  Metrics     — counters + latency histogram + in-flight gauge (per route)
  ▼  RateLimit   — per-route algorithm + limit → 429 when exhausted
  ▼  Auth        — if route requires it: validate JWT / API key → 401 on failure
  ▼  proxy       — matched → ReverseProxy to upstream; unmatched → 404
  ▼  upstream → stream response back through the chain → client
```

**Ordering rationale:** RequestID before Recover (panic logs carry the ID);
Resolve before Logging/Metrics/Auth (context propagates downward only, so the
route must be set first); RateLimit before Auth (shed floods before spending CPU
on signature verification).

---

## 7. Routing & Reverse Proxy

- **Matching:** the live snapshot keeps routes sorted by descending prefix length,
  so the first match is the longest (most specific). Matching respects
  path-segment boundaries: `/api` matches `/api` and `/api/x`, not `/apiv2`. A
  route may declare a method allow-list.
- **Forwarding:** each route owns one `httputil.ReverseProxy` (built when the
  snapshot is loaded), using the `Rewrite` hook to set the upstream scheme/host,
  strip the prefix when configured, and set `X-Forwarded-*`.
- **Resilience:** an `ErrorHandler` logs upstream failures and returns
  **`502`** — never crashes. The response recorder implements `http.Flusher`, so
  streaming / SSE upstreams pass through unbuffered.

---

## 8. Authentication

Per-route, driven by the route's **auth policy** in the store. A request passes
if it satisfies the policy's accepted methods:

- **API key** — `X-API-Key` matched against the store. Keys are stored as
  **hashes** (e.g. SHA-256); lookup hashes the presented key and compares. Keys
  are created/revoked via the Admin API/UI (C5). Each key **belongs to a
  consumer** (see [§8a](#8a-consumers--plans)), so a successful API-key auth
  resolves *which customer* is calling and puts that identity in the request
  context for downstream rate limiting and usage attribution.
- **JWT** — `Authorization: Bearer <token>`. Algorithm is **explicitly
  allow-listed** to defend against `alg: none` and RS→HS confusion attacks.
  - **HS256/384/512** (symmetric): secret injected via env, *referenced* from the
    route policy — never stored in SQLite in plaintext.
  - **RS256 + JWKS** (asymmetric, roadmap-ready): policy holds a `jwks_url`; the
    gateway fetches and caches the issuer's public keys.

Failure returns **`401`** with `WWW-Authenticate`. The gateway *authenticates*;
fine-grained authorization (scopes/roles) is a roadmap item, with coarse
per-route role/scope gates as the first step. See
[concepts-and-auth-decisions.md](./concepts-and-auth-decisions.md) for the full
rationale.

---

## 8a. Consumers & Plans

A production gateway serves many **customers**, each with their own credentials
and their own traffic allowance. The gateway models this with two entities:

- **Consumer** — a customer or application that calls the API. Owns **one or more
  API keys** (so keys can be rotated or scoped per environment without downtime)
  and is assigned a **plan**.
- **Plan** — a named tier (`free`, `pro`, `enterprise`, …) that sets the
  consumer's rate limit and quota *by volume*.

```
  API key  ─owned by─►  Consumer  ─assigned─►  Plan (rps / burst / daily quota)
```

**Identity flow:** on API-key auth the gateway resolves the key → its consumer →
the consumer's plan, and stores the consumer in the request context. Rate
limiting and usage metrics then key on the **consumer**, not the client IP, so
each customer gets their own bucket sized to their tier:

```
key "abc123"  → consumer "acme-corp"     → plan "enterprise" (5000 rps)
key "def456"  → consumer "small-startup" → plan "free"       (60 rps)
```

**Scope (decision):** consumers/plans are modeled for **API keys** in this
iteration. **JWT-based** consumer identity (deriving the consumer from a token
claim, or selecting a per-customer key by `kid`/JWKS) is deferred to the roadmap;
until then JWT auth uses the shared trusted secret and is not consumer-attributed.

Consumers, plans, and a consumer's keys are managed through the Admin API/UI
([§13](#13-control-plane--admin-api)).

---

## 9. Rate Limiting

A **`Limiter` interface** with the **algorithm chosen per route** (F6):

| Algorithm | Behavior | Good for |
|-----------|----------|----------|
| `token_bucket` *(default)* | Sustained rate + burst (`x/time/rate`). | General use; smooth bursts. |
| `fixed_window` | N requests per fixed window. | Simple quotas. |
| `sliding_window` | N requests over a rolling window. | Smoother than fixed; avoids edge bursts. |
| `leaky_bucket` | Constant drain rate. | Strict shaping. |

- **Keying:** when a request is attributed to a **consumer** (via API key, see
  [§8a](#8a-consumers--plans)), the limiter keys on the **consumer** and uses that
  consumer's **plan** limits — so each customer is throttled to their own tier.
  Otherwise (anonymous or JWT-only routes) it falls back to the route's limit
  keyed by **client IP**.
- Client IP from `X-Forwarded-For` (first hop) when present, else `RemoteAddr`.
  **Assumes the gateway is the edge or behind a trusted proxy** (XFF spoofing is
  the trade-off; a trusted-CIDR allow-list is an open question).
- Limiter state evicted when idle to bound memory (N4).
- Over-limit → **`429`**. (Daily quotas, where a plan sets one, also → `429`.)

**Limitation:** state is per-instance; N replicas ≈ N× the global limit.
Distributed limiting (Redis) is a roadmap item.

---

## 10. Observability

- **Logging** — `log/slog` JSON to stdout, one line per request: `request_id,
  method, path, status, bytes, duration_ms, remote, route`. Level via env.
- **Metrics** — Prometheus on a dedicated registry:
  `gateway_requests_total` (counter), `gateway_request_duration_seconds`
  (histogram), `gateway_requests_in_flight` (gauge) — labelled by `route`,
  `method`, `status` — plus Go runtime collectors.
- **Correlation** — `X-Request-ID` honored or generated, echoed in the response
  and attached to every log line.
- **Health** — `GET /healthz` → `200 {"status":"ok"}`.

---

## 11. Control Plane — Config Store (SQLite)

**SQLite is the durable source of truth.** Pure-Go driver
(`modernc.org/sqlite`) so static `CGO_ENABLED=0` builds keep working.

Schema (initial):

```sql
CREATE TABLE routes (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  path_prefix   TEXT NOT NULL,
  upstream      TEXT NOT NULL,
  strip_prefix  INTEGER NOT NULL DEFAULT 0,
  methods       TEXT,                       -- JSON array, nullable
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE auth_policies (
  route_id      INTEGER NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
  require_auth  INTEGER NOT NULL DEFAULT 0,
  methods       TEXT NOT NULL,              -- JSON: ["jwt","api_key"]
  jwt_alg       TEXT,                       -- HS256 | RS256 | …
  jwt_secret_ref TEXT,                      -- env var name holding the HS secret
  jwks_url      TEXT
);

CREATE TABLE rate_limit_policies (
  route_id      INTEGER NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
  algorithm     TEXT NOT NULL DEFAULT 'token_bucket',
  rps           REAL NOT NULL,
  burst         INTEGER NOT NULL,
  window_sec    INTEGER
);

CREATE TABLE plans (                       -- a named tier sizing limits to volume
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,       -- "free" | "pro" | "enterprise" | …
  rps           REAL NOT NULL,
  burst         INTEGER NOT NULL,
  daily_quota   INTEGER,                    -- nullable = unmetered
  created_at    TEXT NOT NULL
);

CREATE TABLE consumers (                    -- a customer / app that calls the API
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE,
  plan_id       INTEGER REFERENCES plans(id),
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE api_keys (
  id            INTEGER PRIMARY KEY,
  consumer_id   INTEGER NOT NULL REFERENCES consumers(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,             -- label, e.g. "prod", "ci" (a consumer may hold several)
  key_hash      TEXT NOT NULL UNIQUE,      -- SHA-256 of the key; plaintext never stored
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL,
  revoked_at    TEXT
);

CREATE TABLE admin_users (
  id            INTEGER PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,              -- bcrypt/argon2
  token_version INTEGER NOT NULL DEFAULT 1, -- bump to invalidate a user's session JWTs
  created_at    TEXT NOT NULL
);

CREATE TABLE config_version (             -- bumped on every change → drives reload
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  version       INTEGER NOT NULL
);
```

- **Migrations** run at startup (embedded SQL, versioned).
- **Secrets are referenced, not stored**: `jwt_secret_ref` names an env var; the
  actual HS secret lives in the environment / secrets manager.
- **Swap-friendly access (single-node decision):** the first release targets
  **single-node** SQLite (multi-node is deferred — see [§20](#20-roadmap)). All
  DB access goes through a **repository interface** in `internal/store`, so
  swapping SQLite for a shared Postgres/etcd backend later is a contained change
  with no impact on the data plane, registry, or admin API. Cheap insurance taken
  now even though multi-node isn't a current requirement.

---

## 12. Control Plane — Hot-Reload

Because both planes share one process, a config change can update the live data
plane **in-memory, atomically** — no polling, no restart:

```
Admin API mutation
   │ 1. write change to SQLite (transaction)
   │ 2. bump config_version
   │ 3. call registry.Reload()
   ▼
registry.Reload()
   │ 4. read full config from SQLite
   │ 5. build a new immutable Snapshot (sorted routes + compiled reverse proxies + limiters)
   │ 6. atomic.Pointer swap: Current → newSnapshot
   ▼
in-flight requests keep using the old snapshot; new requests see the new one
```

- The data plane reads `registry.Current()` — an `atomic.Pointer[Snapshot]` —
  on every request: **lock-free, race-free** (N1, N5).
- A reload is **all-or-nothing**: the new snapshot is built and validated fully
  before the swap; a bad edit is rejected by Admin API validation before it ever
  reaches the store.
- `config_version` lets a future multi-node deployment detect drift and reload by
  polling, when the store becomes shared (Postgres).

---

## 13. Control Plane — Admin API

REST/JSON under `/admin/api`, served on the **private admin listener** only.

| Method & path | Action |
|---------------|--------|
| `GET /admin/api/routes` | List routes (+ policies). |
| `POST /admin/api/routes` | Create a route with auth + rate-limit policies. |
| `GET /admin/api/routes/{id}` | Fetch one route. |
| `PUT /admin/api/routes/{id}` | Update a route/policies. |
| `DELETE /admin/api/routes/{id}` | Delete a route. |
| `GET / POST /admin/api/plans` | List / create plans (tier limits & quota). |
| `PUT / DELETE /admin/api/plans/{id}` | Update / delete a plan. |
| `GET / POST /admin/api/consumers` | List / create consumers (assign a plan). |
| `GET / PUT / DELETE /admin/api/consumers/{id}` | Fetch / update / delete a consumer. |
| `GET /admin/api/consumers/{id}/api-keys` | List a consumer's keys (metadata only). |
| `POST /admin/api/consumers/{id}/api-keys` | Issue a key for the consumer → returns plaintext **once**; stores only the hash. |
| `DELETE /admin/api/api-keys/{id}` | Revoke a key. |
| `POST /admin/api/auth/login` | Admin login → stateless JWT session token. |
| `GET /admin/api/health` | Control-plane health. |

- Every write **validates** (path prefix shape, upstream URL, referenced secret
  exists, algorithm known) before persisting, then triggers hot-reload.
- Mutations are transactional; partial writes can't occur (N5).

---

## 14. Control Plane — Admin UI (React)

- **Stack:** React + TypeScript + Vite. Talks only to the Admin API (REST/JSON).
- **Screens:** routes list/editor (with auth + rate-limit policy forms), API-key
  management (create/copy-once/revoke), login, and a dashboard surfacing the
  Prometheus metrics.
- **Delivery:** `vite build` → static assets in `web/dist/`, **embedded into the
  Go binary via `embed.FS`** and served by the admin listener. One deploy
  artifact despite two languages (N6).

---

## 15. Admin Authentication & Security

The admin surface reconfigures the gateway, so it's a sensitive attack surface:

- **Separate listener** (`:9000`), never exposed publicly — bind to localhost or
  an internal network; expect operators to reach it via VPN / bastion / ingress
  with its own auth.
- **Admin login** — username + password (hashed with bcrypt/argon2 in
  `admin_users`). On success the server issues a **stateless JWT session token**
  (HS256, signed with `GATEWAY_ADMIN_JWT_SECRET`), sent as a Bearer on subsequent
  Admin API calls and verified by signature alone — no session table.
  - **Decision (resolved):** stateless JWT over server-side sessions, for a
    simpler single-binary model. *Note this is the admin-session token system —
    entirely separate from the data-plane JWT that authenticates API clients.*
  - **Revocation trade-off:** stateless JWTs can't be individually revoked before
    expiry, so we mitigate with **short token lifetimes** (e.g. 15–60 min) plus
    refresh-on-activity. A `token_version` claim checked against `admin_users`
    (bump it to invalidate all of a user's tokens) is the planned escape hatch if
    instant revocation becomes a hard requirement.
- **Bootstrap admin** — first run seeds an admin from env
  (`GATEWAY_ADMIN_USER` / `GATEWAY_ADMIN_PASSWORD`), forced rotated after first
  login; thereafter managed in the store.
- **API keys** stored hashed; plaintext shown **once** at creation.
- **Roadmap:** admin RBAC (viewer vs editor), audit log of config changes,
  per-user `token_version` revocation.

---

## 16. Bootstrap Configuration

Most config now lives in SQLite. The environment carries only **bootstrap +
secrets** — the things needed before/around the store:

| Variable | Default | Description |
|----------|---------|-------------|
| `GATEWAY_PROXY_ADDR` | `:8080` | Public data-plane listen address. |
| `GATEWAY_ADMIN_ADDR` | `127.0.0.1:9000` | Private admin (API + UI) listen address. |
| `GATEWAY_DB_PATH` | `./gateway.db` | SQLite file path (mount on a volume). |
| `GATEWAY_ADMIN_USER` | `admin` | Bootstrap admin username (first run only). |
| `GATEWAY_ADMIN_PASSWORD` | — | Bootstrap admin password (first run only). |
| `GATEWAY_ADMIN_JWT_SECRET` | — | HS256 secret for signing admin **session** tokens (distinct from data-plane JWT). |
| `GATEWAY_JWT_SECRET` | — | HS secret, referenced by `jwt_secret_ref` in route policies. |
| `GATEWAY_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error`. |
| `GATEWAY_METRICS_PATH` | `/metrics` | Prometheus scrape path (data plane). |
| `GATEWAY_*_TIMEOUT` | — | Read/write/idle/shutdown timeouts (as before). |

---

## 17. Error Handling & Resilience

| Condition | Response | Behavior |
|-----------|----------|----------|
| No route matches | `404` | Logged + metered under `route="unmatched"`. |
| Auth required and missing/invalid | `401` | `WWW-Authenticate` set. |
| Rate limit exceeded | `429` | Cheap rejection before auth. |
| Upstream unreachable/errors | `502` | `ErrorHandler` logs route + cause. |
| Handler panic | `500` | `Recover` keeps the process alive. |
| Invalid admin edit | `400` | Validated before persisting; never half-applies. |
| Store/migration failure at boot | exit 1 | Fail fast. |
| SIGINT/SIGTERM | graceful | Drain both listeners within `SHUTDOWN_TIMEOUT`. |

---

## 18. Deployment

- **Artifact:** one Go binary (UI embedded). `CGO_ENABLED=0` static build via the
  pure-Go SQLite driver → `scratch`/`distroless` image.
- **Persistence:** the SQLite file on a **mounted volume** (survives restarts).
- **Ports:** publish the proxy port; keep the admin port internal.
- **Probes:** liveness → `/healthz`; metrics scraped from `/metrics`.
- **Scaling caveat:** SQLite is per-node. Multi-replica with shared config needs
  the Postgres/etcd roadmap item; until then, run single-node or treat each
  node's store as independent.

---

## 19. Phased Delivery Plan

Each phase is independently shippable and testable.

1. **Phase 1 — Data plane.** Proxy, routing, auth, rate limiting, observability,
   graceful shutdown. Config from a static source (the stashed scaffold, adapted
   to read from the registry). *Largely built.*
2. **Phase 2 — Store + registry.** SQLite schema/migrations; load config into an
   immutable snapshot; `registry.Current()` lock-free reads; hot-reload swap.
3. **Phase 3 — Admin API.** REST CRUD over the store with validation, admin
   login/sessions, consumer/plan + API-key management; trigger hot-reload on writes.
4. **Phase 4 — Admin UI.** React app for routes/policies/keys/dashboard, embedded
   and served from the admin listener.

---

## 20. Roadmap

1. **Multi-node shared config** — Postgres/etcd backend + version-poll reload.
2. **Distributed rate limiting** (Redis) for correct multi-replica limits.
3. **Authorization** — per-route role/scope gates, then fine-grained policy (OPA/Cedar).
4. **Per-consumer JWT identity** — derive the consumer from a token claim, or
   select a per-customer key by `kid`/JWKS (API-key consumers ship first; see §8a).
5. **Upstream load balancing + active health checks** (multiple targets/route).
6. **Admin RBAC + audit log** of all config changes.
7. **Distributed tracing** — OpenTelemetry spans propagated to upstreams.
8. **Resilience** — circuit breakers, retries with budgets, outlier ejection.
9. **mTLS to upstreams** and optional data-plane TLS termination.

---

## 21. Open Questions

- **Trusted-proxy model:** gate `X-Forwarded-For` parsing behind a trusted-CIDR
  allow-list to prevent client-IP spoofing?
- **JWT for GA:** ship HS256-only first, or include RS256/JWKS from day one?
- **Authorization scope:** do we need per-consumer/tenant scoping of limits & auth?

### Resolved

- **Admin sessions** → **stateless JWT** (HS256, short-lived; `token_version`
  escape hatch for revocation). See [§15](#15-admin-authentication--security).
- **Multi-node timeline** → **not now.** First release is single-node SQLite; DB
  access kept behind a repository interface so a shared-store swap stays cheap.
  See [§11](#11-control-plane--config-store-sqlite) / [§20](#20-roadmap).
```
