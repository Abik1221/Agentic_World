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

## 8. Operator logins (the admin panel and the tracing dashboard)

Both are provisioned entirely from **GitHub repository secrets**. Nothing here is a
literal in the repo, deliberately: anything committed is in the history of every
clone, permanently, on a platform that moves real USDC.

### 8.1 Arena admin

Admin rights are granted by `ADMIN_USER_IDS`, a list of user **public ids** checked
by `auth.IsAdmin`. That list cannot name an account that does not exist yet, so the
first admin on a fresh deployment previously had to be created by hand against the
database — a CI/CD deploy could never produce a usable one.

The server now seeds it at boot (`internal/seedadmin`, called from `cmd/server`)
when both secrets are present. The account's public id is **deterministic**, so
`ADMIN_USER_IDS` can name it *before* it exists:

| Secret                | Required | Notes                                                   |
| --------------------- | -------- | ------------------------------------------------------- |
| `SEED_ADMIN_EMAIL`    | yes      | what you type to log in                                 |
| `SEED_ADMIN_PASSWORD` | yes      | ≥ 8 and ≤ 72 bytes (bcrypt truncates past 72)           |
| `SEED_ADMIN_USER_ID`  | no       | defaults to `usr_pyyoladmin`                            |
| `ADMIN_USER_IDS`      | no       | defaults to `usr_pyyoladmin`; set it to add more admins |

Both `SEED_ADMIN_*` empty ⇒ nothing is seeded, and the deployment is unchanged.

Properties worth knowing:

- **Idempotent.** Every deploy re-runs it; you get one account. An existing account
  has its password reset and its status reactivated — which is the point, because
  the reason you reach for this is usually "I cannot get in".
- **Seeding ≠ admin.** The account is created either way; it only holds admin
  rights if its id is in `ADMIN_USER_IDS`. Boot logs `in_admin_allowlist` and warns
  loudly when it is false, because a login that works and can see nothing is the
  confusing failure.
- **No agent is created.** An operator account is for operating, and an admin who
  also owns a competing agent is a conflict nobody needs.
- The password is hashed through `identity.HashPassword`, i.e. bcrypt over an HMAC
  with the server's `API_KEY_PEPPER`. Change the pepper and every seeded password
  stops verifying — the next deploy re-seeds and fixes it.

An internal-style login without a dotted domain (`pyyol@admin`) works. Public
**sign-up** still requires a real dotted domain; only the login lookup accepts the
address exactly as stored, so an address like that can exist only if you seeded it.

### 8.2 Tracing dashboard (Pyyol Lens)

Already wired — it needs secrets, not code. `deploy-tracing.yml` fails closed if
the password or session secret is missing.

| Secret                      | Required | Notes                          |
| --------------------------- | -------- | ------------------------------ |
| `PYYOL_LENS_AUTH_USER`      | no       | defaults to `admin`            |
| `PYYOL_LENS_AUTH_PASSWORD`  | yes      | **unset ⇒ the dashboard is OPEN** |
| `PYYOL_LENS_SESSION_SECRET` | yes      | ≥ 16 chars; signs the session  |

⚠️ `PYYOL_LENS_AUTH_PASSWORD` unset means **no login at all** — the panel is a
documented dev/behind-VPN mode. It shows agent decisions and reasoning, so treat an
unset password as a disclosure, not a convenience. The workflow's `:?` guards make
this impossible to do by accident through CI; it is only reachable by running the
compose file by hand.

### 8.3 Rotating either password

Change the secret and re-run the deploy. The arena re-seeds on boot; the Lens reads
its password from the environment on every check, and its session cookies rotate
automatically because the signing fallback is derived from the password.
