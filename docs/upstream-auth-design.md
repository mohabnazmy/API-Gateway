# Design: Pluggable upstream authentication

**Status:** proposal · **Supersedes:** the GCP-specific `IDTokenSource` wiring added in
PR #15 (`feat/upstream-oidc`).

## Goal

Make the gateway **vendor-neutral**. Calling a private upstream is a common need —
private Cloud Run, private AWS API Gateway / Lambda URLs, an internal service behind
mTLS, or any backend that wants a bearer token. Today only **Google OIDC** is wired in,
through an interface (`proxy.IDTokenSource`) that is shaped specifically like Google's
"mint a token for an audience" model. We want GCP OIDC to be **one configurable option
among many**, not the foundation — so a user on AWS, on-prem, or any cloud configures the
mode that fits their backend, and the core gateway depends on none of them.

## The core question: can one abstraction cover both GCP tokens *and* SigV4?

These two look mechanically different:

| Mode | What it does to the request |
|------|------------------------------|
| `google_oidc` / `bearer` / `oauth2` | **Adds a header**: `Authorization: Bearer <token>` |
| `aws_sigv4` | **Signs the whole request**: reads method + path + query + body + a few headers, computes a signature, and adds `Authorization` + `X-Amz-Date` (and a body hash) |
| `mtls` | **Nothing to the request** — presents a client certificate at the **TLS/transport** layer |

The unifying insight: in every case the strategy's job is to **mutate the outbound
request just before it is sent** (or, for mTLS, configure the transport). "Attach a token"
and "sign the request" are both just *"given the outbound `*http.Request`, do what you
need to it."* So one interface covers all header/signature modes:

```go
// Package upstreamauth. The proxy depends ONLY on this — it knows nothing about
// Google, AWS, OAuth, etc.
type Authenticator interface {
    // Apply mutates the outbound request to authenticate it to the upstream.
    // Called on every proxied request, after the path has been rewritten.
    Apply(ctx context.Context, out *http.Request) error
}
```

- **`bearer`** → `out.Header.Set("Authorization", "Bearer "+token)`. Done.
- **`google_oidc`** → fetch/cache the Google ID token (audience = upstream origin), then
  the same header set. This is the *current* code, moved behind `Apply`.
- **`oauth2_client_credentials`** → fetch/cache a token from any `token_url`, same header.
- **`aws_sigv4`** → read `out` (method, URL, body, headers), compute the SigV4 signature,
  set `Authorization` + `X-Amz-Date`. It needs the **body**, which is the one wrinkle ↓.

So yes — GCP and SigV4 coexist under one interface. They are different *implementations*
of "authenticate this outbound request," selected per route by config.

### The SigV4 wrinkle (and why it shapes the design)

A bearer token ignores the request body; **SigV4 hashes it**. `httputil.ReverseProxy`
streams the body, and signing needs the bytes up front. So for signing modes the strategy
must buffer the body, hash it, sign, and restore the body for forwarding. Two consequences
the design must honor:

1. `Apply` runs at a point where the **full outbound request is known** (final path,
   query, body) — i.e. inside the proxy's `Rewrite`/director step, not before.
2. Signing strategies declare they need the body so we only buffer when required (a plain
   bearer never pays the buffering cost). We expose this via an optional
   `NeedsBody() bool` on the strategy, or simply document that signing strategies buffer
   `out.Body` themselves.

mTLS is the exception that does **not** fit `Apply` — it is a transport concern. It is
configured by building a per-route `*http.Transport` with a client cert, handled where the
reverse proxy's `Transport` is assigned (see *Transport-level modes* below), not via the
header interface.

## Configuration schema

Replace the bare string `upstream_auth: "google_oidc"` with a typed block so each mode
carries its own config:

```jsonc
// model.Route
"upstream_auth": {
  "type": "none"            // default; no upstream auth
  // "type": "bearer",      "token_ref": "env:BACKEND_TOKEN"
  // "type": "google_oidc"  (audience defaults to the upstream origin)
  // "type": "oauth2_client_credentials",
  //   "token_url": "https://issuer/oauth/token",
  //   "client_id": "...", "client_secret_ref": "env:OAUTH_SECRET",
  //   "scopes": ["api.read"], "audience": "https://api.internal"
  // "type": "aws_sigv4",   "region": "us-east-1", "service": "execute-api"
  //                        (credentials from the standard AWS chain: env / role)
  // "type": "mtls",        "cert_ref": "file:/etc/certs/up.crt",
  //                        "key_ref": "file:/etc/certs/up.key"
}
```

