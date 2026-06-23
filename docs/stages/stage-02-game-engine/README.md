# Stage 2 — Goofspiel Game Engine & Replay Log

> **Goal:** a pure, deterministic Goofspiel engine plus the append-only event log
> that *is* the replay. No networking — just provably-correct rules, exhaustively
> tested. Everything downstream (matches, fairness, spectator, clips) builds on this.

**Maps to:** Plan Phase 0, Week 2; Trust/Fairness §12; Game §3.
**Depends on:** Stage 0 (DB for the event log).
**Unblocks:** Stage 3 (match worker), Stage 6 (spectator), Stage 8 (clips).

## Scope
**In:** pure `engine/goofspiel` (rules, commit-reveal seed → prize order,
seal/resolve/timeout, legal actions), `match_events` persistence + replay
reconstruction + verification, golden/property test suite.
**Out:** matchmaking, the live worker loop and timers (Stage 3), broadcasting.

## Design references
- [game-engine.md](../../architecture/game-engine.md) (the full spec — this stage implements it)
- [data-model.md](../../architecture/data-model.md) (`match_events`, `matches.engine_version/prize_seed_commit/prize_seed/replay_hash`)

## Tasks
- [ ] `internal/engine/goofspiel`: `Config`, `State`, `Event`, `Version` constant; **stdlib-only**.
- [ ] `Init(seed)`: derive prize order via `HMAC-SHA256(seed, "prize-order")` → Fisher–Yates; deal identical hands; emit `match_created` + first `prize_revealed`.
- [ ] `LegalActions`, `Seal` (validate membership, store sealed, emit `card_sealed` w/o value), `Resolve` (reveal both, score, **tie carry+stack**, discard, advance, emit `round_revealed`), `ForceTimeout` (seed-derived RNG → random legal card).
- [ ] Finish detection + `Winner` (−1 tie / 0 / 1) + `match_finished` event.
- [ ] Commit–reveal helpers: `commit = sha256(seed)`; verify function.
- [ ] `internal/replay`: append events (gap-free `seq`, unique `(match_id,seq)`); `Reconstruct(matchID) → State` by replaying; `Verify(matchID)` recomputes result + `replay_hash` and checks `sha256(seed)==commit`.
- [ ] `replay_hash` = stable hash over canonical-encoded event log.
- [ ] Golden test vectors (fixed seed + scripted plays → exact expected scores/winner).

## Data model delta
`match_events` table; `matches` columns `engine_version`, `prize_seed_commit`,
`prize_seed`, `replay_hash` populated by Stage 3 but defined/used here for replay.

## API delta
None agent-facing yet. (Replay endpoint is wired in Stage 3 once matches exist.)

## Acceptance criteria
- **Determinism:** same `seed` ⇒ identical prize order and, given identical card
  choices, identical final state — across 10k random trials.
- **Replay fidelity:** for any finished match, `Reconstruct(events)` equals the
  live final state; `Verify` confirms `sha256(seed)==commit` and recomputed result.
- **Rules correctness:** ties carry & stack; a tie on a 10-pt prize makes the next
  pool 10+next; total points awarded across a match == sum of the prize deck minus
  any still-carried pool; no card is ever played twice; a hand shrinks by exactly
  one per round.
- **Timeouts reproducible:** forced-timeout cards are identical on replay from seed.
- Engine package imports only stdlib (enforced by lint/architecture test).

## Test plan
- Table-driven golden replays (hand-computed scenarios incl. multi-round carries).
- Property tests (random legal play): all invariants above hold.
- Determinism: `replay(events) == state` for thousands of random matches.
- Fairness: tamper a card in the log → `Verify` fails (negative test).
- Fuzz `Seal` with illegal/duplicate cards → correct rejection, no panic.

## Observability
- Counters: `engine_matches_resolved_total`, `engine_ties_total`,
  `replay_verify_failures_total` (**must stay 0** in prod).

## Security
- Seed from CSPRNG (`crypto/rand`); commit published before play; reveal after;
  no clock/global state in the engine (reproducibility = anti-rigging control).

## Risks
- Subtle rule bug poisons every match → mitigated by exhaustive golden+property
  tests and the public-replay verification path (caught externally too).

## Definition of Done
Engine + replay fully tested (golden + property + determinism + tamper), lint
clean (stdlib-only proven), acceptance criteria pass.
