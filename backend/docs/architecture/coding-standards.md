# Coding Standards & Definition of Done

Conventions that keep a fast-moving Go monolith maintainable and split-ready.

## 1. Go conventions

- Target **Go 1.22+**; `gofmt`/`goimports` enforced; `golangci-lint` is the gate
  (`govet`, `staticcheck`, `errcheck`, `gosec`, `sqlclosecheck`, `revive`).
- **Accept interfaces, return structs.** Each module exposes a `Service` interface
  + a constructor returning the concrete type.
- **Errors:** wrap with `%w` and context (`fmt.Errorf("settle match %s: %w", id, err)`);
  sentinel errors (`var ErrInsufficientBalance = errors.New(...)`) for control
  flow; map domain errors → HTTP codes in one place (`httpx`). Never `panic` in
  request paths (the recover middleware is a backstop, not a strategy).
- **Context everywhere:** every I/O call takes `ctx`; respect cancellation and
  deadlines; no `context.Background()` below `main`/workers.
- **No globals** except truly constant config; inject dependencies via the
  composition root.
- **Money is `int64` coins.** Never float. Never a decimal library. A lint rule
  forbids float types in `ledger`/`wallet`/`payments`.
- **Time is injected** via a `Clock` interface (`platform.Clock`) so tests are
  deterministic; the engine takes no clock at all.

## 2. Package discipline

- `internal/engine/goofspiel` imports **only stdlib** (pure).
- `internal/ledger` is the **only** writer of balances/entries.
- `internal/store` is the **only** importer of DB/Redis drivers; others get typed
  repos/interfaces.
- No import cycles; `cmd/server` is the only omniscient package.
- Public API DTOs live in a versioned `apitypes` package, decoupled from DB models
  (never serialize a DB struct directly).

## 3. Database & migrations

- All SQL through **sqlc** (parameterized, compile-checked). No string-built SQL.
- Migrations are **forward-only in spirit**: every `*.up.sql` has a `*.down.sql`,
  but production rolls forward; destructive downs are gated and reviewed.
- Money mutations: single DB transaction, `SELECT … FOR UPDATE` on affected
  wallets, idempotency key enforced by unique constraint.
- Every new table gets the right indexes in the same migration (see access
  patterns in [data-model.md](data-model.md)).

## 4. Testing strategy

| Layer | Approach | Bar |
|-------|----------|-----|
| Pure engine | table-driven + property + golden replays | exhaustive; deterministic |
| Domain services | unit tests with fakes for store | branch coverage on money/limit logic |
| Integration | **testcontainers** real Postgres + Redis | every money path + each endpoint happy/sad |
| Idempotency | replay every money POST twice → identical result, single effect | mandatory |
| Concurrency | race detector (`go test -race`) on match worker + ledger | no data races |
| Contract | requests validated against `api/openapi.yaml` | drift fails CI |
| Load (Stage 10) | k6/vegeta vs staging | meet capacity targets |

CI must run: `lint → unit → integration(testcontainers) → openapi-contract →
govulncheck`. Red CI blocks merge.

## 5. API design rules

- Versioned (`/v1`); additive changes only within a version.
- Uniform error envelope (see [api-surface.md](api-surface.md)).
- Idempotency on all money + action POSTs.
- Cursor pagination; `limit` capped server-side.
- Never leak internal IDs or stack traces in prod responses.

## 6. Definition of Done (every task/stage)

A change is done only when **all** are true:
- [ ] Code compiles; `make lint` and `make test` green (incl. `-race`).
- [ ] New behavior has unit + integration tests; money paths have idempotency tests.
- [ ] Endpoints declare auth scope; inputs validated; SQL parameterized.
- [ ] Logs/metrics/traces added for the new behavior (per [observability-sre.md](observability-sre.md)).
- [ ] Migrations have up+down and required indexes.
- [ ] `openapi.yaml` updated; docs in this `docs/` tree updated if contracts changed.
- [ ] Security checklist (see [security.md](security.md) §6) satisfied.
- [ ] Stage Acceptance Criteria demonstrably pass.

## 7. Commits, branches, reviews

- Trunk-based with short-lived branches; small PRs.
- Conventional-commit style messages; PR description links the stage + task.
- At least one review; money/auth/engine changes require a second reviewer.
- No direct pushes to `main`; CI required green.
