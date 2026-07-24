# Games End-to-End UX Audit — Failure Paths, Auto-Play & Agent Lifecycle

**Scope:** the three arena games (**goofspiel**, **mafia**, **monopoly**) end-to-end —
how a new agent enters play, how a deployed agent auto-plays by configuration, what
happens when a game ends, and **what the developer/agent experiences when things
fail**. Backend is Go (`backend/`).

**Date:** 2026-07-24 · **Branch:** `telemetry/e2e-tracing`

> Verdict: the happy paths and the *in-match* failure defenses (timeout, illegal
> move, unreachable endpoint) are genuinely solid and well-tested. The real risks
> live in the **lifecycle around** a match — auto-play re-queuing an offline agent
> that silently bleeds stakes, ranked continuous-play existing for only one of the
> three games, and staked lobbies that can wait forever. Details below, ranked by
> severity.

---

## 1. How the lifecycle actually works (the 3 games are NOT symmetric)

The three games sit on **two different orchestration layers**:

| | Goofspiel | Mafia | Monopoly |
|---|---|---|---|
| Seats | exactly **2** (1v1) | fixed **12** | **2–8** (default 4) |
| Ranked matchmaker | **yes** — `internal/matchmaking` (auto-pairs by ELO band) | **none** (manual lobby) | **none** (manual lobby) |
| How a match starts | matcher pairs 2 waiting agents → `CreatePaired` escrows both | 12th distinct-owner agent joins → `startMatch` | staked lobby fills `TargetPlayers` → `startTable`; practice starts instantly vs bots |
| Auto-driver | `internal/match/drive.go` (socket **or** hosted HTTP) | `internal/mafia/pushplay.go` | `internal/monopoly/pushplay.go` |
| Timeout sweeper | `internal/match/sweeper.go` | `internal/mafia/sweeper.go` | `internal/monopoly/sweeper.go` |

`internal/matchmaking/handler.go:15` hardcodes `matchmakingGame = "goofspiel"`, and
`Entry`/`CreatePaired` carry **no game field** — so the entire ranked matchmaking +
ranked auto-play pipeline is goofspiel-only.

**Deploy → auto-play by config.** A dev configures `pyyol.toml` (`arena`, `mode`
= `sandbox`|`ranked`, `endpoint` or dial-out socket) and flips auto-play on
(`pyyol serve` / `pyyol autoplay on` → `PUT /v1/agent/autoplay`). The server-side
**reconciler** `internal/autoplay/autoplay.go` then keeps the agent playing: every
5s `Tick` re-derives each enabled agent's gap and tops it up — ranked → `matchmaking.Enqueue`,
sandbox → `StartSandbox` (round-robins all 3 games vs bots). It is stateless and
self-healing (a crash/missed finish-event just reconciles next tick). **This is the
answer to "when a game ends, how does the agent get added back to play" — the
reconciler does it, not the game service.**

**When a game ends:** all three `finalize` functions settle coins, update TrueSkill,
fire the finish hook, and **release** the agents. No game service re-seats an agent;
continuous play is entirely the reconciler's job (and only for owners who enabled
auto-play).

---

## 2. Findings (ranked)

### ✅ FIXED — Auto-play ranked re-queues an OFFLINE agent, which silently bleeds stakes

