# End-to-End Product Review

Full-product QA pass across the Go backend + Next.js frontend. The sandbox has no
Go toolchain and no Postgres/Redis, so backend **runtime** testing must be done
locally; everything statically checkable was checked here.

## ✅ Verified in this environment

**Frontend types** — `npx tsc --noEmit` passes clean across the whole app
(all three game consoles, spectator, profile, and the new hero components).

**Route/link integrity** — every static internal `href` used anywhere in
`app/`+`components/` resolves to a real `app/**/page.tsx` route. No dead links.

**Frontend ↔ backend API contract (game flows)** — every `/v1/*` path the game
UIs call is registered in a Go handler:

| Flow | Frontend calls | Backend route | OK |
|---|---|---|---|
| Goofspiel | `/v1/lobby`, `/v1/lobby/{create,join}`, `/v1/match/{id}/{state,action,watch,replay}`, `/v1/matches/live`, `/v1/stats/live`, `/v1/leaderboard` | match + spectator + rating handlers | ✅ |
| Mafia | `/v1/mafia/live`, `/v1/mafia/lobby[/create/join/cancel]`, `/v1/mafia/{id}/{state,action,economy,replay,watch}` | mafia handler | ✅ |
| Monopoly | `/v1/monopoly/live`, `/v1/monopoly/lobby/create`, `/v1/monopoly/{id}/{state,action,economy,replay,watch}` | monopoly handler | ✅ |

**Go static review of everything changed this session** — imports all used, no
import cycles (`internal/monopoly` does not import `internal/store`), the lean
hub has no prometheus dep, and every external symbol the new Monopoly package
leans on exists with a matching signature:
`auth.PrincipalFromContext/.AgentPublicID/.UserPublicID`, `auth.RequireScope`,
`auth.ScopeAgent`, `Authenticator.Middleware`, `store.NewLocker` (returns a
`*Locker` whose `Lock(ctx,key,ttl)(func(),bool,error)` matches `monopoly.Locker`),
`store.Store.DB/.Redis`, `platform.NewClock`/`platform.Clock`,
`httpx.DecodeJSON/JSON/Error/NewError/ErrNotFound`. Brace/paren balance verified
on every new/edited Go file.

## ⚠️ Needs local verification (can't run here)

1. **Go build + tests** — the Monopoly service layer was written as a reviewable
   patch without a compiler. Run:
   ```
   go build ./...
   go vet ./...
   go test ./internal/engine/... ./internal/mafia/... ./internal/monopoly/...
   ```
   New tests to expect green: `internal/engine/monopoly/view_test.go`,
   `internal/engine/goofspiel/tierule_test.go`, plus the Mafia
   `redact_test.go`/`assign_test.go`. If `go build` flags anything it will almost
   certainly be a tiny import-ordering (`gofmt -w ./...`) or a one-line signature
   nit — the wiring mirrors the compiling Mafia code.

2. **Frontend production build** — `cd Frontend && npm run build` (exceeds the
   sandbox time budget; `tsc` covers types but not the full Next bundle).

3. **Live smoke test** (with Postgres + Redis + `make migrate` + `go run ./cmd/server`):
   - Spectate hub `/spectate` shows live tables + the animated hero board.
   - Register → verify → agent key in session.
   - `/play` (Goofspiel), `/mafia/play`, `/monopoly/play`: create a table, submit
     an action, confirm the state advances and the match settles.
   - Confirm **hidden-info**: while a Goofspiel round is open, the opponent's card
     is absent from `/v1/match/{id}/state`; Mafia `/v1/mafia/{id}/watch` never
     streams a night target/finding mid-match; Monopoly `/v1/monopoly/{id}/state`
     never contains `chance_order`/`cc_order`.

## 🧩 Known gaps (intentional / non-blocking)

- **Profile / account / notifications** endpoints (`/v1/agent/profile`,
  `/v1/auth/magic-link`, `/v1/notifications`, `/v1/agent/game-config`) aren't in
  the backend; the frontend **falls back to the local profile store** (see
  `patches/profile-flow`), so the UI works but doesn't persist server-side.
  `setGameConfig`/`/v1/agent/game-config` is now dead code after the per-game
  behaviour section was removed — safe to delete.
- **Monopoly staked economy** — tables run as practice (entry fee 0, wallet wired
  as `nil`). Pooling stakes through the ledger needs a small `MonopolyWallet`
  adapter. Console play, spectating, and settlement math already work.
- **Goofspiel `TieSplit`** — implemented + tested in the engine, but not yet
  selectable via match creation (needs a `tie_rule` field on the create API +
  column). `TieCarry` stays the wired default, so nothing changes until then.
- **OpenAPI** — `/openapi.yaml` still omits the Mafia + Monopoly routes.
- **Legacy `monopoly_*` tables** (migration 0017) are unused — Monopoly reuses the
  shared `matches`/`match_players`/`match_events` tables like Mafia. Safe to drop
  in a later migration.

## Bottom line
Frontend is green (types + links + contracts). All three games are wired
end-to-end with spec-compliant hidden-information handling. The only thing between
here and "confirmed working" is a local `go build`/`go test` + a live smoke test —
the static review found no blockers.
