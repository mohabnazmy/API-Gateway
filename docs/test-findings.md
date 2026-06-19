# Edge-Case Test Findings

Adversarial unit + integration tests probing where the gateway can break.
Generated alongside the test files under `internal/**/*_edge_test.go`,
`*_test.go`, and the concurrency/registry tests. Run them with:

```bash
go test -race ./...
```

All tests currently **pass** — the ones covering weaknesses are *characterization*
tests that pin the current (imperfect) behavior so it can't regress silently and
is visible here. Each is tagged `WEAKNESS` in the source.

---

## Weaknesses found

| # | Severity | Area | Issue |
|---|----------|------|-------|
| W1 | **High** | Rate limiting | Spoofed `X-Forwarded-For` bypasses the limit entirely. |
| W2 | **Medium** | Routing / security | Request paths with `..` / `.` are matched and forwarded **unnormalized**. |
| W3 | **Medium** | Resilience | No per-upstream request/dial timeout; a slow upstream ties up the gateway. |
| W4 | Low | Auth | `Bearer` scheme matched case-sensitively (RFC 6750 says case-insensitive). |
| W5 | Low | Routing | Disallowed HTTP method returns `404`, not `405 Method Not Allowed`. |
| W6 | Low | Config | Duplicate route names accepted → collide as metric labels. |
| W7 | Low | Config | Duplicate/overlapping `path_prefix` accepted → second route silently shadowed. |
| W8 | Low | Auth | API-key check is a plain map lookup (not constant-time) — minor timing side-channel. |

### W1 — Rate-limit bypass via spoofed XFF · *High*
`internal/middleware/ratelimit.go` keys the limiter on `clientIP`, which trusts
`X-Forwarded-For` unconditionally. A caller sending a unique XFF per request gets
a fresh token bucket each time.
**Evidence:** `TestRateLimitBypassedBySpoofedXFF` — 10/10 requests bypass a
`burst:1` limit using `X-Forwarded-For: 198.51.100.{i}`.
**Fix:** only honor XFF from a configured set of trusted proxy CIDRs; otherwise
use `RemoteAddr`. (This is open question "trusted-proxy model" in the design.)

### W2 — Unnormalized path forwarding · *Medium*
The gateway matches and proxies the raw request path without collapsing `.`/`..`
segments.
**Evidence:** `TestDotSegmentsForwardedUnnormalized` — `/public/../admin` matches
the `/public` route and the backend receives `/public/../admin` verbatim. If an
upstream resolves `..`, traffic admitted under a public route's prefix could
reach a different area; auth decisions made on the raw path can also diverge from
what the upstream ultimately serves.
**Fix:** clean the path before matching (or reject dot-segments / encoded slashes
`%2F`). Note the reverse proxy also drops `RawPath` (`pr.Out.URL.RawPath = ""`),
re-deriving the escaped path from `Path`.

### W3 — No upstream timeout · *Medium*
Each route's `httputil.ReverseProxy` uses the default transport with no dial or
response-header timeout. Only the server-side `WriteTimeout` bounds a slow
upstream, and that's global, not per-route. A hung upstream can exhaust
connections.
**Fix:** give the reverse proxy a `*http.Transport` with `DialContext` +
`ResponseHeaderTimeout` (and ideally a per-route timeout).
*(Not yet covered by a test — would require a deliberately slow backend.)*

### W4 — Case-sensitive Bearer scheme · *Low*
`validJWT` checks the literal prefix `"Bearer "`. A valid token sent as
`Authorization: bearer <token>` is rejected.
**Evidence:** `TestAuthAttacksAndEnforcement/lowercase_bearer_scheme` → `401`.
**Fix:** compare the scheme case-insensitively.

### W5 — 404 instead of 405 · *Low*
A path that exists but whose method isn't in the route's allow-list returns
`404`, giving clients no "method not allowed" signal.
**Evidence:** `TestDisallowedMethodReturns404Not405`.
**Fix:** when a prefix matches but the method doesn't, return `405` with an
`Allow` header.

### W6 / W7 — No route uniqueness checks · *Low*
`config.validate` doesn't enforce unique `name` or non-overlapping `path_prefix`.
Duplicate names collide as Prometheus labels; a duplicate prefix makes the
second route unreachable (stable sort keeps the first), silently.
**Evidence:** `TestLoadAcceptsDuplicateRouteNames`, `TestLoadAcceptsDuplicatePathPrefix`.
**Fix:** reject (or at least warn on) duplicate names and overlapping prefixes.

### W8 — Non-constant-time API-key check · *Low*
Keys are matched via `map[string]struct{}` lookup. Already noted in the design;
mitigated in Phase 2 when keys move to hashed store lookups.

---

## Strengths confirmed

These adversarial cases were **rejected/handled correctly**:

- **JWT algorithm allow-listing** defeats `alg=none` and RS→HS algorithm
  confusion (`TestAuthAttacksAndEnforcement`).
- **Expired** and **wrong-secret** JWTs are rejected.
- **Per-route credential scoping**: a route accepting only `jwt` rejects a valid
  API key, and vice-versa.
- **Atomic config swap**: a failed `registry.Load` leaves the previous snapshot
  intact (`TestFailedLoadKeepsPreviousSnapshot`); an unknown algorithm fails the
  load instead of half-applying.
- **Concurrency**: `go test -race` is clean across all packages, including
  concurrent registry reads + reloads and concurrent limiter access. The token
  bucket admits **exactly** its burst under 200 racing goroutines
  (`TestTokenBucketBurstExactUnderConcurrency`).
- **Resilience**: panics → `500` (`TestRecoverConvertsPanicTo500`), upstream
  failures → `502`, unmatched paths → `404`.
- **Routing correctness**: longest-prefix wins, segment-boundary matching,
  prefix stripping (incl. exact-match → `/`), upstream base-path joining, query
  preservation, and method fall-through across same-prefix routes.

---

## Known limitations (by design, not bugs)

- **Per-instance rate-limit state** — N replicas ≈ N× the global limit
  (distributed limiting is a roadmap item).
- **Anonymous API keys** — keys aren't yet tied to a consumer (Phase 2).
- **No request body-size limit** beyond server timeouts.

## Suggested priority

1. **W1** (rate-limit bypass) and **W2** (path normalization) — security-relevant,
   should be fixed before any untrusted exposure.
2. **W3** (upstream timeouts) — resilience under bad upstreams.
3. **W4–W7** — correctness/usability polish.
