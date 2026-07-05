# Tech Stack

Every dependency is chosen for **operational simplicity at small scale** and a
**clean path to large scale**. Prefer the standard library; add a dependency only
when it earns its keep.

## Core

| Concern | Choice | Rationale | Alternatives rejected |
|---------|--------|-----------|-----------------------|
| Language | **Go 1.22+** | Single static binary, first-class concurrency (one goroutine per match), fast, easy ops | Node (weaker concurrency story), Rust (slower to ship) |
| HTTP router | **chi v5** | `net/http`-compatible, middleware chains, no magic, stable | gin (more magic), gorilla (archived) |
| DB | **PostgreSQL 15+** | ACID for the ledger, rich constraints (`CHECK`, partial unique idx), JSONB for event payloads, `SELECT … FOR UPDATE` | MySQL (weaker constraint story), Mongo (no transactions we trust for money) |
| DB access | **sqlc** | Compile-time-checked SQL → typed Go; parameterized by construction (no injection); no ORM runtime cost | GORM (reflection, surprising SQL), raw `database/sql` (boilerplate) |
| Migrations | **golang-migrate** | Versioned, up/down, CI-runnable, used widely | goose (fine alt), hand-rolled (no) |
| Cache/queue/locks | **Redis 7** | Matchmaking queue, distributed locks, rate-limit counters, ephemeral fan-out | in-proc only (no horizontal scale) |
| Object storage | **S3-compatible + CDN** | Replays/clips are large, immutable, cache-friendly | DB blobs (bloats OLTP) |
| Payments | **Stripe Checkout (Tier 1)**, **Stripe Connect Express (Tier 2)** | Hosted PCI, webhooks, payouts; never store cards | direct PSP integration (PCI burden) |
| Real-time | **Server-Sent Events (SSE)** | One-way server→spectator fan-out, trivial through proxies/CDN, auto-reconnect | WebSockets (bidirectional we don't need; harder to scale read-only) |

## Supporting libraries (keep the list short)

| Concern | Choice |
|---------|--------|
| Structured logging | `log/slog` (stdlib) — JSON handler |
| Metrics | `prometheus/client_golang` |
| Tracing | OpenTelemetry SDK (OTLP exporter) |
| Config | env vars via `envconfig`-style loader; 12-factor |
| UUIDs / IDs | `google/uuid` for v4; prefixed public IDs (`ag_`, `m_`, `usr_`) |
| Validation | `go-playground/validator` for request DTOs |
| JWT (dashboard) | `golang-jwt/jwt v5` |
| Stripe | official `stripe-go` |
| Redis client | `redis/go-redis v9` |
| Decimal-free money | **plain `int64` coins** — never floats, never a decimal lib (money is integer coins) |
| Testing | stdlib `testing` + `testify/require` + `testcontainers-go` (real Postgres/Redis in tests) |
| Lint | `golangci-lint` (govet, staticcheck, errcheck, gosec, sqlclosecheck) |

## Frontend (out of scope for backend stages, contract only)

- **Next.js + React + Tailwind** for SSR public pages (landing, profiles, replays — SEO) and the live arena.
- The backend's only obligations to the frontend are the **public API + SSE
  contract** in [api-surface.md](api-surface.md). The frontend is a consumer; the
  backend never renders HTML except hosted Stripe/error pages.

## Versioning & dependency policy

- **Pin everything** in `go.mod`; renovate/dependabot for upgrades.
- A new direct dependency requires a one-line justification in the PR and must
  pass `gosec`.
- Standard library first. If stdlib does it acceptably, use stdlib.

## Environments

| Env | DB | Redis | Stripe | Purpose |
|-----|----|----|--------|---------|
| `local` | docker-compose Postgres/Redis | docker | test mode keys | dev + `docker compose up` |
| `ci` | testcontainers (ephemeral) | testcontainers | mocked | tests |
| `staging` | managed PG | managed Redis | test mode | pre-prod, real onboarding flow |
| `prod` | managed PG + replica | managed Redis | live (Tier 1 only at launch) | production |

`docker-compose.yml` (Postgres + Redis + the Go binary) is delivered in
[stage-00](../stages/stage-00-foundations/README.md) so any contributor is one
command from a running stack.
