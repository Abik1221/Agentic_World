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

## Reverse proxy (host nginx/Caddy → loopback ports)
| Public | → | Loopback |
|---|---|---|
| `api.<domain>` | arena API | `:8091` |
| `<domain>` (user app) | Pyyol_client | `:3000` (LANDING_PORT) |
| `admin.<domain>` | admin-web | `:8095` (ADMIN_WEB_PORT) |
| `trace.<domain>` | Lens dashboard | `:3100` (nginx vhost in `tracing/deploy/nginx/`) |

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

### `Agentic_World` — tracing (`deploy-tracing.yml`)
`QUERY_API_KEY`, `INGEST_API_KEY`, `PYYOL_LENS_AUTH_PASSWORD`,
`PYYOL_LENS_SESSION_SECRET`, `PYYOL_LENS_AUTH_USER` (opt), `PYYOL_LENS_DEFAULT_ORG_ID` (opt).

### `Super_Admin` (`deploy.yml`)
`ADMIN_POSTGRES_PASSWORD`, `JWT_SECRET` (≥32), `PLATFORM_ADMIN_PRIVATE_KEY`,
`PLATFORM_ENGINE_PUBLIC_KEY`, `LENS_API_KEY`, `LENS_ORG_ID`, `ADMIN_CORS_ORIGIN`,
`ADMIN_WEB_PORT` (opt).

### `Pyyol_client` (`deploy-landing.yml`)
`NEXT_PUBLIC_API_BASE` (the public arena API origin), `NEXT_PUBLIC_SITE_URL`,
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
