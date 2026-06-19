# Edge-Case Test Findings

Adversarial unit + integration tests probing where the gateway can break, under
`internal/**/*_edge_test.go` and the concurrency/registry tests. Run with:

```bash
go test -race ./...
```

The initial pass (see git history) surfaced eight weaknesses. **W1–W7 have been
fixed**; the tests below now assert the corrected behavior. W8 is deferred to
Phase 2. Strengths remain verified.

---

## Weaknesses — status

| # | Severity | Issue | Status |
|---|----------|-------|--------|
| W1 | **High** | Spoofed `X-Forwarded-For` bypassed rate limiting | ✅ **Fixed** |
| W2 | **Medium** | Paths with `..`/`.` matched + forwarded unnormalized | ✅ **Fixed** |
| W3 | **Medium** | No per-upstream request/dial timeout | ✅ **Fixed** |
| W4 | Low | `Bearer` scheme matched case-sensitively | ✅ **Fixed** |
| W5 | Low | Disallowed method returned `404`, not `405` | ✅ **Fixed** |
| W6 | Low | Duplicate route names accepted | ✅ **Fixed** |
| W7 | Low | Shadowing duplicate `path_prefix` accepted | ✅ **Fixed** |
| W8 | Low | API-key check not constant-time | ⏳ Deferred to Phase 2 (hashed store) |

### W1 — Trusted-proxy XFF model · *fixed*
`X-Forwarded-For` is now resolved via `middleware.RealIP`, which only trusts XFF
when the immediate peer is in `GATEWAY_TRUSTED_PROXIES` (a list of CIDRs/IPs).
The default trusts no proxy, so XFF is ignored and the client IP is `RemoteAddr`
— a spoofed XFF can't create fresh rate-limit buckets.
**Tests:** `TestSpoofedXFFNoLongerBypassesRateLimit` (1/10 through, the burst),
`TestTrustedProxyXFFIsHonored`, `TestRealIPTrustModel`.

### W2 — Path normalization · *fixed*
`proxy.Resolve` canonicalizes the request path (collapsing `.`/`..` and duplicate
slashes) before matching and forwarding. Traversal that escapes a route's prefix
no longer matches it.
**Tests:** `TestDotSegmentsNormalizedWithinPrefix`,
`TestTraversalEscapingPrefixNoLongerMatches`.

### W3 — Upstream timeouts · *fixed*
Each reverse proxy uses a transport (`proxy.NewTransport`) with a dial timeout
and a `ResponseHeaderTimeout`, configurable via `GATEWAY_UPSTREAM_DIAL_TIMEOUT` /
`GATEWAY_UPSTREAM_RESPONSE_TIMEOUT`. A hung upstream is cut off → `502`.
**Test:** `TestSlowUpstreamTimesOutAs502`.

### W4 — Case-insensitive Bearer · *fixed*
The scheme is compared with `EqualFold`, so `bearer` / `BEARER` work (RFC 6750).
**Tests:** `TestAuthAttacksAndEnforcement/{lowercase,uppercase}_*`.

### W5 — 405 + Allow · *fixed*
When a prefix matches but the method isn't allowed, the gateway returns `405`
with an `Allow` header listing accepted methods.
**Test:** `TestDisallowedMethodReturns405WithAllow`.

### W6 / W7 — Route uniqueness · *fixed*
`config.validate` rejects duplicate route names and same-prefix routes with
overlapping methods (which would silently shadow). Same prefix with disjoint
methods is still allowed.
**Tests:** `TestLoadRejectsDuplicateRouteNames`,
`TestLoadRejectsShadowingPathPrefix`, `TestLoadAcceptsSamePrefixDisjointMethods`.

### W8 — Non-constant-time API-key check · *deferred*
Plain map lookup. Mitigated in Phase 2 when keys move to a hashed store.

---

## Strengths (still verified)

- **JWT alg allow-listing** defeats `alg=none` and RS→HS confusion; expired and
  wrong-secret tokens rejected; per-route credential scoping enforced.
- **Atomic config swap**: a failed `registry.Load` leaves the prior snapshot
  intact; an unknown algorithm fails the load instead of half-applying.
- **Concurrency**: `go test -race` is clean across all packages, including
  concurrent registry reads + reloads and concurrent limiter access. The token
  bucket admits **exactly** its burst under 200 racing goroutines.
- **Resilience**: panics → `500`, upstream failures → `502`, unmatched → `404`.
- **Routing**: longest-prefix wins, segment-boundary matching, prefix stripping,
  upstream base-path joining, query preservation, method fall-through.

## Known limitations (by design)

- **Per-instance rate-limit state** — N replicas ≈ N× the global limit
  (distributed limiting is a roadmap item).
- **Anonymous API keys** — not yet tied to a consumer (Phase 2).
- **No request body-size limit** beyond server timeouts.
