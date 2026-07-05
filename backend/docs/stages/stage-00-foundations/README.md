# Stage 0 — Foundations

> **Goal:** an empty repo becomes a deployable, observable, tested Go service with
> a database, migrations, config, health checks, and CI — the skeleton every later
> stage hangs on. No game logic yet; just a rock-solid base.

**Maps to:** Plan Phase 0, Week 1.
**Depends on:** nothing.
**Unblocks:** every other stage.

## Scope

**In:** project scaffold, config loader, DB+Redis wiring, migration tooling,
middleware chain, health/readiness, structured logging+metrics+tracing skeleton,
`docker-compose`, CI pipeline, Makefile.
**Out:** any domain logic (no agents, matches, money yet).

## Design references
- [project-layout.md](../../architecture/project-layout.md) (layout, composition root, config)
- [tech-stack.md](../../architecture/tech-stack.md) (pinned deps)
- [observability-sre.md](../../architecture/observability-sre.md) (signals, health)
- [coding-standards.md](../../architecture/coding-standards.md) (CI gates, DoD)

## Tasks
- [ ] Initialize Go module, repo layout per project-layout.md, `cmd/server/main.go` boots and serves.
- [ ] `internal/config`: env loader → typed `Config`; **fail-fast validation**; `.env.example` documented.
- [ ] `internal/platform`: `slog` JSON logger, Prometheus registry, OTel tracer, `Clock` interface, ID generator (prefixed public IDs).
- [ ] `internal/store`: Postgres pool (pgx) + Redis (go-redis) with pings; sqlc configured (`sqlc.yaml`).
- [ ] `migrations/` + golang-migrate wired; `make migrate`; seed **system wallets** migration stub (kinds: `stripe_clearing`, `platform_revenue`, `escrow`) ready for Stage 4.
- [ ] `internal/httpx` + `internal/middleware`: chi router with chain — request-id → slog logging → recover → CORS → (rate-limit + auth placeholders) → handler; uniform error envelope renderer.
- [ ] Endpoints: `GET /healthz` (liveness), `GET /readyz` (DB+Redis+migrations check), `GET /metrics` (internal), `GET /v1/ping`.
- [ ] Graceful shutdown on SIGTERM (drain + close pools).
- [ ] `deploy/docker-compose.yml` (Postgres+Redis+service) and `Dockerfile` (multi-stage, distroless/static).
- [ ] `Makefile`: `run, migrate, test, lint, gen, compose`.
- [ ] CI pipeline: `lint → unit → integration(testcontainers) → govulncheck`; red blocks merge.
- [ ] Baseline dashboards/alerts wired (RED metrics, readiness alert).

## Data model delta
- System wallets seed (kinds above) — table from [data-model.md](../../architecture/data-model.md) lands here as the first migration so money stages plug in cleanly.

## API delta
- `GET /healthz`, `GET /readyz`, `GET /metrics`, `GET /v1/ping`.

## Acceptance criteria
- **Given** a clean checkout, **when** I run `docker compose up`, **then** the
  service is healthy: `/healthz` 200, `/readyz` 200 only after DB+Redis up and
  migrations applied.
- **Given** missing/invalid required config, **when** the process starts, **then**
  it exits non-zero with a clear message (no half-booted server).
- `make lint` and `make test` are green in CI on a fresh clone.
- A forced `SIGTERM` drains in-flight requests and exits 0 within the grace window.
- `/metrics` exposes RED metrics for `/v1/ping`.

## Test plan
- Integration (testcontainers): boot real PG+Redis, assert `/readyz` flips
  false→true; assert migrations applied.
- Config: table tests for valid/invalid env → load result.
- Shutdown: start, fire SIGTERM mid-request, assert in-flight completes and exit 0.

## Observability
- slog JSON with `request_id`; RED metrics middleware; OTel span per request;
  readiness alert if `/readyz` failing across instances.

## Security
- TLS terminated at LB (doc + staging); secrets only via env; `gosec` in CI;
  CORS allowlist scaffold; no secrets in image (verified in CI).

## Risks
- Config sprawl → mitigate with one typed struct + fail-fast validation.
- Migration drift → CI runs migrations on ephemeral DB every build.

## Definition of Done
All tasks checked, acceptance criteria demonstrably pass in CI + a `docker compose
up` smoke, DoD checklist in [coding-standards.md](../../architecture/coding-standards.md) §6 satisfied.
