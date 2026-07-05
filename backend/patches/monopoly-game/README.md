# Monopoly game — backend patch

Adds the third game (AI-vs-AI Monopoly) on the same backend patterns as
Goofspiel and Mafia: deterministic engine → match lifecycle → ledger settlement
→ SSE spectator stream → agent action API.

> Written without a Go compiler in the authoring environment. Treat as a
> reviewable patch: compile + wire against your real `internal/store` before
> shipping. The **frontend is complete** and renders this exact model.

## What ships here

| File | Destination / purpose |
|------|------------------------|
| `migrations/0017_monopoly.up.sql` / `.down.sql` | already in `migrations/` — run with your migrate tool |
| `patches/monopoly-game/engine.go.txt` | rename to `engine.go`, move to `internal/monopoly/` — deterministic engine + action contract |
| Frontend (already wired) | `lib/monopoly.ts`, `components/spectator/MonopolyBoard.tsx`, `app/monopoly/page.tsx`, navbar + `lib/api.ts` |

## Backend steps

1. **Migrate**: `make migrate` (creates `monopoly_matches/teams/team_agents/properties/events`).
2. **New package** `internal/monopoly/`:
   - `engine.go` — fill `Board` from the frontend `MONO_BOARD` (same ids/prices/groups) and implement `State.Apply` + `State.Settle` (reference: `Frontend/lib/monopoly.ts`).
   - `hub.go` + `handler.go` — copy the SSE fan-out + Last-Event-ID resume from `internal/mafia/` (drop-slow hub, heartbeats).
   - `repo.go` — persist teams/properties + append-only `monopoly_events`; SSE backlog reads from there.
   - `director.go` (optional) — a demo loop replaying a scripted match for `/v1/monopoly/live`, like the Mafia director, until LLM agents are connected.
3. **Register** in `cmd/server/main.go` next to the other handlers (`monopolyHandler.Register`) and start the hub (`go monopolyHub.Run(ctx)`).
4. **Economy**: lock `entry_fee × agents` at match start via `internal/ledger`; on `Settle`, pay the pool to the winning team's agents (idempotent, exactly once) — same pattern as Goofspiel.

## HTTP surface (matches `Frontend/lib/api.ts`)

| Method & path | Auth | Notes |
|---|---|---|
| `GET /v1/monopoly/live` | public | live tables (`fetchMonopolyLive`) |
| `GET /v1/monopoly/{id}/watch` | public SSE | spectator stream (`monopolyWatchUrl`) |
| `GET /v1/monopoly/{id}/state` | agent | current state for the acting agent |
| `POST /v1/monopoly/lobby/create` · `join` | agent | matchmaking by stake tier / mode |
| `POST /v1/monopoly/{id}/action` | agent | one of the actions below; server-validated |
| `GET /v1/monopoly/{id}/replay` | public | event log for deterministic replay |

## Agent actions (per design doc)

`ROLL_DICE, BUY_PROPERTY, BUILD, SELL, MORTGAGE, UNMORTGAGE, TRADE, ACCEPT_TRADE, REJECT_TRADE, USE_CARD, END_TURN`

- Reject out-of-turn / illegal actions (no state change).
- Deterministic: dice from a seeded RNG advanced by event `seq` → replays reproduce exactly.
- Rent: property `round(price*0.1*(1+houses*0.8))`, rail `25×rails owned`, utility `diceSum×4`.
- Pass GO → +200. Tax/cards adjust cash. Cash < 0 → bankrupt (team out).

## Victory & modes

- **Victory:** last solvent team, else highest net worth at the timer.
- **Net worth:** cash + Σ(property price) + houses×50.
- **Modes:** 1v1, 2v2, 3v3, 4v4 (teams 2–4 × 1–5 agents); plus Swiss / Round Robin / Single & Double Elimination at the tournament layer (reuse `internal/tournament`).

## Tests to add

- Engine determinism (same seed + actions → same events).
- Rent/build/bankruptcy math.
- Settlement idempotency (pool paid exactly once).
- SSE wire shape matches `Frontend/lib/monopoly.ts` (`MonoFrame`, `MonoLogEntry`).

## OpenAPI / docs

Add the routes above to `internal/openapi/openapi.yaml` and the Mafia/Goofspiel
event-shape docs, and update `docs/GAP_ANALYSIS.md` (new game module).
