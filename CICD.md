# CI/CD — full-stack deployment across the 3 repos

Every repo has a GitHub Actions pipeline that, on push to `main`, tests → builds
→ ships over SSH → `docker compose up` → health-checks. **All config comes from
GitHub Secrets** (set per-repo). All services join one shared Docker network
(`pyyol`) on the host and talk to each other by container name.

## Repos & pipelines
| Repo | Workflow(s) | Deploys |
|---|---|---|
| `Agentic_World` | `deploy-backend.yml` | Arena API + Postgres + Redis (`/opt/agent-arena`) |
| `Agentic_World` | `deploy-tracing.yml` | Pyyol Lens: ClickHouse, NATS, ingest/processor/query/control, web (`/opt/pyyol-lens`) |
| `Super_Admin` | `deploy.yml` | Admin API + SPA + admin Postgres (`/opt/super-admin`) |
| `Pyyol_client` | `deploy-landing.yml` | User Next.js app (`docker run`) |

## Deploy ORDER (first time)
1. **Arena** (`deploy-backend.yml`) — creates the `pyyol` network + `arena-redis`
   (the config bus + `platform:events` live here; admin depends on it).
2. **Tracing** (`deploy-tracing.yml`) — ClickHouse + `pyyol-lens-ingest/-query`.
3. **Super_Admin** (`deploy.yml`) — needs arena-redis + arena-server + pyyol-lens-query.
4. **Pyyol_client** (`deploy-landing.yml`) — browser app; only needs the public arena URL.
After the first round, pushes to each repo redeploy that piece independently.

## How they communicate (on the `pyyol` network, by container name)
```
Pyyol_client (browser) ──public https──▶ api.<domain> ─▶ arena-server:8080
arena-server ─telemetry─▶ pyyol-lens-ingest:8081        (PYYOL_LENS_API_KEY == INGEST_API_KEY)
admin-api ──▶ arena-redis:6379   (config bus + events; SHARED with arena)
admin-api ──▶ arena-server:8080  (signed backfill; PLATFORM_* keys MUST MATCH)
admin-api ──▶ pyyol-lens-query:8082   (LENS_API_KEY == QUERY_API_KEY)
tracing web ──▶ pyyol-lens-query:8082 (PYYOL_LENS_API_KEY == QUERY_API_KEY)
```
Each deploy runs `docker network create pyyol || true`. Postgres/Redis/ClickHouse
stay internal; only the web apps + arena API are published (loopback) for the
host nginx/Caddy to front on subdomains with TLS.

## Reverse proxy (host nginx → loopback ports) — vhosts provided
Copy each vhost to `sites-available`, symlink to `sites-enabled`, and copy
`deploy/nginx/websocket-upgrade.conf` to `/etc/nginx/conf.d/` (defines
`$connection_upgrade` for the WS proxies). Then `certbot --nginx -d <host>`.

| Public | → | Loopback | vhost file |
|---|---|---|---|
| `api.<domain>` | arena API | `:8091` | `deploy/nginx/api.pyyol.com.conf` |
| `<domain>` (user app) | Pyyol_client | `:3000` | `Pyyol_client/deploy/nginx/pyyol.com.conf` |
| `admin.<domain>` | admin-web | `:8095` | `Super_Admin/deploy/nginx/admin.pyyol.com.conf` |
| `trace.<domain>` | Pyyol Eye | `:3100` loopback + **`:3110` public** | `tracing/deploy/nginx/trace.pyyol.com.conf` (named vhost, never `default_server`) + Cloudflare origin-rule to `:3110` so Mega Hub can keep `:80` |

Replace `pyyol.com` with your domain in each file before enabling.

## GitHub Secrets — set per repo