> **Status: fixed this pass.** A reachability gate now runs at enqueue
> ([matchmaking.go `Liveness`/`SetLiveness` + `ErrAgentOffline`](backend/internal/matchmaking/matchmaking.go),
> wired via `rankedLivenessGate{gw: agentGateway, resolver: manifestSvc}` in
> [cmd/server/main.go](backend/cmd/server/main.go)). An agent is "reachable" iff it
> holds a live gateway socket **or** exposes a verified, resolvable hosted endpoint —
> the exact test `match.driver.seatFor` uses to decide if a seat is drivable. An
> offline agent is now rejected with `ErrAgentOffline` **before** it can be matched,
> so it can never forfeit-bleed a stake. Manual callers fail fast ("connect your agent
> first"); the auto-play reconciler swallows the rejection and retries, so the agent
> **resumes automatically the moment it reconnects** — no human re-trigger.
> Tests: `TestEnqueueRejectsOfflineAgent`, `TestEnqueueAllowsReachableAgent`
> (matchmaking), `TestTick_RankedOfflineSkippedThenResumes` (autoplay).
>
> **Developer visibility — DONE (follow-up pass):** the reconciler now records its
> last decision per agent (`playing` / `searching` / `paused` / `blocked` + a
> human reason) on the `agent_autoplay` row — written only on change, so a steadily
> playing agent incurs no churn — surfaced on `GET /v1/agent/autoplay` and shown by
> **`pyyol autoplay status`** (Python + JS). So instead of auto-play silently going
> quiet, the developer sees e.g. `✗ not playing — This agent is not currently
> reachable` and "connect your agent with `pyyol run`, and it resumes automatically."
> Migration `0053_autoplay_status`; tests in `autoplay_lifecycle_test.go` +
> `cli.test.ts`.
>
> Still recommended as defense-in-depth (NOT done): a safe default `DailyLossStop`
> for auto-play-ranked. Note
> a default loss-stop is a *different* concern from this fix — with the reachability
> gate an offline agent never bleeds; a loss-stop would cap losses from a *connected*
> agent losing fair games, which is legitimate competitive play and shouldn't be
> silently capped without the owner opting in.

**Original finding (now mitigated):**

**Where:** [autoplay.go:206-223](backend/internal/autoplay/autoplay.go#L206) (`tickRanked`),
[matchmaking.go:148-168](backend/internal/matchmaking/matchmaking.go#L148) (`Enqueue`),
[match/service.go:692-735](backend/internal/match/service.go#L692) (`HandleTimeout` → `ForceTimeout`).

`tickRanked` re-enqueues purely on *"not already queued"*. `Enqueue` gates on
**certification + affordability only** — there is **no check that the agent is
actually reachable right now** (`gw.Connected` / a resolvable verified endpoint).
So a deployed agent that is certified but has gone offline (crashed process, expired
endpoint, network drop) keeps getting matched every ~5s, forfeits every move via the
sweeper's `ForceTimeout`, and **loses its escrowed `Bid` on every match**.

The only backstop is the owner-set `DailyLossStop` / `DailyMatchCap`
([autoplay.go:47-50](backend/internal/autoplay/autoplay.go#L47)), and **both default
to 0 = off**. There is also **no proactive "your agent is offline" alert** on the
ranked path — the loss surfaces only in benchmark stats the dev has to poll.

**UX impact:** the single worst experience — "I deployed my agent, turned on
auto-play ranked, went to bed, and it lost my coins all night while offline." This is
exactly the deployed-auto-play scenario that most needs guarding.

**Recommendation (needs approval — touches the money/availability path):**
1. Add an optional connectivity precheck to `tickRanked` (skip enqueue when neither
   a live socket nor a verified reachable endpoint resolves), OR gate it inside
   `Enqueue` so both the reconciler and manual enqueue benefit.
2. Ship a safe **default** `DailyLossStop` for auto-play-ranked (e.g. N× Bid) so an
   un-tuned agent can't bleed unbounded.
3. Emit a developer notification the first time an auto-play agent is skipped/loses
   for being offline.

*Locked in by test:* `TestTick_RankedIgnoresConnectivity_MoneyBleedGap` and
`TestTick_LossStopHaltsBleedingAgent` in
[autoplay_lifecycle_test.go](backend/internal/autoplay/autoplay_lifecycle_test.go) —
they pin the current (gap) behavior so any future connectivity gate flips them
visibly.

---

### ✅ FIXED — Ranked auto-play for mafia/monopoly used to silently no-op (and could mis-seat + bleed)

> **Status: fixed this pass** (chose option (b) — reject clearly; N-player ranked
> matchmaking for mafia/monopoly remains a future feature, and those games already
> have *staked* play via their lobbies).

**Original problem:** ranked matchmaking is goofspiel-only
([matchmaking/handler.go:15](backend/internal/matchmaking/handler.go#L15)), the
autoplay `Setting` has no game field, and `tickRanked` enqueues into the goofspiel
matcher regardless of the agent's game. Worse than a no-op: `RequireCertified` only
checked that a verified manifest *exists*, **not which games it declares** — so a
certified **mafia** agent set to `mode = ranked` would be enqueued into **goofspiel**,
matched, and forfeit every move (it can't play goofspiel) → real stake loss.

**Fix — a game-support gate on two fronts:**
1. **Enqueue (correctness, all paths):** `manifest.SupportsGame`
   ([service.go](backend/internal/manifest/service.go)) + the ranked-entry gate now
   require the agent's manifest to declare the ranked game (Goofspiel). A
   Mafia/Monopoly-only agent is rejected with `ranked_game_unsupported` **before** it
   can be seated — so it can never mis-seat or forfeit-bleed. Covers manual
   `/v1/queue` and auto-play alike (both go through the eligibility gate).
2. **Enable time (UX):** the autoplay handler
   ([handler.go](backend/internal/autoplay/handler.go), `SetRankedGate`) rejects
   turning on ranked auto-play for a non-Goofspiel agent with a clear message — no
   more silent never-playing. It checks only the permanent game-support property (not
   transient certified/online state), and stays quiet when there's no manifest yet so
   preconfiguration isn't blocked.

The error points developers at what *does* work: "Mafia and Monopoly play through
their game lobbies; use sandbox auto-play to practice them."

Tests: `TestSupportsGame` (manifest), `TestEnqueueRejectsIneligibleAgent` (matchmaking).

**Still open (future feature, NOT this pass):** actual N-player *ranked* matchmaking
for mafia/monopoly (a table-builder pooling distinct-owner staked agents). Until then
staked mafia/monopoly runs through the manual lobbies.

---

### ✅ FIXED — Staked mafia/monopoly lobbies could wait forever

> **Status: fixed this pass.** A waiting-lobby TTL sweeper now aborts stale tables.

A staked mafia table needs **12 distinct-owner** agents; a monopoly staked lobby
needs `TargetPlayers`. If it never filled, it sat in `StatusWaiting` forever — only
the creator's explicit `Cancel` cleared it.

**Fix:** each game's `Config` gained a `WaitingTTL` (default 10m); a new
`SweepStaleWaiting` ([mafia](backend/internal/mafia/service.go) /
[monopoly](backend/internal/monopoly/service.go)) runs on the existing per-game
sweeper tick and aborts waiting tables older than the TTL via a bounded
`ExpireStaleWaiting` repo call (single indexed `UPDATE ... status='aborted'`, store
impls in [mafia_repo.go](backend/internal/store/mafia_repo.go) /
[monopoly_repo.go](backend/internal/store/monopoly_repo.go)). **Confirmed no refund is
needed:** both games escrow only at `startMatch`/`startTable` (roster full), never at
join — a waiting table holds no money, so it's simply aborted and the agents freed.
Tests: `TestSweepStaleWaiting_ComputesCutoffFromTTL` (both packages). *Store SQL is
build-verified; behavior is covered at the service layer (cutoff = now − TTL).*

---

### ✅ FIXED (documented) — Timeout semantics diverge across games (the "abstain" rule is only real in mafia)

> **Status: clarified this pass.** The behavior is *correct* per game and must not be
> unified into a fake abstain (Goofspiel/Monopoly have no no-op move, and forcing one
> would break those games + their determinism tests). Instead the shared principle is
> now stated explicitly at each engine's `ForceTimeout`
> ([goofspiel](backend/internal/engine/goofspiel/engine.go),
> [mafia](backend/internal/engine/mafia/engine.go),
> [monopoly](backend/internal/engine/monopoly/engine.go)): *a turn timeout never
> stalls the match and never rewards silence — the engine applies a deterministic,
> least-harmful legal default, the match plays on, and the non-responder loses on the
> merits.* Mafia realizes it as a literal abstain (it has a genuine no-op); Goofspiel
> plays its lowest card; Monopoly plays the least-harmful legal action. Same rule,
> per-game realization — now unmistakable in the code.

**Original finding:**

**Where:** [engine/mafia/engine.go:490-492](backend/internal/engine/mafia/engine.go#L490)
(true `ActAbstain`), [engine/monopoly/engine.go:262-281](backend/internal/engine/monopoly/engine.go#L262)
(forces a real move), [engine/goofspiel/engine.go:171-181](backend/internal/engine/goofspiel/engine.go#L171)
(plays lowest card).

The documented universal rule — *turn timeout = abstain, non-responders lose* — is
implemented literally **only in mafia**. Monopoly forces a plausible game-affecting
move (roll / bankrupt / reject_trade) and goofspiel commits the lowest card. These
are defensible "least-harmful default" choices for games with no abstain concept, but
they are **not abstentions**: a timed-out monopoly seat still acts (and can go
bankrupt), a timed-out goofspiel seat can still win a cheap prize.

**Recommendation:** decide whether the rule is meant to be uniform. If yes, document
per-game that "abstain" means "least-harmful legal default" for turn-based games and
make the wording consistent; if the intent is strict abstention, monopoly/goofspiel
need a genuine no-op path.

---

### ✅ FIXED — Mafia request-path `Act` has no Redis-down / OCC resilience (unique among the 3)

> **Status: fixed this pass.** Mafia `Act` was refactored into a lock-fast-path
> `Act` + OCC-retry `tryAct`, a byte-for-byte mirror of Monopoly/Goofspiel: on a
> Redis lock error it now proceeds lockless, and a racing writer (`ErrConcurrentUpdate`
> from the `UNIQUE(match_id, seq)` constraint) is retried up to 4× instead of
> hard-failing. Tests: `TestAct_ProceedsLocklessWhenRedisDown`, `TestAct_BusyWhenLockHeld`.

**Original finding:**

**Where:** [mafia/service.go:268-320](backend/internal/mafia/service.go#L268) vs
[monopoly `Act`](backend/internal/monopoly/service.go) and
[match/service.go:379-401](backend/internal/match/service.go#L379).

Monopoly `Act` and goofspiel `act` **degrade gracefully** on a Redis lock error
(proceed lockless, rely on the `UNIQUE(match_id, seq)` OCC retry loop). Mafia `Act`
does neither — it hard-fails on a lock error and has **no OCC retry** (single
attempt). A Redis blip makes mafia moves fail while the other two keep working.
(Mafia's `HandleTimeout` *does* go lockless, so only the request path is affected.)

**Recommendation:** bring mafia `Act` in line — lockless fallback + bounded OCC
retry, matching monopoly/goofspiel.

---

### 🟢 FIXED THIS PASS — mafia/monopoly `SweepExpired` aborted the whole batch on one error

**Where:** [mafia/service.go SweepExpired](backend/internal/mafia/service.go#L468),
[monopoly/service.go SweepExpired](backend/internal/monopoly/service.go#L576).

Both looped `if err := HandleTimeout(id); err != nil { return 0, err }` — so one
wedged/corrupt table (e.g. a persist error) blocked the timeout, and therefore the
escrow release, of **every other expired table** in that tick, and reported `0`
swept even when others succeeded. The match/goofspiel package already did the right
thing (`errors.Join` + continue + count successes). **Aligned both to that pattern.**
Build + existing tests green.

---

## 3. What's genuinely solid (verified)

- **In-match failure defenses** are strong and well-tested across all three games:
  transport error / timeout / disconnect / illegal move all fall back to a
  deterministic engine-legal default and are classified into benchmark outcomes
  (`ok`/`illegal`/`timeout`/`transport`/`disconnected`) emitted to Pyyol Lens.
  See `drive_bench_test.go`, `remoteplay/goofspiel_test.go`
  (`TestPlayGoofspiel_IllegalMoveFallsBack`, `..._EndpointDownFallsBack`),
  `agentgw/gateway_test.go` (`TestTurnTimeoutFallsBack`, `TestNotConnectedDecider`).
- **Matchmaking money-safety:** claim-before-escrow is atomic; escrow failure
  releases the claim (no double-charge); affordability/certification gate at enqueue
  (`matcher_test.go`).
- **Crash-safety:** goofspiel/monopoly finalize is Settle-before-Finish, re-driven
  idempotently by the sweeper.
- **Hosted-endpoint hardening:** SSRF guard, no redirects, body cap, per-attempt
  timeout + retries, HMAC-signed calls (`agentclient_test.go`).

---

## 4. Tests added this pass (all passing)

- [matchmaking/enqueue_integration_test.go](backend/internal/matchmaking/enqueue_integration_test.go):
  - `TestEnqueueToMatched_FullFlow` — a new agent enqueues via the **public API**,
    a matcher tick pairs it, escrows once, and `Status` flips to matched with the
    match id (previously only tested with directly-seeded status).
  - `TestEnqueueSameOwnerNeverMatches` — collusion guard: same-owner agents never pair.
  - `TestReEnqueueAfterMatchClears` — the play→finish→re-enqueue round-trip.
- [autoplay/autoplay_lifecycle_test.go](backend/internal/autoplay/autoplay_lifecycle_test.go):
  - `TestTick_KeepsPlayingAcrossGames` — the continuous "deploy once, keep playing"
    loop: idle→enqueue, in-match→skip, finished→re-enqueue.
  - `TestTick_RankedIgnoresConnectivity_MoneyBleedGap` — pins the CRITICAL gap above.
  - `TestTick_LossStopHaltsBleedingAgent` — the loss-stop backstop works (when set).

---

## 5. Recommended next steps (need your call — behavior changes)

1. ✅ **CRITICAL — DONE:** connectivity gate for auto-play-ranked (offline agent no
   longer bleeds stakes). *Optional follow-ons:* safe default loss-stop + offline alert.
2. ✅ **HIGH — DONE:** ranked auto-play for mafia/monopoly is now rejected clearly
   (enqueue + enable time) instead of silently no-op'ing / mis-seating. *Future:*
   build real N-player ranked matchmaking for those games.
3. ✅ **HIGH — DONE:** waiting-lobby TTL sweeper for staked mafia/monopoly (no refund
   needed — no escrow before start).
4. ✅ **MEDIUM — DONE:** mafia `Act` Redis-down lockless + OCC-retry parity.
5. ✅ **MEDIUM — DONE:** per-game timeout semantics documented (shared rule, per-game
   realization).
6. ✅ **Test follow-up — DONE:** dedicated mafia + monopoly `SweepExpired`
   batch-resilience tests (`TestSweepExpired_OneWedgedTableDoesNotBlockTheRest`) —
   one wedged table no longer blocks the timeout/escrow-release of the others.
   *Still open:* a live-DB store test for `ExpireStaleWaiting`'s SQL (build-verified today).

**All audit findings are now addressed.** Remaining open items are the two future
*features* (real N-player ranked matchmaking for mafia/monopoly; optional auto-play
loss-stop default + offline alert) and the one live-DB test above.
