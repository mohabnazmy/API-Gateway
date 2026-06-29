# Design: Phase 2 — Config store + hot-reload

**Status:** implemented (D1–D4 as recommended) · **Implements:**
technical-design.md §11 (store) + §12 (hot-reload). Built **TDD**.

## Goal

Stop loading routes once from `GATEWAY_ROUTES` at startup. Make an embedded
**SQLite** database the durable source of truth, load it into the existing
immutable `proxy.Snapshot`, and support **hot-reload** — config changes take
effect without a restart. No Admin API yet (that's Phase 3); Phase 2 builds the
store, the registry reload path, and a trigger to exercise it.

What stays the same: the data plane, `proxy`, `ratelimit`, `upstreamauth`, and the
`registry`'s lock-free `Current()` snapshot reads are untouched. `registry` was
already built as the hot-reload basis — Phase 2 feeds it from the store.

## Scope (what ships in Phase 2)

In scope:
- `internal/store` — SQLite repository (migrations, routes CRUD, config version).
- Seeding `GATEWAY_ROUTES` → SQLite on first run (backward compatible).
- `registry.Reload(routes)` + a reload trigger so changes go live without restart.
- Wire into `main` behind `GATEWAY_DB_PATH`.

Deferred to Phase 3 (Admin API): the `plans`, `consumers`, `api_keys`,
`admin_users` tables; REST CRUD; admin auth. We add those tables in a later
migration when the API that uses them lands — not as dead tables now.

## Decisions that want your review

### D1 — Schema subset now, rest later
§11 lists the full target schema. Phase 2 creates only what it uses:
`routes`, `auth_policies`, `rate_limit_policies`, `config_version`. The other
tables arrive in a Phase 3 migration. *(Alternative: create all tables now as
documented. I recommend subset-now to avoid unused schema.)*

### D2 — Persisting `upstream_auth`
The §11 `routes` table predates the `upstream_auth` field added in PR #15, so it
has no column for it. `UpstreamAuth` is a 12-field struct (mode + per-mode
params). I propose a single **`upstream_auth TEXT` JSON column** on `routes`
(lossless, matches how the field already decodes), rather than a normalized
table. *(Normalized buys nothing until the Admin API needs to query by mode.)*

### D3 — Hot-reload trigger in Phase 2
§12's flow is *admin write → bump version → `registry.Reload()`* in-process. With
no Admin API yet, Phase 2 needs another trigger to prove reload works. I propose a
**version-poll loop**: a goroutine reads `config_version` every
`GATEWAY_CONFIG_POLL_INTERVAL` (default `0` = disabled) and reloads on change.
Phase 3 keeps `registry.Reload()` but drives it directly from writes; the poller
stays as the multi-node story (§20) or can be removed. *(Alternative: no trigger
in Phase 2 — load once from the store, add reload in Phase 3. I recommend the
poller so hot-reload is real and tested now.)*

### D4 — Seeding & precedence
On startup, run migrations, then if `routes` is **empty**, import
`GATEWAY_ROUTES` into SQLite (one-time seed). After that the **store wins** and
`GATEWAY_ROUTES` is ignored. This keeps every current deployment working with no
config change, and makes the DB authoritative. A missing/empty DB with no
`GATEWAY_ROUTES` is valid (zero routes).

## Repository interface (`internal/store`)

```go
type Store interface {
    ListRoutes(ctx context.Context) ([]model.Route, error)
    UpsertRoute(ctx context.Context, r model.Route) error  // insert or update by name
    DeleteRoute(ctx context.Context, name string) (bool, error) // false = not found
    Version(ctx context.Context) (int64, error)            // bumps on every write
    Close() error
}
```

`Open(path string) (*SQLite, error)` returns the SQLite implementation. All
writes bump `config_version` in the **same transaction**, so a reader/poller never
sees a version newer than the data it would read. DB access lives only here, so a
future Postgres/etcd swap (§20) doesn't touch the data plane.

Mapping `model.Route` ↔ rows: `routes` (name, path_prefix, upstream, strip_prefix,
methods JSON, upstream_auth JSON, enabled, timestamps) + `auth_policies` +
`rate_limit_policies`, joined on `route_id`. `ListRoutes` returns enabled routes
ordered by name (deterministic).

## Migrations

Embedded, versioned SQL run at `Open`. A `schema_migrations` table records the
applied version; each migration is an idempotent step applied in order inside a
transaction. Phase 2 ships migration `0001_init`. Pure-Go driver
**`modernc.org/sqlite`** so `CGO_ENABLED=0` static/distroless builds keep working.

> **Build note:** `modernc.org/sqlite` raised the module's Go directive to
> **1.25**. The deploy Dockerfiles (`deploy/Dockerfile`,
> `deploy/autodeploy/deployer.sh`) must bump `golang:1.24-alpine → 1.25-alpine`.
> CI reads the version from `go.mod`, so it tracks automatically.

## Config (new env)

| Variable | Default | Description |
|----------|---------|-------------|
| `GATEWAY_DB_PATH` | `./gateway.db` | SQLite file path (mount on a volume in Cloud Run). |
| `GATEWAY_CONFIG_POLL_INTERVAL` | `0` | Hot-reload poll interval (e.g. `5s`); `0` disables polling. |

## Hot-reload path

```
(Phase 2) poll loop: read config_version
            │ changed?
            ▼
registry.Reload(store.ListRoutes())
   │ build a new immutable Snapshot (sorted routes + compiled proxies + limiters)
   │ atomic.Pointer swap: Current → new
   ▼
in-flight requests keep the old snapshot; new requests see the new one
(old snapshot's limiters/transports closed after swap)
```

Reload is all-or-nothing: if the new config fails to compile, the swap is skipped
and the previous snapshot keeps serving (logged). Phase 3 calls `Reload` directly
on each admin write instead of polling.

## TDD plan

Store tests first (`internal/store/sqlite_test.go`), then implement to green:
- empty store → no routes, version 0
- upsert round-trips every field incl. `upstream_auth`, `Methods`, nested policies
- upsert updates in place (no duplicate); clearing a slice round-trips as `nil`
- delete: true when present, false when missing; version bumps only on success
- list ordered by name; data + version persist across reopen
- `SeedRoutes` inserts only when empty (idempotent)

Then registry reload tests (compile failure keeps old snapshot; swap on change),
then a main wiring smoke test.

## Out of scope / non-goals

- Admin API, consumers/plans/keys, admin auth → Phase 3.
- Multi-node shared config → §20 (single-node SQLite first).
- Secrets in the DB → still referenced by env (`*_ref`), never stored.