### Common (all repos) — SSH target
`DEPLOY_SSH_KEY` (private key; public half in the server's `authorized_keys`),
`SSH_PORT` (default 22). Host/user secret **names differ per repo**:
- `Agentic_World` (backend+tracing) & `Super_Admin`: `SSH_HOST`, `SSH_USER`
- `Pyyol_client`: `SERVER_HOST`, `SERVER_USER`

### `Agentic_World` — arena (`deploy-backend.yml`)
`POSTGRES_PASSWORD`, `JWT_SIGNING_KEY`, `API_KEY_PEPPER`, `AGENT_ENDPOINT_SECRET_KEY`,
`ADMIN_USER_IDS`, `PLATFORM_ENGINE_PRIVATE_KEY`, `PLATFORM_ADMIN_PUBLIC_KEY`,
`PYYOL_LENS_ENABLED`(=true), `PYYOL_LENS_API_KEY`(==tracing `INGEST_API_KEY`),
`PYYOL_LENS_ORG`, `BASE_URL`, `CORS_ALLOWED_ORIGINS`, `ARENA_PORT`, `STRIPE_*` (optional).

**`ENV` is mandatory and must be exactly `prod` or `staging` in a live deployment.**
The arena now REFUSES TO BOOT on any other value, which is deliberate. `ENV` is the
switch every safety gate hangs off: `ALLOW_MINT` (the free-coin test endpoint)
defaults ON when the env is not prod-like, and the SSRF guards on agent endpoint
verification are only refused when it is. `ENV=production` — the natural spelling, and
wrong — or an unset `ENV` (defaults to `local`) used to boot happily with a mint
endpoint open to anyone and the SSRF rails down, with no error and a healthy-looking
deployment. A process that will not start gets fixed in minutes; a silently permissive
one is never noticed.

**`PYYOL_LENS_QUERY_ENDPOINT`** (e.g. `http://pyyol-lens-query:8082`) enables the
developer trace view (`/v1/developer/traces`). Unset ⇒ that route returns 503 and
nothing else is affected; it is a read-only convenience and never blocks play.

**`MIN_STAKE_USD_CENTS`** (default `500`) is the paid-table floor the admin stake
editor enforces. Leave it at $5 unless you intend cheaper tables; `0` removes the
floor entirely and is for sandbox deployments only.
**Registry (image push/pull):** `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN` (a Docker Hub
PAT with read+write), `DOCKERHUB_REPO` (e.g. `nahi12/pyyol_backend`) — set in ALL THREE
repos. **One private repo holds every service image, one tag per service** so the server
never builds (big win on a small VPS); CI builds + pushes, the server logs in, pulls the
pinned `<sha>` tag, logs out:
`arena-<sha>` (backend) · `admin-api-<sha>` + `admin-web-<sha>` (Super_Admin) ·
`landing-<sha>` (Pyyol_client) · `lens-backend-<sha>` + `lens-web-<sha>` (tracing),
each with a moving `-latest`. The VPS never builds these — CI pushes, the server pulls.
**DNS/Cloudflare (used by `dns.yml`):** `CLOUDFLARE_API_TOKEN`, `SERVER_IP`, `CLOUDFLARE_ZONE_ID`.

### `Agentic_World` — tracing (`deploy-tracing.yml`)
`QUERY_API_KEY`, `INGEST_API_KEY`, `PYYOL_LENS_AUTH_PASSWORD`,
`PYYOL_LENS_SESSION_SECRET`, `PYYOL_LENS_AUTH_USER` (opt), `PYYOL_LENS_DEFAULT_ORG_ID` (opt).

### `Agentic_World` — WALLET / crypto (arena, `deploy-backend.yml`)
Set these in the **Agentic_World** repo. The coin economy (mint/stake/settle)
works without any of them; these enable **real-money** rails + Privy login.
- **Privy login:** `PRIVY_APP_ID`, `PRIVY_VERIFICATION_KEY` (empty ⇒ email/password only).
- **Solana USDC deposits (ALL four required to turn deposits on):** `SOLANA_RPC_URL`
  (the **free public RPC** `https://api.mainnet-beta.solana.com` is fine at launch;
  swap to a paid RPC only if you hit rate limits), `SOLANA_USDC_MINT` (mainnet USDC mint),
  `SOLANA_PLATFORM_OWNER` (platform wallet address), `SOLANA_PLATFORM_ATA` (its USDC
  token account). Optional: `SOLANA_COMMITMENT` (default `finalized`).
- **Solana withdrawals (needs deposits on + a funded hot wallet):**
  `SOLANA_HOT_WALLET_SECRET_ENC` + `SOLANA_HOT_WALLET_ENC_KEY` — produce the
  encrypted form with `cd backend && go run ./cmd/wallet-secret-encrypt` (never
  store the raw base58 key). Fund the hot wallet with USDC (payouts) + SOL (fees).
- **Economy (optional):** `COIN_CENTS` (default 1), `WITHDRAW_SELL_FEE_PCT` (default 10 —
  the only fee on the coin round trip), `DEPOSIT_FEE_PCT` (default 0 — deposits are
  free), `WITHDRAW_MIN_COINS`. All are fallbacks: the Super Admin economy screen
  overrides them live over the config bus.

Operational prereqs (not just secrets): a reachable Solana RPC, a platform wallet +
its USDC ATA created on-chain, and a funded hot wallet. Until all deposit vars are
set, `/v1/deposits` returns 503 and the deposit listener stays off; until the hot
wallet is set, withdrawals stay off — the app boots fine either way.

### `Super_Admin` (`deploy.yml`)
`ADMIN_POSTGRES_PASSWORD`, `JWT_SECRET` (≥32), `PLATFORM_ADMIN_PRIVATE_KEY`,
`PLATFORM_ENGINE_PUBLIC_KEY`, `LENS_API_KEY`, `LENS_ORG_ID`, `ADMIN_CORS_ORIGIN`,
`ADMIN_WEB_PORT` (opt).

### `Pyyol_client` (`deploy-landing.yml`)
`NEXT_PUBLIC_API_BASE` (the public arena API origin), `NEXT_PUBLIC_SITE_URL`,
`NEXT_PUBLIC_DOCS_API` (admin public docs API, e.g. `https://admin.pyyol.com/api/public` — powers the /docs page; unset ⇒ static-only docs),
`NEXT_PUBLIC_PRIVY_APP_ID` (opt), `LANDING_PORT` (opt).

## Keys that MUST MATCH across repos (or the bus/telemetry silently breaks)
| Value | Arena | Tracing | Admin |
|---|---|---|---|
| Platform admin key | `PLATFORM_ADMIN_PUBLIC_KEY` | — | `PLATFORM_ADMIN_PRIVATE_KEY` (pair) |
| Platform engine key | `PLATFORM_ENGINE_PRIVATE_KEY` | — | `PLATFORM_ENGINE_PUBLIC_KEY` (pair) |
| Lens query secret | `PYYOL_LENS_API_KEY` used for reads | `QUERY_API_KEY` | `LENS_API_KEY` |
| Lens ingest secret | `PYYOL_LENS_API_KEY` | `INGEST_API_KEY` | — |
| Org id | `PYYOL_LENS_ORG` | `PYYOL_LENS_DEFAULT_ORG_ID` | `LENS_ORG_ID` |

Generate the platform-bus pair once: in `backend/`, `go run ./cmd/platform-bus-keygen`.
(See `DEPLOYMENT_PREREQS.md` for the full fail-closed contract.)

## Migrations to expect on this deploy
Postgres migrations apply automatically on arena boot (`AUTO_MIGRATE`, advisory-locked
so multi-instance is safe); `/readyz` only passes once they have. Two are new and
worth knowing about before you look at the data:
- **0064** — platform liveness heartbeats (outage grace for staked matches).
- **0065** — lifts seeded stake tiers to the $5 floor. Mafia moves $1/$5/$20 →
  $5/$20/$50. It is guarded all-or-nothing on the originally seeded values, so if you
  have already priced your own tiers nothing is touched. A ladder you partly re-priced
  that still holds a sub-$5 band keeps it, and the stakes editor will refuse to save
  until you raise it — that is intended, since a migration silently re-pricing a
  deliberate configuration is worse than an explicit error.

ClickHouse migrations apply on processor boot. **005** adds tiered retention (raw
events 30d, per-match structure 180d, token usage 365d, rollups 90d/730d) and enables
the skip indexes that were left commented out — expect the first merge pass after
deploy to do real work on a large existing dataset.

## Verification baked into each pipeline
- **Arena**: polls `/readyz` (DB reachable + migrations applied).
- **Tracing**: waits for ClickHouse healthy → `schema_migrations` populated (processor
  applied them) → ingest(8081)+query(8082) `/health`.
- **Admin**: polls `/healthz` through the SPA nginx → admin-api; then greps admin-api
  logs for `mirror consuming platform events` (admin ⇄ arena bus connected).
- **Client**: polls `/healthz`.

## One-time manual step (admin only)
Seed the first admin (DESTRUCTIVE — truncates; run ONCE, never on redeploy):
`docker compose -f deploy/docker-compose.prod.yml run --rm admin-api /app/seed`