Backward compatibility: accept the legacy string form (`"upstream_auth":"google_oidc"`)
and normalize it to `{ "type": "google_oidc" }` during config decode, so PR #15's format
and existing `.env` files keep working.

**Secret references (`*_ref`)** keep secrets out of the route JSON: `env:NAME` reads an
env var, `file:/path` reads a file. This generalizes cleanly to the config store / admin
API in later phases (a `secret://` indirection) instead of inlining secrets.

## Registry (selection by name)

A small factory map mirrors how `ratelimit.New` already picks an algorithm by name — the
codebase precedent:

```go
// upstreamauth.New builds an Authenticator from a route's config, or nil for "none".
func New(cfg model.UpstreamAuth) (Authenticator, error) {
    switch cfg.Type {
    case "", "none":      return nil, nil
    case "bearer":        return newBearer(cfg)
    case "google_oidc":   return newGoogleOIDC(cfg)      // wraps today's gcpauth
    case "oauth2_client_credentials": return newOAuth2(cfg)
    case "aws_sigv4":     return newSigV4(cfg)
    case "mtls":          return nil, errMTLSIsTransport // handled at transport layer
    default:              return nil, fmt.Errorf("unknown upstream_auth type %q", cfg.Type)
    }
}
```

Each provider lives in its own file/sub-package and is the *only* place that imports its
SDK — so a non-AWS user never compiles AWS code paths in their hot path, and the proxy
core imports none of them.

## How the proxy changes

```
            ┌─────────────────────────────────────────────┐
            │ proxy.compile(route)                         │
            │   authn = upstreamauth.New(route.UpstreamAuth)│  ← built once per route
            └───────────────────────┬─────────────────────┘
                                    │
   request ─▶ Rewrite(pr) ─▶ rewrite path ─▶ authn.Apply(ctx, pr.Out) ─▶ upstream
                                              (header/sign modes)
                          Transport = mTLS transport (transport modes)
```

- `proxy.go` drops the `IDTokenSource` interface and the `google_oidc` `switch`; it gains
  a generic `authn Authenticator` field on `Entry`, built in `compile` via
  `upstreamauth.New`. `Rewrite` calls `authn.Apply(...)` when `authn != nil`.
- `model.go`'s `UpstreamAuth string` becomes `UpstreamAuth UpstreamAuth` (a struct), with
  a custom `UnmarshalJSON` that accepts the legacy string form.
- `main.go` no longer constructs `gcpauth.NewIDTokenSource()` directly; the Google source
  is created lazily by `newGoogleOIDC` only when a route asks for it. `proxy.Options` loses
  its `IDTokenSource` field.

### Transport-level modes (mTLS)

mTLS does not touch headers, so it is selected where the proxy assigns `Transport`: when a
route is `type: "mtls"`, `compile` clones the base transport and sets
`TLSClientConfig.Certificates`. This keeps the `Apply` interface clean and puts the one
transport-shaped mode where transports are built.

## Migration of the existing GCP code

The work added in PR #15 is **kept, not thrown away** — only re-homed:

| Today (PR #15) | After |
|----------------|-------|
| `internal/gcpauth/idtoken.go` | unchanged — still the metadata-server token source |
| `proxy.IDTokenSource` interface | removed; replaced by `upstreamauth.Authenticator` |
| `proxy` hardcodes `google_oidc` | `upstreamauth/google.go` wraps `gcpauth` behind `Apply` |
| `proxy.Options.IDTokenSource` | removed |
| `model.Route.UpstreamAuth string` | `model.Route.UpstreamAuth` struct (+ legacy decode) |

Behavior for an existing `google_oidc` route is **identical**; only the wiring moves.

## Phasing

1. **Abstraction + port GCP** — introduce `upstreamauth.Authenticator` + registry, move
   `google_oidc` onto it, legacy-string decode, delete the GCP-specific proxy interface.
   *No new providers.* Net behavior unchanged; gateway is now provider-neutral by design.
2. **`bearer` + `oauth2_client_credentials`** — the two most broadly useful, cover most
   non-GCP private backends (Auth0/Okta/Keycloak/Azure AD and any static-token backend).
3. **`aws_sigv4`** — implement request signing + body buffering for AWS-private targets.
4. **`mtls`** — transport-level client certificates.

Each phase is independently shippable and testable. Phase 1 is the one that resolves the
"GCP-only" concern; the rest broaden coverage.

## Non-goals

- Authenticating **clients to the gateway** (inbound JWT / API key) — that is the existing
  `AuthPolicy` and is unchanged. This doc is only about the gateway → upstream hop.
- A plugin system loading external binaries. Providers are compiled-in Go, selected by
  config — same model as rate-limit algorithms.
```
