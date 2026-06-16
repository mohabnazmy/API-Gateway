# Concepts & Auth Decisions — Reader's Guide

**Purpose:** A plain-language companion to the [technical design](./technical-design.md)
explaining the two concepts that need your decision before we build:
**reverse-proxy routing** and **authentication / authorization**. The auth
section ends with a decision worksheet.

**Status:** For review · **Last updated:** 2026-06-16

---

## Part 1 — Reverse-Proxy Routing

### What a reverse proxy is

A **proxy** sits between a client and a server and forwards requests. The
*direction* it faces is what names it:

- **Forward proxy** — sits in front of *clients* (your browser → corporate proxy
  → internet). It represents the client.
- **Reverse proxy** — sits in front of *servers* (client → proxy → your backend
  services). It represents the servers.

"Reverse" because it's on the **server's** side. The client thinks it's talking
to one server; behind the scenes the proxy relays the request to a real backend
and streams the response back.

```
  client  ──►  [ reverse proxy ]  ──►  backend service
          ◄──                    ◄──   (the real server)
```

The client never learns the backend's address, how many backends exist, or which
one served the request.

### What routing adds

The proxy fronts many backends, so it must decide **which backend** each request
goes to. That decision is **routing** — in our gateway, by **longest path-prefix
match**.

Given:

```
/api/users   →  http://users-service:9001
/api/orders  →  http://orders-service:9002
/public      →  http://cms:9003
```

Requests resolve like this:

```
GET /api/users/42      →  /api/users   →  users-service
GET /api/orders/99     →  /api/orders  →  orders-service
GET /public/logo.png   →  /public      →  cms
```

**"Longest match wins":** if both `/api` and `/api/users` existed, a request to
`/api/users/42` picks the more specific `/api/users`.

### Two extra knobs in our design

- **Strip prefix** — optionally remove the matched prefix before forwarding, so
  the backend sees a clean path:
  ```
  client → GET /api/users/42   →  (strip /api/users)  →  backend receives GET /42
  ```
- **Method allow-list** — a route can be limited to certain HTTP methods
  (e.g. only `GET`/`POST`).

### Why it matters

This is what makes the gateway a **single front door**. Clients hit one address;
you can split, rename, relocate, or scale backends behind it without clients
changing anything. And because *all* traffic flows through one point, it's the
natural place to add auth, rate limiting, logging, and metrics **once** instead
of in every backend.

> In code: `internal/proxy/proxy.go` holds an `Engine` that stores routes,
> matches each request, and hands it to Go's standard-library
> `httputil.ReverseProxy` for the actual forwarding and response streaming.

---

## Part 2 — Authentication & Authorization

Two **separate** questions, often confused:

- **Authentication (AuthN):** *Who are you?* — proving identity.
- **Authorization (AuthZ):** *What are you allowed to do?* — granting access.

You authenticate first, then authorize.

### 2.1 What a JWT is

A **JWT** (JSON Web Token) is a signed token a client sends to prove identity.
Three dot-separated parts:

```
eyJhbG...header . eyJzdWI...payload . SflKxw...signature
      │                  │                    │
   {"alg":"HS256"}   {"sub":"user42",   proves header+payload
                      "exp":...}          were not tampered with
```

The **signature** is the trust anchor. The server recomputes it and checks it
matches. Edit the payload (say, change `user42` to `admin`) and the signature no
longer matches — the token is rejected.

### 2.2 What "HS256/384/512" means

The `alg` header says **which algorithm** signed the token. `HS*` is the
**HMAC-SHA** family — *symmetric* signing:

| Algorithm | Hash        | Key model          |
|-----------|-------------|--------------------|
| HS256     | HMAC-SHA-256| one shared secret  |
| HS384     | HMAC-SHA-384| one shared secret  |
| HS512     | HMAC-SHA-512| one shared secret  |

- **Symmetric (HS\*)** — the *same secret* signs **and** verifies. Whoever can
  verify can also forge. The number (256/384/512) is just the hash size; 256 is
  the common default.
- **Asymmetric (RS\*/ES\*)** — a *private* key signs, a *public* key verifies.
  Verifiers cannot forge. This is the "RS256/JWKS" option in the design.

### 2.3 Why "algorithm allow-listed" is a real security control

The token *itself* declares its algorithm in its header. A naive verifier just
trusts that field — which enables two classic forgeries:

1. **`alg: none` attack.** Attacker sends `{"alg":"none"}` with no signature. A
   naive library skips verification → forged token accepted.
2. **Algorithm confusion (RS→HS).** Server expects RS256 (asymmetric). Attacker
   crafts a token claiming `HS256` and signs it using the server's *public* key
   as the HMAC secret. The server verifies with that public key — which is, by
   definition, public → forged token accepted.

**Allow-listing** closes both: the server decides up front *"I only accept HS256,
HS384, or HS512; anything else — including `none` or `RS256` — is rejected before
any verification."* In our code:

```go
jwt.Parse(tokenStr, keyFunc,
    jwt.WithValidMethods([]string{"HS256", "HS384", "HS512"}))
```

