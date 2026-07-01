# Design: Phase 4 — Admin UI (Go html/template + HTMX)

**Status:** proposal (for review before implementation) · **Implements:**
technical-design.md §14. Built **TDD** where practical (handlers/auth; templates
verified via rendered-output assertions + a live smoke test).

## Goal

A server-rendered web UI for the control plane — routes, plans, consumers, API
keys, and a metrics dashboard — served from the **same private admin listener**
as the Admin API. No React, no Node/npm: Go `html/template` (auto-escaping,
XSS-safe) + **HTMX** (one ~14 KB vendored JS file, no build step), embedded via
`embed.FS` so the whole thing stays a single static binary (N6).

The Admin API (Phase 3) already does the work — validation, hot-reload,
transactional writes. Phase 4 adds an HTML presentation layer over the same
`store` + validation; it introduces no new data-plane behavior.

## Scope

In scope: login/logout, a base layout + nav, CRUD screens for routes / plans /
consumers / API keys (incl. copy-key-once + revoke), and a dashboard surfacing
the Prometheus metrics. Delivery via `embed.FS` on the admin listener.

Out of scope (roadmap): admin RBAC (viewer vs editor), audit log, richer
dashboards/charts, i18n.

## Proposed increments (each its own PR, TDD)

- **4a — Scaffolding + auth + routes screen.** `embed.FS`, base layout, vendored
  HTMX asset, **cookie-based browser session** (login/logout), the admin
  middleware extended to accept the session cookie, and the first full CRUD
  screen (routes: list / create / edit / delete via HTMX). Proves the whole
  pattern end-to-end.
- **4b — Plans, consumers, API keys.** The remaining CRUD screens, including
  issue-key (shown once) and revoke.
- **4c — Dashboard.** A metrics overview page (reads the Prometheus registry /
  `/metrics`) + polish (flash messages, empty states).

Recommended order: 4a → 4b → 4c.

## How it fits the existing admin package

The admin listener already serves `/admin/api/*` (JSON). Phase 4 adds HTML routes
on the **same** router/listener:

```
/admin/login            GET  form · POST  authenticate → set cookie → redirect
/admin/logout           POST clear cookie
/admin                  GET  dashboard (4c)
/admin/routes           GET  list · POST create        (HTMX)
/admin/routes/{name}    GET  edit  · PUT/DELETE …       (HTMX fragments)
/admin/plans …  /admin/consumers …  /admin/consumers/{id}/api-keys …
```

UI handlers reuse the same `store.Store` and the 3c validation helpers, returning
**HTML fragments** for HTMX swaps (not JSON). The JSON API stays as-is for
programmatic use; the UI does not go through it.

Files: `internal/admin/web/` (templates `*.gohtml` + `static/htmx.min.js`,
embedded), `internal/admin/ui.go` (handlers), `internal/admin/render.go`
(template set + render helpers).

## Browser auth (the one real new concern)

The Admin API authenticates with a **Bearer** session JWT — fine for `curl`, but a
browser can't attach a header to normal navigations. So the UI carries the same
JWT in an **HttpOnly cookie**:

- Login form POST verifies the password (existing `Service.Login`) and sets the
  JWT as `Set-Cookie: admin_session=…; HttpOnly; SameSite=Strict; Secure` (Secure
  when served over TLS).
- The admin auth middleware is extended to accept the token from **either** the
  `Authorization: Bearer` header (API) **or** the `admin_session` cookie (UI).
- Logout clears the cookie.

**CSRF:** cookies are sent automatically, so state-changing UI requests need CSRF
protection. Plan: `SameSite=Strict` **plus** a double-submit CSRF token embedded
in every form and checked on POST/PUT/DELETE from the UI. (The private listener
already limits exposure; this is defense-in-depth for a surface that reconfigures
the gateway.)

## Decisions that want your review

- **D1 — Increments.** Ship 4a→4c as **three PRs** (recommended) or one?
- **D2 — Browser auth.** HttpOnly `SameSite=Strict` cookie carrying the admin
  JWT, middleware accepts cookie **or** Bearer (recommended). Alternative:
  server-side session store (heavier; drops the stateless model).
- **D3 — CSRF.** `SameSite=Strict` + a double-submit CSRF token on UI mutations
  (recommended), or rely on `SameSite=Strict` alone given the private listener
  (simpler, slightly weaker)?
- **D4 — HTMX asset.** **Vendor** `htmx.min.js` into `web/static/` and embed it
  (recommended — no CDN, offline, fixed version), or load from a CDN (lighter
  repo, adds a runtime dependency + supply-chain surface)?
- **D5 — Reuse the JSON API or separate HTML handlers?** Separate HTML handlers
  returning fragments, reusing the store + validation (recommended, idiomatic
  HTMX), vs. the UI calling the JSON API via HTMX + client templating (not
  server-rendered).

## TDD plan

- Auth: middleware accepts a valid session **cookie**; rejects missing/expired;
  login sets the cookie; logout clears it; CSRF token required on mutations.
- Handlers: routes list renders seeded routes; create/edit/delete via the UI
  round-trip through the store and trigger reload; validation errors render
  inline (422 → error partial); not-found → 404 page.
- Rendering: templates parse at startup (fail fast) and produce the expected
  fragments (assert on key markers, not full HTML).
- Live smoke test per increment (log in, drive the screen in a headed check).

## Non-goals

- No SPA, no JS build step, no CDN (if D4 = vendor). No new data-plane behavior —
  the UI is a thin presentation layer over the Phase 3 control plane.
