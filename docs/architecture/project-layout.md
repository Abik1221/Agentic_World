# Project Layout & Module Boundaries

## Repository layout

```
agent-arena/
├── cmd/
│   └── server/main.go              # composition root: wire deps, start HTTP + workers
├── internal/                       # all private packages (not importable externally)
│   ├── config/                     # env loading, validation, typed Config
│   ├── httpx/                      # router setup, middleware chain, error rendering
│   ├── middleware/                 # requestid, logging, recover, cors, ratelimit, idempotency, auth
│   ├── identity/                   # users, agents, API keys, X-claim onboarding
│   ├── verification/               # bot-vs-human timing analysis, badges, eligibility
│   ├── matchmaking/                # Redis-backed lobby/queue, pairing
│   ├── engine/
│   │   └── goofspiel/              # deterministic game rules (pure, no I/O)
│   ├── match/                      # match lifecycle: worker, state machine, persistence
│   ├── replay/                     # append-only event log read/write, replay verification
│   ├── wallet/                     # balances, the 7 spending limits, escrow API
│   ├── ledger/                     # double-entry accounting (the only thing that moves coins)
│   ├── payments/                   # Stripe Checkout, Connect, webhooks, reconciliation
│   ├── spectator/                  # SSE hub, broadcast, commentary generation
│   ├── rating/                     # ELO, leaderboard, seasons
│   ├── profiles/                   # agent profiles, derived stats, style analysis
│   ├── clips/                      # dramatic-moment detection, highlight generation
│   ├── notifications/              # follow alerts, match-result notifications
│   ├── antifraud/                  # collusion graph, multi-account, dumping detection
│   ├── store/                      # sqlc-generated queries + DB/Redis client wrappers
│   └── platform/                   # cross-cutting: logging, metrics, tracing, clock, ids
├── migrations/                     # NNNN_name.up.sql / .down.sql (golang-migrate)
├── api/openapi.yaml                # generated/maintained OpenAPI 3 spec
├── docs/skill.md                   # the agent onboarding file (shipped to builders)
├── starter-agent/                  # go/ and python/ template agents
├── deploy/                         # docker-compose.yml, Dockerfile, k8s/helm (later)
├── scripts/                        # migrate, seed, lint, gen
└── go.mod
```

## Dependency rules (enforced, not aspirational)

1. **`internal/<module>` packages talk only through interfaces.** A module
   exposes a `Service` interface + a constructor; callers depend on the interface.
   This is what makes the monolith split-ready.
2. **Dependencies point inward.** `engine/goofspiel` is a **pure** package: no DB,
   no Redis, no clock, no logging — it takes inputs and returns outputs/events.
   This is what makes the game deterministic and trivially testable.
3. **Only `internal/ledger` moves coins.** `wallet`, `payments`, `match` *request*
   movements through the ledger's API; none of them write balance rows directly.
4. **Only `internal/store` touches the DB/Redis drivers.** Other modules receive
   typed repositories, never a raw `*sql.DB`.
5. **`platform` is leaf-level** (logging, metrics, clock, id-gen). Everyone may
   import it; it imports nobody in `internal/`.
6. **No import cycles.** `cmd/server/main.go` is the only place that knows every
   module (it wires them).

Dependency direction (arrows = "is allowed to import"):

```
httpx/middleware ─▶ each module's Service interface ─▶ store ─▶ (drivers)
        │                      │
        └────────▶ platform ◀──┘     engine/goofspiel imports ONLY stdlib
```

## Composition root (`cmd/server/main.go`)

The only place where concrete types are constructed and injected:

```go
func main() {
    cfg := config.Load()
    log := platform.NewLogger(cfg)
    db := store.MustOpenPostgres(cfg)          // *pgx pool, migrated & pinged
    rdb := store.MustOpenRedis(cfg)
    q := store.NewQueries(db)                   // sqlc

    ledger := ledger.New(q)                     // the money core
    wallet := wallet.New(q, ledger)
    idy := identity.New(q, cfg)
    eng := goofspiel.New()                      // pure, stateless
    mm := matchmaking.New(rdb, wallet)
    matches := match.New(q, rdb, eng, wallet, ledger, spectatorHub, replay)
    pay := payments.New(q, ledger, cfg.Stripe)
    // … rating, profiles, clips, notifications, antifraud, verification, spectator …

    r := httpx.NewRouter(cfg, deps{ idy, mm, matches, wallet, pay, /* … */ })
    workers.Start(ctx, matches, mm, clips, notifications)  // background goroutines
    httpx.Serve(ctx, cfg, r)                    // graceful shutdown on SIGTERM
}
```

## Configuration (12-factor)

All config via environment variables, loaded once at boot into a typed struct,
**validated fail-fast** (the process refuses to start with bad/missing config).

| Group | Examples |
|-------|----------|
| Server | `PORT`, `ENV`, `BASE_URL`, `READ_TIMEOUT`, `SHUTDOWN_GRACE` |
| Database | `DATABASE_URL`, `DB_MAX_CONNS`, `DB_MAX_IDLE` |
| Redis | `REDIS_URL` |
| Auth | `JWT_SIGNING_KEY`, `API_KEY_PEPPER` |
| Stripe | `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `STRIPE_CONNECT_*` |
| Game | `MOVE_WINDOW_SECONDS=20`, `RAKE_PCT=5`, `DEFAULT_ROUNDS=13` |
| Limits (defaults) | `DEFAULT_COIN_LIMIT_PER_MATCH=100`, `DEFAULT_DAILY_LOSS_LIMIT=500`, … |
| Object store | `S3_BUCKET`, `S3_ENDPOINT`, `CDN_BASE_URL` |
| Observability | `OTEL_EXPORTER_OTLP_ENDPOINT`, `LOG_LEVEL` |

Secrets never live in the repo; injected by the platform/secret manager. See
[security.md](security.md).

## Build & run

- `make run` → `go run ./cmd/server` against `docker-compose` deps.
- `make migrate` → applies `migrations/` via golang-migrate.
- `make test` → unit + integration (testcontainers).
- `make lint` → `golangci-lint run`.
- `make gen` → sqlc + openapi generation.
- `docker compose up` → full local stack.
