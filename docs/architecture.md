# Application Architecture

Visual companion to the [technical design](./technical-design.md). It documents
the **current implementation** (Phase 1 data plane + security hardening) and
shows where the planned control plane (Phases 2–4) attaches. Diagrams are
Mermaid (rendered inline by GitHub) with ASCII fallbacks where useful.

- [1. System context](#1-system-context)
- [2. Two-plane architecture](#2-two-plane-architecture)
- [3. Module / package map](#3-module--package-map)
- [4. Request lifecycle](#4-request-lifecycle)
- [5. Configuration & hot-reload](#5-configuration--hot-reload)
- [6. Rate-limiting design](#6-rate-limiting-design)
- [7. Outcomes & status codes](#7-outcomes--status-codes)
- [8. Deployment view](#8-deployment-view)
- [9. Current vs planned](#9-current-vs-planned)

---

## 1. System context

Who talks to the gateway and what it fronts.

```mermaid
flowchart LR
    client["Clients<br/>(web / mobile / services)"]
    subgraph edge["Trust boundary"]
        gw["API Gateway<br/>(this project)"]
    end
    upA["Upstream A<br/>users-service"]
    upB["Upstream B<br/>orders-service"]
    upC["Upstream C<br/>cms"]
    prom["Prometheus<br/>(scrapes /metrics)"]
    ops["Operators<br/>(control plane — planned)"]

    client -->|HTTPS| gw
    gw -->|HTTP /api/users/*| upA
    gw -->|HTTP /api/orders/*| upB
    gw -->|HTTP /public/*| upC
    prom -.->|GET /metrics| gw
    ops -.->|Admin API/UI — Phase 3-4| gw
```

The gateway is the **single ingress**: clients address one host; the gateway
routes each request to the right upstream by path prefix, applying auth, rate
limiting, and observability at this one choke point.

---

## 2. Two-plane architecture

The system separates a **data plane** (serves live traffic) from a **control
plane** (manages configuration). Today the data plane is built; the control
plane is planned, with the **registry** already the seam between them.

```mermaid
flowchart TB
    subgraph CP["Control plane — PLANNED (Phases 2-4)"]
        ui["React Admin UI"] --> api["Admin API (REST)"]
        api --> store[("SQLite config store")]
    end

    subgraph DP["Data plane — BUILT"]
        reg["registry<br/>atomic snapshot"]
        chain["middleware chain"]
        proxy["reverse proxy"]
        reg --> chain --> proxy
    end

    store -. "load + hot-reload" .-> reg
    cfgenv["env / .env<br/>(bootstrap config — current source)"] --> reg
    client["client"] --> chain
    proxy --> up["upstreams"]

    classDef planned stroke-dasharray:5 5,fill:#f7f7f7;
    class CP,ui,api,store planned;
```

| | Data plane | Control plane |
|---|---|---|
| **Status** | Built | Planned |
| **Job** | Proxy traffic, fast | Manage config |
| **Reads/writes** | reads `registry.Current()` per request | writes store → triggers reload |
| **Listener** | public `:8080` | private `:9000` (planned) |
| **Config source (now)** | env / `.env` → registry | — |

---

## 3. Module / package map

Go packages and their dependencies. `cmd/gateway` wires everything; `internal/*`
holds the logic. Arrows point from a package to what it imports.

```mermaid
flowchart TD
    main["cmd/gateway<br/>entrypoint + signals"]
    server["internal/server<br/>HTTP wiring"]
    registry["internal/registry<br/>atomic snapshot"]
    proxy["internal/proxy<br/>match + reverse proxy"]
    mw["internal/middleware<br/>reqID · recover · log · metrics · auth · ratelimit · realIP"]
    rl["internal/ratelimit<br/>Limiter + 4 algorithms"]
    cfg["internal/config<br/>env bootstrap + validation"]
    model["internal/model<br/>Route · AuthPolicy · RateLimitPolicy"]

    main --> cfg
    main --> registry
    main --> proxy
    main --> server
    server --> cfg
    server --> mw
    server --> proxy
    server --> registry
    registry --> proxy
    registry --> model
    proxy --> model
    proxy --> rl
    mw --> proxy
    mw --> model
    rl --> model
    cfg --> model
```

`model` is the shared vocabulary every layer agrees on. Note the data plane
(`proxy`) depends only on `model` + `ratelimit` — never on config sources — so
swapping env for SQLite later touches only `config`/`registry`.

---

## 4. Request lifecycle

Order is significant: `Resolve` runs first so logging, metrics, rate-limit and
auth can all read the matched route from context. Unmatched requests still flow
through (logged + metered); `Dispatch` emits the final 404/405.

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant RID as RequestID
    participant Rec as Recover
    participant Res as Resolve (proxy)
    participant Log as Logging
    participant Met as Metrics
    participant RL as RateLimit
    participant Auth as Auth
    participant D as Dispatch (proxy)
    participant U as Upstream

    C->>RID: HTTP request
    RID->>Rec: + X-Request-ID (ctx + header)
    Rec->>Res: (defer recover → 500)
    Note over Res: normalize path (collapse ./..)<br/>match snapshot → Entry in ctx (or nil + allowed methods)
    Res->>Log: 
    Log->>Met: 
    Note over Met: inflight++ / counter / latency
    Met->>RL: 
    Note over RL: key = RealIP.From(r)<br/>limiter.Allow()? else 429
    RL->>Auth: 
    Note over Auth: if route.RequireAuth:<br/>JWT (alg-allowlisted) or API key, else 401
    Auth->>D: 
    alt route matched
        D->>U: reverse proxy (strip prefix, X-Forwarded-*)
        U-->>D: response (or error → 502)
        D-->>C: stream response
    else no match
        D-->>C: 404 (or 405 + Allow)
    end
```

**RealIP** (used by Logging + RateLimit) resolves the client IP, trusting
`X-Forwarded-For` only from configured trusted proxies — otherwise `RemoteAddr`.

Operational endpoints bypass this chain entirely:

```
GET /healthz  → 200 {"status":"ok"}
GET /metrics  → Prometheus exposition
```

---

## 5. Configuration & hot-reload

The **registry** holds the live config as an immutable snapshot in an
`atomic.Pointer`. Reads are lock-free; updates build a new snapshot and swap it
atomically — the basis for zero-restart hot-reload.

```mermaid
sequenceDiagram
    autonumber
    participant Src as Config source<br/>(env now · SQLite later)
    participant Reg as registry
    participant Snap as proxy.NewSnapshot
    participant Cur as Active snapshot (atomic)
    participant DP as Data plane (per request)

    Src->>Reg: Load(routes)
    Reg->>Snap: compile routes → proxies + limiters
    alt invalid route
        Snap-->>Reg: error
        Note over Reg: keep current snapshot (no half-apply)
    else valid
        Snap-->>Reg: new *Snapshot
        Reg->>Cur: Swap(new)
        Reg->>Reg: old.Close() (stop old limiters)
    end
    DP->>Cur: Current() (lock-free read)
    Cur-->>DP: active *Snapshot
```

Configuration today is **bootstrap env vars** (loaded from `.env` if present):
listen addr, secrets, routes JSON, rate-limit defaults, trusted proxies, and
upstream timeouts. In Phase 2 the same `Load` path is driven by the SQLite store.

---

## 6. Rate-limiting design

A pluggable `Limiter` interface with four algorithms, selected per route. A
shared `keyed` wrapper owns per-key (per-client-IP) state and idle eviction, so
each algorithm only implements `allow()`.

```mermaid
classDiagram
    class Limiter {
        <<interface>>
        +Allow(key string) bool
        +Stop()
    }
    class keyed {
        -factory
        -visitors
        +Allow(key) bool
        +Stop()
    }
    class bucket {
        <<interface>>
        -allow() bool
    }
    class tokenBucket
    class leakyBucket
    class fixedWindow
    class slidingWindow

    Limiter <|.. keyed
    keyed o-- bucket : per key
    bucket <|.. tokenBucket
    bucket <|.. leakyBucket
    bucket <|.. fixedWindow
    bucket <|.. slidingWindow
```

| Algorithm | Behavior | Params |
|-----------|----------|--------|
| `token_bucket` *(default)* | steady refill + burst | `rps`, `burst` |
| `leaky_bucket` | constant drain, no bursts | `rps`, `burst` |
| `fixed_window` | count per fixed window | `rps`, `window_sec` |
| `sliding_window` | rolling weighted window | `rps`, `window_sec` |

---

## 7. Outcomes & status codes

```mermaid
flowchart TD
    req["request"] --> norm["normalize path"]
    norm --> match{"route match?"}
    match -- "no, prefix matches<br/>but method not allowed" --> c405["405 + Allow"]
    match -- "no match" --> c404["404"]
    match -- "yes" --> rl{"within rate limit?"}
    rl -- no --> c429["429"]
    rl -- yes --> auth{"auth ok?<br/>(if required)"}
    auth -- no --> c401["401"]
    auth -- yes --> up{"upstream ok?"}
    up -- "error / timeout" --> c502["502"]
    up -- panic --> c500["500 (recovered)"]
    up -- ok --> c200["2xx (streamed)"]
```

| Code | When |
|------|------|
| 2xx | proxied upstream response |
| 401 | auth required, missing/invalid credential |
| 404 | no route matches the path |
| 405 | path matches but method not allowed (`Allow` header set) |
| 429 | rate limit exceeded |
| 500 | handler panic (recovered; process survives) |
| 502 | upstream unreachable / timed out |

---

## 8. Deployment view

Single static Go binary; SQLite + admin UI arrive with the control plane.

```mermaid
flowchart TB
    subgraph host["Container / host"]
        bin["gateway binary<br/>(CGO_ENABLED=0 static)"]
        envf[".env / env vars<br/>+ secrets"]
        db[("SQLite file<br/>on a volume — Phase 2")]
        envf --> bin
        db -.-> bin
    end
    lb["L4/L7 load balancer<br/>(sets X-Forwarded-For)"] --> bin
    bin --> upstreams["upstream services"]
    prom["Prometheus"] -.->|/metrics| bin

    classDef planned stroke-dasharray:5 5,fill:#f7f7f7;
    class db planned;
```

- **Artifact:** one static binary (pure-Go SQLite keeps `CGO_ENABLED=0`).
- **Behind an LB?** set `GATEWAY_TRUSTED_PROXIES` to the LB network so
  `X-Forwarded-For` is trusted for client-IP resolution; otherwise XFF is ignored.
- **Scaling:** stateless data plane scales horizontally; rate-limit state and
  (Phase 2) SQLite config are per-node until the shared-store roadmap item.

---

## 9. Current vs planned

```mermaid
flowchart LR
    subgraph done["Built"]
        p1["Phase 1 — data plane<br/>routing · auth · rate-limit · observability"]
        sec["Security hardening<br/>XFF trust · path norm · timeouts · 405 · uniqueness"]
    end
    subgraph next["Planned"]
        p2["Phase 2 — SQLite store + hot-reload"]
        p3["Phase 3 — Admin API + consumers/plans"]
        p4["Phase 4 — React Admin UI"]
    end
    p1 --> sec --> p2 --> p3 --> p4
```

| Area | Status |
|------|--------|
| Reverse-proxy routing (longest-prefix, strip, methods) | ✅ Built |
| Auth — JWT (HS256/384/512, alg-allowlisted) + API keys | ✅ Built |
| Rate limiting — 4 algorithms, per route | ✅ Built |
| Observability — slog logs, Prometheus, request IDs | ✅ Built |
| Trusted-proxy XFF, path normalization, upstream timeouts | ✅ Built |
| SQLite config store + hot-reload | ⏳ Phase 2 |
| Admin REST API + consumers/plans | ⏳ Phase 3 |
| React admin UI | ⏳ Phase 4 |

See [technical-design.md](./technical-design.md) for the full specification and
[test-findings.md](./test-findings.md) for the adversarial test results.
