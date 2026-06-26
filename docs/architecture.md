# Application Architecture

Visual companion to the [technical design](./technical-design.md). It documents
the **current implementation** (Phase 1 data plane + security hardening) and
shows where the planned control plane (Phases 2–4) attaches. All diagrams are
Mermaid (rendered inline by GitHub).

### Colour legend

```mermaid
flowchart LR
    a["Client / external"]:::client
    b["Data plane"]:::gw
    c["Upstream service"]:::up
    d["Config / registry"]:::store
    e["Observability"]:::obs
    f["Error / reject"]:::err
    g["Planned (Phase 2-4)"]:::planned

    classDef client fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef gw fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef up fill:#fef3c7,stroke:#d97706,color:#7c2d12;
    classDef store fill:#ede9fe,stroke:#7c3aed,color:#4c1d95;
    classDef obs fill:#ccfbf1,stroke:#0d9488,color:#134e4a;
    classDef err fill:#fee2e2,stroke:#dc2626,color:#7f1d1d;
    classDef planned fill:#f3f4f6,stroke:#9ca3af,color:#374151,stroke-dasharray:5 5;
```

- [1. System context](#1-system-context)
- [2. Two-plane architecture](#2-two-plane-architecture)
- [3. Module / package map](#3-module--package-map)
- [4. Middleware pipeline](#4-middleware-pipeline)
- [5. Request lifecycle (sequence)](#5-request-lifecycle-sequence)
- [6. Authentication decision](#6-authentication-decision)
- [7. Client-IP resolution (trusted proxy)](#7-client-ip-resolution-trusted-proxy)
- [8. Configuration & hot-reload](#8-configuration--hot-reload)
- [9. Snapshot lifecycle](#9-snapshot-lifecycle)
- [10. Rate-limiting design](#10-rate-limiting-design)
- [11. Outcomes & status codes](#11-outcomes--status-codes)
- [12. Deployment view](#12-deployment-view)
- [13. Current vs planned](#13-current-vs-planned)

---

## 1. System context

```mermaid
flowchart LR
    client["Clients<br/>web · mobile · services"]:::client

    subgraph edge["Trust boundary"]
        gw["API Gateway"]:::gw
    end

    upA["users-service"]:::up
    upB["orders-service"]:::up
    upC["cms"]:::up
    prom["Prometheus"]:::obs
    ops["Operators<br/>(control plane — planned)"]:::planned

    client -->|HTTPS| gw
    gw -->|/api/users/*| upA
    gw -->|/api/orders/*| upB
    gw -->|/public/*| upC
    prom -.->|GET /metrics| gw
    ops -.->|Admin API/UI · Phase 3-4| gw

    classDef client fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef gw fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef up fill:#fef3c7,stroke:#d97706,color:#7c2d12;
    classDef obs fill:#ccfbf1,stroke:#0d9488,color:#134e4a;
    classDef planned fill:#f3f4f6,stroke:#9ca3af,color:#374151,stroke-dasharray:5 5;
    style edge fill:#eff6ff,stroke:#93c5fd,color:#1e3a8a;
```

The gateway is the **single ingress**: one host for clients, routing each
request to the right upstream by path prefix while applying auth, rate limiting,
and observability at this one choke point.

---

## 2. Two-plane architecture

```mermaid
flowchart TB
    cfgenv["env / .env<br/>bootstrap config (current source)"]:::store
    client["client"]:::client

    subgraph CP["Control plane — PLANNED"]
        direction TB
        ui["Admin UI (Go templates + HTMX)"]:::planned --> api["Admin API (REST)"]:::planned
        api --> store[("SQLite store")]:::planned
    end

    subgraph DP["Data plane — BUILT"]
        direction TB
        reg["registry<br/>atomic snapshot"]:::store
        chain["middleware chain"]:::gw
        rp["reverse proxy"]:::gw
        reg --> chain --> rp
    end

    store -. "load + hot-reload" .-> reg
    cfgenv --> reg
    client --> chain
    rp --> up["upstreams"]:::up

    classDef client fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef gw fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef up fill:#fef3c7,stroke:#d97706,color:#7c2d12;
    classDef store fill:#ede9fe,stroke:#7c3aed,color:#4c1d95;
    classDef planned fill:#f3f4f6,stroke:#9ca3af,color:#374151,stroke-dasharray:5 5;
    style CP fill:#fafafa,stroke:#d1d5db,color:#374151,stroke-dasharray:5 5;
    style DP fill:#eff6ff,stroke:#93c5fd,color:#1e3a8a;
```

The **registry** is the seam between planes: the data plane reads it lock-free
per request; the config source (env now, SQLite later) writes it.

---

## 3. Module / package map

Arrows point from a package to what it imports. Coloured by role.

```mermaid
flowchart TD
    main["cmd/gateway<br/>entrypoint · signals"]:::entry
    server["internal/server<br/>HTTP wiring"]:::gw
    registry["internal/registry<br/>atomic snapshot"]:::store
    proxy["internal/proxy<br/>match · reverse proxy"]:::gw
    mw["internal/middleware<br/>reqID · recover · log · metrics<br/>auth · ratelimit · realIP"]:::gw
    rl["internal/ratelimit<br/>Limiter + 4 algorithms"]:::accent
    cfg["internal/config<br/>env bootstrap · validation"]:::store
    model["internal/model<br/>Route · AuthPolicy · RateLimitPolicy"]:::core

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

    classDef entry fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef gw fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef store fill:#ede9fe,stroke:#7c3aed,color:#4c1d95;
    classDef accent fill:#ffe4e6,stroke:#e11d48,color:#881337;
    classDef core fill:#fef9c3,stroke:#ca8a04,color:#713f12;
```

`model` is the shared vocabulary every layer agrees on. The data plane (`proxy`)
depends only on `model` + `ratelimit` — never on config sources — so swapping env
for SQLite later touches only `config`/`registry`.

---

## 4. Middleware pipeline

The chain in order. Each stage can short-circuit with a status; otherwise it
calls the next. `Resolve` runs first so everything downstream can read the
matched route.

```mermaid
flowchart LR
    in(["request"]):::client --> rid["RequestID"]:::gw
    rid --> rec["Recover"]:::gw
    rec --> res["Resolve<br/>normalize · match"]:::gw
    res --> log["Logging"]:::obs
    log --> met["Metrics"]:::obs
    met --> rl["RateLimit"]:::gw
    rl --> auth["Auth"]:::gw
    auth --> disp["Dispatch"]:::gw
    disp -->|matched| up(["upstream"]):::up
    disp -->|no match| nf["404 / 405"]:::err

    rec -. panic .-> e500["500"]:::err
    rl -. over limit .-> e429["429"]:::err
    auth -. invalid .-> e401["401"]:::err
    disp -. upstream error .-> e502["502"]:::err

    classDef client fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef gw fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef obs fill:#ccfbf1,stroke:#0d9488,color:#134e4a;
    classDef up fill:#fef3c7,stroke:#d97706,color:#7c2d12;
    classDef err fill:#fee2e2,stroke:#dc2626,color:#7f1d1d;
```

Operational endpoints bypass the chain entirely: `GET /healthz` and
`GET /metrics`.

---

## 5. Request lifecycle (sequence)

```mermaid
sequenceDiagram
    autonumber
    box rgb(220,252,231) Client
    participant C as Client
    end
    box rgb(219,234,254) Gateway middleware
    participant RID as RequestID
    participant Res as Resolve
    participant RL as RateLimit
    participant Auth as Auth
    participant D as Dispatch
    end
    box rgb(254,243,199) Upstream
    participant U as Upstream
    end

    C->>RID: HTTP request
    RID->>Res: + X-Request-ID
    Note over Res: normalize path (collapse ./..)<br/>match snapshot → Entry (or nil + Allow)
    Res->>RL: (also: Logging, Metrics)
    Note over RL: key = RealIP.From(r)<br/>limiter.Allow()? else 429
    RL->>Auth: 
    Note over Auth: if RequireAuth:<br/>JWT (alg-allowlisted) or API key, else 401
    Auth->>D: 
    alt route matched
        D->>U: reverse proxy (strip prefix · X-Forwarded-*)
        U-->>D: response (or error/timeout → 502)
        D-->>C: stream response
    else no match
        D-->>C: 404 (or 405 + Allow)
    end
```

---

## 6. Authentication decision

Only routes with `RequireAuth` are gated. A request passes if **any** accepted
credential validates.

```mermaid
flowchart TD
    start(["request on protected route"]):::client --> req{"RequireAuth?"}
    req -- no --> pass["pass through"]:::ok
    req -- yes --> akcheck{"route accepts<br/>api_key?"}
    akcheck -- yes --> ak{"X-API-Key valid?"}
    ak -- yes --> pass
    ak -- no --> jwtcheck
    akcheck -- no --> jwtcheck{"route accepts jwt?"}
    jwtcheck -- yes --> scheme{"Bearer scheme?<br/>(case-insensitive)"}
    scheme -- yes --> alg{"alg in HS256/384/512?<br/>(allow-list)"}
    alg -- yes --> sig{"signature & exp valid?"}
    sig -- yes --> pass
    alg -- "no (none / RS256)" --> deny["401"]:::err
    sig -- no --> deny
    scheme -- no --> deny
    jwtcheck -- no --> deny

    classDef client fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef ok fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef err fill:#fee2e2,stroke:#dc2626,color:#7f1d1d;
```

The `alg` allow-list is what defeats `alg=none` and RS→HS confusion attacks.

---

## 7. Client-IP resolution (trusted proxy)

`RealIP` decides the identity used for rate limiting and logging. The secure
default ignores `X-Forwarded-For`, so a client can't spoof it to evade limits.

```mermaid
flowchart TD
    r(["request"]):::client --> peer{"peer (RemoteAddr) in<br/>GATEWAY_TRUSTED_PROXIES?"}
    peer -- "no (default)" --> remote["use RemoteAddr<br/>(XFF ignored)"]:::gw
    peer -- yes --> xff{"X-Forwarded-For present?"}
    xff -- yes --> first["use left-most XFF IP<br/>(original client)"]:::gw
    xff -- no --> remote

    remote --> key["rate-limit key + log 'remote'"]:::store
    first --> key

    classDef client fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef gw fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef store fill:#ede9fe,stroke:#7c3aed,color:#4c1d95;
```

---

## 8. Configuration & hot-reload

The registry holds the live config as an immutable snapshot in an
`atomic.Pointer`. Reads are lock-free; updates build a new snapshot and swap it
atomically.

```mermaid
sequenceDiagram
    autonumber
    box rgb(237,233,254) Config + registry
    participant Src as Config source<br/>(env now · SQLite later)
    participant Reg as registry
    participant Cur as Active snapshot (atomic)
    end
    box rgb(219,234,254) Data plane
    participant DP as per request
    end

    Src->>Reg: Load(routes)
    Reg->>Reg: compile → proxies + limiters
    alt invalid route
        Reg-->>Src: error (keep current — no half-apply)
    else valid
        Reg->>Cur: Swap(new)
        Reg->>Reg: old.Close() (stop old limiters)
    end
    DP->>Cur: Current() — lock-free read
    Cur-->>DP: active snapshot
```

---

## 9. Snapshot lifecycle

```mermaid
stateDiagram-v2
    direction LR
    [*] --> Empty: registry.New()
    Empty --> Active: Load(valid)
    Active --> Active: Current() (reads)
    Active --> Building: Load(new)
    Building --> Active: valid → swap + Close(old)
    Building --> Active: invalid → keep Active
    Active --> [*]: shutdown
```

A reload is all-or-nothing: the new snapshot is built and validated fully before
the atomic swap, so a bad edit never half-applies and in-flight requests finish
on the snapshot they started with.

---

## 10. Rate-limiting design

A pluggable `Limiter` with four algorithms, chosen per route. A shared `keyed`
wrapper owns per-client-IP state and idle eviction; each algorithm only
implements `allow()`.

```mermaid
classDiagram
    direction LR
    class Limiter {
        <<interface>>
        +Allow(key) bool, Result
        +Stop()
    }
    class Result {
        +Limit int
        +Remaining int
        +Reset Duration
        +RetryAfter Duration
    }
    class keyed {
        -factory
        -visitors
        +Allow(key) bool, Result
        +Stop()
    }
    class bucket {
        <<interface>>
        -allow() bool, Result
    }
    class tokenBucket
    class leakyBucket
    class fixedWindow
    class slidingWindow

    Limiter <|.. keyed
    Limiter ..> Result : returns
    keyed o-- bucket : one per key
    bucket <|.. tokenBucket
    bucket <|.. leakyBucket
    bucket <|.. fixedWindow
    bucket <|.. slidingWindow
```

```mermaid
flowchart LR
    tb["token_bucket<br/>steady refill + burst"]:::a
    lb["leaky_bucket<br/>constant drain"]:::b
    fw["fixed_window<br/>count per window"]:::c
    sw["sliding_window<br/>rolling weighted"]:::d

    classDef a fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef b fill:#ccfbf1,stroke:#0d9488,color:#134e4a;
    classDef c fill:#fef3c7,stroke:#d97706,color:#7c2d12;
    classDef d fill:#ede9fe,stroke:#7c3aed,color:#4c1d95;
```

| Algorithm | Behavior | Params |
|-----------|----------|--------|
| `token_bucket` *(default)* | steady refill + burst | `rps`, `burst` |
| `leaky_bucket` | constant drain, no bursts | `rps`, `burst` |
| `fixed_window` | count per fixed window | `rps`, `window_sec` |
| `sliding_window` | rolling weighted window | `rps`, `window_sec` |

### Consumption headers

Each `Allow` returns a `Result` (limit, remaining, reset, retry-after) computed
from the route's configured limit. The middleware surfaces it so clients can see
their consumption and when capacity returns.

```mermaid
flowchart LR
    req(["request"]):::client --> lim["limiter.Allow(key)<br/>→ (ok, Result)"]:::gw
    lim -->|ok| pass["proxy + headers:<br/>RateLimit-Limit / -Remaining / -Reset"]:::obs
    lim -->|denied| rej["429 +<br/>Retry-After · RateLimit-Reset"]:::err

    classDef client fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef gw fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef obs fill:#ccfbf1,stroke:#0d9488,color:#134e4a;
    classDef err fill:#fee2e2,stroke:#dc2626,color:#7f1d1d;
```

| Header | Meaning |
|--------|---------|
| `RateLimit-Limit` (+ `X-RateLimit-Limit`) | configured allowance (burst / per-window limit) |
| `RateLimit-Remaining` (+ `X-`) | allowance left for this client |
| `RateLimit-Reset` (+ `X-`) | seconds until the allowance replenishes |
| `Retry-After` | on `429` only — seconds to wait before retrying |

---

## 11. Outcomes & status codes

```mermaid
flowchart TD
    req(["request"]):::client --> norm["normalize path"]:::gw
    norm --> match{"route match?"}
    match -- "prefix ok,<br/>method not allowed" --> c405["405 + Allow"]:::err
    match -- "no match" --> c404["404"]:::err
    match -- yes --> rl{"within rate limit?"}
    rl -- no --> c429["429"]:::err
    rl -- yes --> auth{"auth ok?"}
    auth -- no --> c401["401"]:::err
    auth -- yes --> up{"upstream ok?"}
    up -- "error / timeout" --> c502["502"]:::err
    up -- panic --> c500["500 (recovered)"]:::err
    up -- ok --> c200["2xx (streamed)"]:::ok

    classDef client fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef gw fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef ok fill:#bbf7d0,stroke:#16a34a,color:#14532d;
    classDef err fill:#fee2e2,stroke:#dc2626,color:#7f1d1d;
```

---

## 12. Deployment view

```mermaid
flowchart TB
    lb["L4/L7 load balancer<br/>sets X-Forwarded-For"]:::client
    subgraph host["Container / host"]
        bin["gateway binary<br/>CGO_ENABLED=0 static"]:::gw
        envf[".env / env vars + secrets"]:::store
        db[("SQLite file on a volume<br/>— Phase 2")]:::planned
        envf --> bin
        db -.-> bin
    end
    prom["Prometheus"]:::obs

    lb --> bin
    bin --> upstreams["upstream services"]:::up
    prom -.->|/metrics| bin

    classDef client fill:#dcfce7,stroke:#16a34a,color:#14532d;
    classDef gw fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    classDef up fill:#fef3c7,stroke:#d97706,color:#7c2d12;
    classDef store fill:#ede9fe,stroke:#7c3aed,color:#4c1d95;
    classDef obs fill:#ccfbf1,stroke:#0d9488,color:#134e4a;
    classDef planned fill:#f3f4f6,stroke:#9ca3af,color:#374151,stroke-dasharray:5 5;
    style host fill:#f8fafc,stroke:#cbd5e1,color:#334155;
```

- **Artifact:** one static binary (pure-Go SQLite keeps `CGO_ENABLED=0`).
- **Behind an LB?** set `GATEWAY_TRUSTED_PROXIES` to the LB network so XFF is
  trusted; otherwise XFF is ignored and every client looks like the LB.
- **Scaling:** stateless data plane scales horizontally; rate-limit state and
  (Phase 2) SQLite config are per-node until the shared-store roadmap item.

---

## 13. Current vs planned

```mermaid
flowchart LR
    subgraph done["Built"]
        direction TB
        p1["Phase 1 — data plane"]:::ok
        sec["Security hardening"]:::ok
        p1 --> sec
    end
    subgraph next["Planned"]
        direction TB
        p2["Phase 2 — SQLite + hot-reload"]:::planned
        p3["Phase 3 — Admin API + consumers/plans"]:::planned
        p4["Phase 4 — Admin UI (Go templates + HTMX)"]:::planned
        p2 --> p3 --> p4
    end
    sec --> p2

    classDef ok fill:#bbf7d0,stroke:#16a34a,color:#14532d;
    classDef planned fill:#f3f4f6,stroke:#9ca3af,color:#374151,stroke-dasharray:5 5;
    style done fill:#f0fdf4,stroke:#86efac,color:#14532d;
    style next fill:#fafafa,stroke:#d1d5db,color:#374151;
```

| Area | Status |
|------|--------|
| Reverse-proxy routing (longest-prefix · strip · methods) | ✅ Built |
| Auth — JWT (HS256/384/512, alg-allowlisted) + API keys | ✅ Built |
| Rate limiting — 4 algorithms, per route | ✅ Built |
| Observability — slog logs · Prometheus · request IDs | ✅ Built |
| Trusted-proxy XFF · path normalization · upstream timeouts | ✅ Built |
| SQLite config store + hot-reload | ⏳ Phase 2 |
| Admin REST API + consumers/plans | ⏳ Phase 3 |
| Admin UI — Go html/template + HTMX | ⏳ Phase 4 |

See [technical-design.md](./technical-design.md) for the full specification and
[test-findings.md](./test-findings.md) for the adversarial test results.
