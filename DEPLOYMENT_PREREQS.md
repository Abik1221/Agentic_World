# Deployment prerequisites (new/changed this hardening cycle)

The security fixes this cycle **fail closed in production**: several services now
*refuse to boot* (or reject traffic) when required secrets are missing. Set these
before deploying, or prod will not come up. Keys marked **MUST MATCH** have to be
identical across the services listed.

## 1. Platform bus (Admin ⇄ Arena) — Ed25519 keypair
Generate one pair: in `backend/`, `go run ./cmd/platform-bus-keygen`.

| Service | Env | Notes |
|---|---|---|
| Arena | `PLATFORM_ENGINE_PRIVATE_KEY` | engine signs events |
| Arena | `PLATFORM_ADMIN_PUBLIC_KEY` | **MUST MATCH** admin's private |
| Admin | `PLATFORM_ADMIN_PRIVATE_KEY` | admin signs config; **MUST MATCH** arena's admin-public |
| Admin | `PLATFORM_ENGINE_PUBLIC_KEY` | **MUST MATCH** arena's engine-private |

The admin **refuses to boot in prod** if `PLATFORM_ADMIN_PRIVATE_KEY` or
`PLATFORM_ENGINE_PUBLIC_KEY` is empty (would publish unsigned config / accept
forged events). A malformed key is also fatal in prod.

## 2. Shared Redis (config bus + event stream)
Admin and Arena **MUST use the same Redis instance AND DB index** — the config
snapshot (`platform:config:*`) and event stream (`platform:events`) are shared
keys. A split DB silently breaks suspensions/economy propagation (the arena reads
a stale/empty snapshot). *(This bit us locally: admin on db1, arena on db0.)*

## 3. Admin JWT secret
`JWT_SECRET` (admin) — **≥32 chars, not the dev default.** The admin refuses to
boot in prod on the insecure default / empty / <32 chars (JWTs would be forgeable).
Refresh tokens are now HttpOnly cookies; the admin UI + API must be same-origin
(or CORS-with-credentials for `CORS_ORIGIN`).

## 4. Pyyol Lens query-api gate — shared secret
The query-api now requires `X-Pyyol-Key` on every `/v1` route and **refuses to
boot in prod without it**.

| Service | Env | Notes |
|---|---|---|
| Tracing query-api | `QUERY_API_KEY` | required in prod |
| Admin | `LENS_API_KEY` | **MUST MATCH** `QUERY_API_KEY` (admin dashboards read Lens) |
| Tracing web | `PYYOL_LENS_API_KEY` | **MUST MATCH** `QUERY_API_KEY` (direct-fetch path) |
| Tracing ingest | `INGEST_API_KEY` | now fail-closed: empty key rejects all ingest |

`LENS_ORG_ID` (admin) **MUST EQUAL** the arena's `PYYOL_LENS_ORG`.

## 5. ClickHouse migrations — now automatic
The processor applies ClickHouse migrations on boot (version-tracked,
idempotent). **No manual migration step** — a pre-existing volume gets the
benchmark-token columns automatically. Postgres (arena/admin) migrations
auto-apply as before.

## 6. Optional / deliberately-off flags (leave unset unless intended)
- `RANKED_AUTODRIVE` (arena) — auto-drives real staked matches. Off by default.
- `AUTOPLAY_ENABLED` (arena) — background auto-play reconciler. Off by default.
- `EMAIL_DELIVERY_ENABLED` (arena) — leave **off** until a mailer is wired;
  magic-link sign-in returns 503 in prod without it (by design — no mailer yet).
- P-Index v2 config: seeded inactive; flip with the documented 2-line SQL when ready.

## 7. Known non-blockers (tracked, not deployment gates)
- Admin "send notification" doesn't deliver (no channel/push endpoint yet).
- Tracing control-api endpoints (Alerts/Settings/etc.) are stubs — their nav
  entries are hidden; routes exist but return empty.