So **"HS256/384/512, algorithm allow-listed"** = *we accept these three HMAC
variants and explicitly reject every other algorithm a token might claim.* A
one-line control that closes a whole class of token-forgery attacks.

---

## Part 3 — How to Choose (the decision part)

There is no universal "perfect." The right choice falls out of a few questions
about **who calls you** and **what they may do**.

### 3.1 Authentication: who is calling?

| If the caller is…                       | Use                     | Why |
|-----------------------------------------|-------------------------|-----|
| Another service / machine (S2S)         | **API key** or mTLS     | Simple, long-lived, no user context |
| A logged-in human via your own frontend | **JWT** (short-lived)   | Carries identity + expiry; stateless to verify |
| A third-party app on a user's behalf    | **OAuth2 / OIDC** (issues JWTs) | Delegated consent, scopes, rotation |

**JWT sub-decision — symmetric (HS) vs asymmetric (RS):**

- **HS256 (what we have now)** — fine when the *same trust domain* issues and
  verifies tokens (your auth service and your gateway share one secret).
  Simplest to operate.
- **RS256 / JWKS** — needed when issuer and verifier are *different parties*, you
  want key rotation without redeploying verifiers, or you use an external
  identity provider (Auth0, Okta, Cognito, Keycloak). The gateway fetches the
  issuer's *public* keys and never holds a forgeable secret.

> **Rule of thumb:** one team / one trust boundary → **HS256 is enough**.
> Multiple parties or an external IdP → **go asymmetric (RS256 + JWKS)**.

### 3.2 Authorization: what may they do?

Our current design **defers this to the backends** — the gateway authenticates
but doesn't yet enforce permissions. Models, simplest first:

| Model              | Idea                                      | Use when |
|--------------------|-------------------------------------------|----------|
| **Allow / deny**   | Authenticated = allowed                   | Tiny apps, internal tools |
| **RBAC** (roles)   | Users have roles; routes require roles    | Most apps — `admin`/`editor`/`viewer` |
| **Scopes** (OAuth) | Token carries `read:users`, `write:orders`| Third-party / API products |
| **ABAC / policy**  | Rules over attributes (OPA, Cedar)        | Complex, fine-grained, multi-tenant |

**Where to enforce:**

- **At the gateway** — coarse checks (does the token hold the `admin` role for
  `/admin/*`?). Centralized; keeps backends simpler.
- **At the backend** — fine-grained checks ("can *this* user edit *this specific*
  order?"). Backends own their data semantics.

> A healthy common split: **gateway does authentication + coarse (role/scope)
> authorization; backends do fine-grained, resource-level authorization.**

### 3.3 A practical decision path for this gateway

1. **Mostly service-to-service traffic?** → API keys are enough to start. *(have it)*
2. **End users via your own frontend + your own auth service?** → HS256 JWT. *(have it)*
3. **External identity provider, or third-party API consumers?** → add RS256/JWKS. *(roadmap)*
4. **Need permissions, not just identity?** → start with per-route role/scope
   checks at the gateway; push resource-level rules into backends. *(roadmap)*

**Guiding principle:** start at the **simplest tier that matches your actual
callers and trust boundaries**, and add asymmetric keys / richer authorization
only when a concrete requirement forces it. Over-building auth early is a common
source of complexity you don't yet need.

---

## Part 4 — Decision Worksheet

Fill these in to lock the auth design. (Replace the `[ ]` you pick.)

**Q1. Who are the primary callers?** *(pick all that apply)*
- [ ] Other internal services (machine-to-machine)
- [ ] Logged-in human users via our own frontend
- [ ] Third-party developers / external API consumers

**Q2. Do we have (or want) an external identity provider?**
(Auth0 / Okta / Cognito / Keycloak / Google, etc.)
- [ ] No — we issue our own tokens within one trust domain → **HS256 is enough**
- [ ] Yes / probably later → **plan for RS256 + JWKS**

**Q3. What authorization granularity do we need at the gateway?**
- [ ] None yet — authenticated is enough; backends decide the rest
- [ ] Coarse — per-route **role** or **scope** gates (e.g. `/admin/*` needs `admin`)
- [ ] Fine-grained at the edge — resource-level rules (heavier; usually backend's job)

**Q4. Credential types to accept on protected routes?** *(pick all that apply)*
- [ ] API keys (`X-API-Key`)
- [ ] JWT Bearer tokens
- [ ] Both (API keys for services, JWT for users)

**Q5. For GA, is HMAC-only JWT acceptable?** *(updates open-question #3)*
- [ ] Yes — HS256 only for now
- [ ] No — we need RS256/JWKS in the first release

### My recommendation (until you decide otherwise)

For a first release with a single team and one trust boundary:
**API keys for service-to-service + HS256 JWT for users, gateway authenticates
only, backends own fine-grained authorization.** Add RS256/JWKS the moment an
external IdP or third-party consumers enter the picture. This is what the current
design implements, so picking it means **zero rework**.

---

*Related: [technical-design.md](./technical-design.md) §8 (Authentication),
§7 (Routing & Reverse Proxy), §13 (Roadmap), §14 (Open Questions).*
