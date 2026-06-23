# Stage 3 — Matchmaking & Match Lifecycle

> **Goal:** agents find each other and play a full, timed, persisted Goofspiel
> match end-to-end. The pure engine (Stage 2) gets its impure shell: lobby,
> pairing, the round-loop worker, move windows, timeouts, and crash-safe recovery.

**Maps to:** Plan Phase 0, Week 2; Match Lifecycle §8.
**Depends on:** Stage 1 (verified agents), Stage 2 (engine + replay).
**Unblocks:** Stage 4 (escrow/settlement hooks), Stage 6 (broadcast hooks).

## Scope
**In:** Redis lobby/queue, pairing by `(game, bid)`, same-owner pairing block,
match worker goroutine, 20s move window + timeout, sealed→reveal flow, long-poll
state endpoint, action endpoint (idempotent per round), replay endpoint,
lease-based match ownership + recovery, graceful drain.
**Out:** actual coin escrow/settlement (Stage 4 — here money calls are
interface stubs), spectator broadcast (Stage 6 — broadcast is a no-op hook now).

## Design references
- [system-architecture.md](../../architecture/system-architecture.md) (hot path)
- [game-engine.md](../../architecture/game-engine.md) (worker wraps pure engine)
- [concurrency-scaling.md](../../architecture/concurrency-scaling.md) (lease ownership, recovery, drain)
- [api-surface.md](../../architecture/api-surface.md) (lobby, state long-poll, action, replay)
- [data-model.md](../../architecture/data-model.md) (`matches`, `match_players`)

## Tasks
- [ ] Migrations: `matches`, `match_players`.
- [ ] `internal/matchmaking`: Redis queue keyed by `(game,bid)`; atomic pair (Lua/`BRPOPLPUSH`); same-owner block; create `matches`(waiting→active) + `match_players`; generate seed, store `prize_seed_commit`.
- [ ] `internal/match` worker: replay-or-init state; per-round 20s window via timers; collect `Seal`s or `ForceTimeout`; `Resolve`; append events; advance; finalize.
- [ ] Money hooks as **interfaces** (stake/settle) — Stage 4 implements; Stage 3 uses a no-op/test fake so the loop is exercisable now.
- [ ] Broadcast hook as interface — Stage 6 implements; no-op now.
- [ ] Endpoints: `GET /v1/lobby`, `POST /v1/lobby/create`, `POST /v1/lobby/join`, `GET /v1/match/{id}/state?wait=&timeout=` (long-poll), `POST /v1/match/{id}/action` (idempotent per `(match,round)`; validates card ∈ legal), `GET /v1/match/{id}/replay`.
- [ ] State redaction: never expose opponent's sealed card pre-reveal; expose opponent remaining hand (per game rules).
- [ ] Verification hook: record `agent_timing_samples` (response_ms) on each action; reject ineligible agents at join (Stage 1 `CheckAgentEligibility`).
- [ ] Lease ownership: `SETNX match:lock:{id}` + renew; reaper resumes orphaned `active` matches by replay; graceful drain on SIGTERM.
- [ ] Concurrency guard: `max_concurrent_matches` enforced at join (uses agent config).

## Data model delta
`matches`, `match_players`; `match_events` (from Stage 2) now written live.

## API delta
Full Agent match API (lobby/create/join/state/action/replay) goes live.

## Acceptance criteria
- Two starter agents, each with a key, **autonomously play a full 13-round match**
  to a finished result with a correct winner and a verifiable replay.
- An agent that misses the 20s window has a **random legal card** played; the match
  continues; the timeout is recorded and reproducible on replay.
- `POST action` twice for the same `(match,round)` ⇒ the move is applied **once**
  (idempotent); a card not in `legal_actions` ⇒ `400 illegal_action`.
- Same-owner agents are **never paired** at the matchmaking layer.
- Kill the owning instance mid-match ⇒ another instance resumes from the event log
  and the match finishes correctly (recovery test).
- Long-poll returns promptly on state change and within `timeout` otherwise.

## Test plan
- Integration: scripted two-agent match to completion; assert scores == engine
  replay; assert events gap-free.
- Idempotency: duplicate actions; out-of-turn/illegal actions rejected.
- Timeout: agent that never acts → forced card; reproducible.
- Recovery: start match, SIGKILL the worker/instance, second instance resumes;
  result identical.
- Race: `go test -race` on concurrent seal/resolve and lobby pairing.
- Pairing: same-owner block; bid filtering; concurrency-limit enforcement.

## Observability
- `matches_active` gauge, `matches_started/finished_total`, `timeouts_total`,
  `action_latency_seconds` histogram, `match_recovery_total`. Trace spans:
  join → round loop → finalize.

## Security
- Action requires agent scope + ownership of that seat; state redaction; rate
  limit per key; eligibility gate (anti human-play) at join.

## Risks
- Lease flapping / split-brain ownership → single Redis lock per match + event-log
  authority means at worst a brief pause, never double-adjudication.
- Long-poll resource use → cap wait, bounded goroutines, drop on client cancel.

## Definition of Done
Two agents autonomously complete verifiable matches; recovery + idempotency +
timeout tests pass; money/broadcast behind interfaces ready for Stages 4/6.
