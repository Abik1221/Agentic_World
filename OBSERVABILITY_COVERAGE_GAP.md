# The benchmark recorder is attached to the driver, not to the match

**Status:** FIXED and verified end to end (2026-08-07). Kept for the reasoning and the evidence.
**Found:** 2026-08-06, while wiring the model board's data path.

## The finding in one line

Per-match benchmark instrumentation lives inside the platform's **drive loops**, so any agent
that plays by calling `Act` directly — the pull model — produces no benchmark fact, no decision
log, and no board presence, in any game.

## Evidence

Measured against the lab database, not inferred:

| table / stream | contents |
| --- | --- |
| `agent_match_benchmark` | 556 rows, **all goofspiel**. Zero Monopoly, zero Mafia. |
| `events` where `type = 'match.benchmark'` | 297, **all goofspiel**. Zero Monopoly. |
| `agent_match_decisions` for `mp_%` matches | **zero** |
| finished, rated Monopoly matches | **2067** (2105 total, one rateable seat each) |
| `match_events` for Monopoly | **8,460,732** — the matches genuinely played |

So the matches happened in volume and the benchmark event was never *emitted*, rather than
emitted and dropped downstream.

`benchmark.NewRecorder` appears in exactly four places, and every one is a driver:

| path | file | instrumented |
| --- | --- | --- |
| goofspiel, ranked platform drive | `internal/match/drive.go:227` | yes |
| goofspiel, sandbox push-play | `internal/sandbox/pushplay.go:181` | yes |
| monopoly, push-play only | `internal/monopoly/pushplay.go:192` | yes |
| mafia, push-play only | `internal/mafia/pushplay.go:228` | yes |
| **any game via `Service.Act`** | — | **no** |

Ruled out along the way, so nobody re-checks them:

- `Flush` is correctly deferred in every driver.
- `p.persist` is correctly wired — `SetBenchmark` copies onto the pushPlayer and runs after
  `EnablePushPlay`.
- The `match.benchmark` projection in `cmd/server/main.go` is game-agnostic.
- `Recorder.Empty()` is true only when neither `Record` nor `SetAgentMeta` was called, so the
  recorders in question were never touched at all.

Goofspiel masks the problem because it has **two** instrumented paths and its ranked play is
always platform-driven. Monopoly and Mafia have one each, and the pull path is the common one.

## Why it matters more than the row count suggests

Every published figure is affected, not only the new model board:

- **P-Index Intelligence** folds `match.benchmark` seats — so it scores Goofspiel only.
- **Model benchmark and developer edge** read `agent_match_benchmark`.
- **Decision-quality scoring** (`internal/skill`) reads `agent_match_decisions`.
- **Model board** (`internal/modelboard`) reads both.

Each of those currently describes one game while presenting itself as describing the platform.
This is the same shape as the defects fixed earlier in this session: the number is not wrong so
much as it is silently narrower than its label.

It also subsumes two previously queued findings, which share this root:

- Mafia decision-quality scoring "unwired" — there is no decision log to score.
- Monopoly scorer "never run on live data" — there is no live data in the tables it reads.

## The fix, and why it is not a one-liner

The recorder is per-match state with a lifecycle: create at match start, `Record` per decision,
`SetAgentMeta`/`SetResult` at the end, `Flush` once. `Act` is a stateless per-request handler,
so there is nowhere in it for that object to live.

Two workable shapes:

1. **Per-decision durable events, aggregated at match end.** `Act` emits one durable decision
   event (the machinery already exists — `s.decisionTracer` in the push paths), and a match-end
   hook folds them into the `match.benchmark` summary. Keeps `Act` stateless and gives every path
   the same treatment. Costs one write per decision.

2. **A match-scoped recorder in the service**, keyed by match id, updated by `Act` and flushed by
   the existing finish hook. Fewer writes, but introduces mutable per-match state that has to be
   reaped on abandonment and made safe across instances — the kind of state this codebase has
   deliberately kept in Postgres rather than memory.

(1) is the better fit for how the rest of the platform is built, and it has the property that the
data survives an instance dying mid-match.

Whichever is chosen, the acceptance test should be behavioural rather than structural: play one
Monopoly and one Mafia match through `Act` and assert a `match.benchmark` event, an
`agent_match_benchmark` row per rateable seat, and a non-empty `agent_match_decisions` log. A test
that merely asserts "a recorder exists on this path" would pass while emitting nothing, which is
exactly the current state.

## What was fixed alongside this

`SetTurnMinter` now propagates to an already-built pushPlayer in all three games (commit
`763e0ac`). Monopoly called it *after* `EnablePushPlay`, which copies `s.turns` by value, so the
pusher held `nil` and every Monopoly view shipped with no turn proof — no Monopoly decision could
ever bind and no Monopoly agent could earn Verified. Mafia had identical code in the opposite
order and worked, so the behaviour was decided by a line number with nothing hinting order
mattered. The regression test asserts order-*independence* rather than a specific order.

## Consequence to state plainly

Until this is fixed, **two of three games contribute nothing to any board**, and every figure the
platform publishes is a Goofspiel figure. The model board surface (handler, snapshot job,
methodology page) is deliberately **blocked** on this: publishing a page that describes a
game-theoretic model board fitted on Goofspiel-only data would misrepresent what it measures.

---

## Resolution

Fixed across `7e84731` (store layer), `dc057e0` (Monopoly Act), `33a36b9` (Mafia Act) and
`20bf6cc` (aggregation moved to `finalize`).

Verified on a live Monopoly match played entirely through the request path — 269 actions, zero
rejections, played to a genuine finish:

| check | result |
| --- | --- |
| decisions recorded | **269** (this table was always empty for Monopoly) |
| seat benchmark row | **present**, `result = loss` |
| aggregate vs decision log | 269 decisions / 269 legal — reconciles exactly |
| `latency_sum_ms` | **0**, correct: request-path latency is deliberately not reported |
| platform-wide Monopoly benchmark rows | **0 → 566** |

Two things this exercise caught that no test would have:

- **The aggregation was initially hooked to `Act`**, so any match ending by sweeper, forfeit,
  deadline or a bot's final move recorded every decision and then produced no seat row. That
  repeated the very mistake this document describes — instrumentation bound to a transport rather
  than to the event it describes — and was found by asking "can a match finish without an `Act`?",
  not by a failing test. Now in `finalize`, the one point every ending passes through.
- **A stale binary.** Two verification rounds ran against a server built before the fix, which is
  why an earlier finished match showed no row. Rebuild before believing a live result.

### Still worth doing

- A reusable Monopoly conformance driver. The one used here is written against the engine's real
  phase model (every phase has a chosen action; `bid`/`build`/`mortgage` need extra fields) and
  finishes reliably by never acquiring, but it lives in a scratch directory.
- The auction error message: `bid` is listed legal, but a bid without an `amount` is refused with
  "That action is not legal in the current phase" — the message blames the phase when the real
  problem is a missing field. It cost a debugging round here and will cost developers the same.
