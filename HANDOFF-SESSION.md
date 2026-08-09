# Session handoff — 2026-08-09

Read `CLAUDE.md` first for the invariants and build commands. This file is **current state and
the open queue only**.

**Everything is committed.** 28 commits, working tree clean. The previous session's ~47
uncommitted paths were reviewed and landed in coherent chunks (completion binding / deadline fix
/ provider generality / Lens / SDKs), each one built and tested from an exported index before it
was committed, so a bisect means something.

---

## Platform health at handoff

| check | result |
|---|---|
| ledger audit | clean; escrow reconciles exactly — `1,511,600 = 1,511,600 held + 0 open`, 0 unexplained |
| mafia | completing — 780 finished in 6h |
| queue orphans | 0 |
| house never stakes | 0 house-created matches with bid > 0; 0 house agents in a staked match |
| completion binding | active at boot, all three games |
| rollups vs `events_raw` | exact, per meter, at every bucket |
| a fully-bound seat's coverage | 100% (was 76–93%) |
| clean clone | builds; 79 packages pass |

---

## Delivered this session

### Range bindings — coverage now means "decisions a model made"

The headline. Coverage counted CALLS, so one completion bound one round, and an agent that
batched — one call planning three rounds — scored ~33% while playing entirely model-backed.
Phase 4 exists to *reward* that batching, so the two rules pulled in opposite directions and no
threshold reconciled them.

A completion may now declare the rounds it decided. The SDK sends a `plan`, the gateway binds
each round from that one completion (shared `completion_hash`, one receipt per round over that
round's own move), and match-time enforcement is unchanged.

**Measured on real staked tables:**

| archetype | before | after | calls |
|---|---|---|---|
| perfect | 100% | 100% | 13 for 13 rounds |
| batcher (`-bind-batch 3`) | **33–44%** | **100%** | **5 for 13 rounds** |
| flaky (`-bind-fail-pct 15`) | 71–100% | 85–92% | one per round |

The honest floor is now set by provider failures, which is the correct thing to set it — a call
that never happened proved nothing.

**Two guards make over-claiming pointless rather than profitable:**

- A span is a **commitment**. Every round in it is enforced, so submitting anything else is
  rejected. Proven live: match `m_yoyejg3i3oapkr3k` round 2 — bound by the *round 1* call —
  `model_produced="card:1" agent_submitted="card:2"`, refused.
- **Backward claims are dropped.** Rounds before the proven round are already played, so a
  binding over them is coverage nothing will ever check. Forward claims are self-limiting
  because they are enforced; backward ones are not.

`BoundDecisions` and `CoverageFor` now intersect bound rounds with the decision log, so a span
cannot credit a round the agent never played. **That intersection fails open** — a seat whose
decisions were never logged still counts everything, because `BoundDecisions` feeds the rule
that voids staked matches. Verified on live data: 25 of 26 seats unchanged, **zero newly at
zero**. The one that changed held a binding for round 24 of a 13-round match.

Contract pinned in all three languages: `sdk/conformance/move_binding.json` gained 8
`plan_cases` beside the 29 single-move cases. Go 8/8, Python 332 passed, JS 219 (was 211).

### Phase 3 — staked but unranked

Unverified agents keep playing staked and winning coins; they are gone from the published
ladder. **Filters the publication, never the computation** — ratings still update for everyone,
because dropping 55 of 75 rated agents from the maths would degrade the verified agents' own
numbers.

One predicate, `publishedAgent()`, shared by the board, the rank snapshots (or `trend` reports
movement nobody made) and `AgentStanding`. Standing gained `ranked`, because omitting a
developer from the ladder while still showing them a rank is the one outcome the policy must not
produce.

Live: the ladder went from a top eight that was **entirely unverified** — led by an agent on
1791 Elo with 900 coins won — to exactly the 20 verified agents. `ag_kei3rfkzy2ybepmo` now
reports `ranked: false`, elo 1791, coins 900 intact.

Mutation-verified: replacing the predicate with `TRUE` fails the integration test.

### Phase 2 — the threshold is ready; ADOPTION is what blocks it

`RANKED_INTEGRITY_MIN_PCT` stays 0, but for a different reason than before. The metric is fixed.
The number it should take is **50**, and the binomial false-void table is now in the comment at
the wiring site: at 50% an honest agent with a pessimistic 15% provider failure rate is wrongly
voided about once in 800 matches, and the cliff is between 60 and 70.

**What actually blocks it:** rule 2 is absolute, and over the last 48 hours of staked ranked
play **3863 of 3895 seats proved nothing**; a threshold of 50 would have voided 3523 of them.
The gate is no longer a measurement — it is that routing through the gateway becomes the norm.
The comment names the query that answers "is it yet".

### A cryptographic proof now outranks the timing guess

The timing detector infers "a human is playing this by hand" from response-time distribution and
flags the agent ineligible; ~112k such flags sat on deterministic agents, and the matchmaker
could not pair them. Completion binding answers the same question directly — a human cannot
produce a bound decision, because the match rejects any move that is not the model's.

Not an exemption cut into a fraud control, and two properties keep it that way: **90% of
decisions must be proven** (mutation-verified — relaxing to `bound > 0` fails the test), and an
**unreadable proof leaves the flag standing** (deliberately the opposite direction to
`movebind.Enforce`; an exemption reachable by breaking the database is not a control).

### Lens

- **`span_id` was set by nothing** — 0 of 129,760 events — so the `spans` projection produced
  zero rows forever. Decided in favour of the emitter setting it: the events this service emits
  are leaves, so the event's own id IS the span id, which is already unique and already the
  dedup key. Live: 318 spans, 133 traces, 7 span types.
- That exposed a latency bug the empty table was hiding: the backfill derived span latency from
  `min/max(event_time)`, which is 0 for a single-event span. Every model call reported 0ms. Now
  real: 260ms, 123ms, 116ms.
- **All ten projections now name their columns**, not just `spans`.

### A critical ledger alert — and my own wrong fix for it, retracted

Found by reading the audit log after a deploy rather than by any test, after I had already
drafted a handoff saying "ledger audit clean":

```
LEDGER INTEGRITY VIOLATION check=escrow_unexplained severity=critical rows=2700
"the stake was taken and there is no story for where it went"
```

**My first fix was wrong and I have reverted it.** I widened the escrow reconciliation to accept
a `held_settlements` row as explaining retained coins — without checking the production path
before changing a fraud control.

`held_settlements` is not a second hold record. `payout_holds` says a payout IS held;
`held_settlements` carries the split to replay on release. **Every** deny branch of the real gate
(`antifraud.Service.Allow`, all four) calls `RecordHold`, so a genuinely held match always has
both. The state I taught the audit to accept is one production cannot produce — it came from a
test whose `denyGate` stub refused without recording a hold. Widening would have masked exactly
the defect the check exists to catch: a deny path that writes the payout map and forgets the
hold, leaving coins retained with nothing marking them retained.

The check is strict again, the fixture is fixed instead, and the regression test pins both
directions — a split alone must still fire, and the hold state must clear it. It asserts on a
DELTA rather than the global figure, because the audit reconciles the whole database and any
unrelated finding would otherwise decide the result, which is how the first version misled me.

The three stale fixtures were repaired by recording their **missing hold markers**, not by
deleting anything: each carries four balanced ledger entries, so removing the transactions would
have orphaned real coin movements. No ledger row was altered.

### Six integration tests were never cleaning up

`defer pool.Close()` runs when the test function *returns*, which is before every `t.Cleanup`.
So each cleanup was deleting rows through an already-closed pool and silently doing nothing,
because those deletes ignore their errors. That is how `ag_covitest` and three staked Mafia
fixtures came to be sitting in the lab database — the 2700 coins above were one of them.

Now `t.Cleanup(pool.Close)` registered first, so LIFO closes the pool last. Verified: after a
run the seeded agents, matches and ledger rows are gone.

### Two `.gitignore` rules were silently excluding source

Found by exporting the git index to a clean tree and building *that* rather than the working
directory.

- `coverage.*` matched `internal/rating/coverage.go`. **A fresh clone did not compile** and
  `git status` stayed clean, because ignored files are not reported.
- `test/` was unanchored, so it matched `sdk/js/src/test/`. **Four JS test files had never been
  tracked** — including `conformance.test.ts`, the cross-language drift guard itself. The stated
  contract is that the fixtures are enforced in three languages; a fresh clone enforced them in
  two.

---

## Corrections on the record

**I reported "full suite green" from a broken pipeline.** The command was
`go test ./... | grep -v '^ok'`, so the exit code was *grep's*, not the test run's — precisely
the trap `CLAUDE.md` warns about, which I had read. Re-run properly, the suite is green (79
packages, real exit 0), but the claim was unfounded when I made it. Every verification in this
file was re-run with `cmd >log 2>&1; echo EXIT=$?`.

**I reported a rollup/`events_raw` mismatch that was my own query error.** I compared
`prompt_tokens + completion_tokens` against the rollup's `sum(total_tokens)`. They are different
quantities. Both meters reconcile exactly.

**A second rollup discrepancy was real, and it is a race, not a defect.** A backfill run while
matches were in flight left the rollups +5,220 gateway / +6,643 sdk tokens: the rollups are
re-derived by truncate-and-reselect while the processor writes a delta per arriving event, so an
event landing in between is counted twice. With traffic stopped and the consumer drained, the
same run landed on ground truth exactly. Now documented at the function with the command that
checks it — **quiesce the pipeline before backfilling.**

---

## Match replay ("the clips section") — WIRED END TO END

A developer whose agent played while they were asleep can now find the match and watch it back.
Every piece already existed; nothing connected them, and the conversation was being dropped.

| step | where |
|---|---|
| pick **Ranked** or **Sandbox** | `/traces` — two tabs, each with its own totals |
| the games, newest first, paginated | `/traces` — 20/page, page in the URL so a link to page 4 is a link |
| open one | `/traces/{id}` — decisions, timings, what it cost |
| **watch it back** | **new** — "Watch the replay" → `/live-arena/{game}?match={id}` |

The replay reuses the **same viewer that renders a live match**, given `?match={id}`:
`useGoofspielLiveScript` already fetched `/v1/match/{id}/replay` and returned `mode:"replay"`.
A second viewer would have drifted from the live one, so the fix was the missing link, not a
new renderer. Mafia and Monopoly have the same route and their own fetchers.

**The table talk was being dropped.** The arena records every line an agent says — a real
13-round match stores 61, interleaved with the play — and none of it reached the viewer:
`goofEventsFromWire` had no `agent_says` case, and `mapEventsToScript` discarded the `think`
kind as *"no live source for per-move reasoning"* (true of the old stream, wrong for a
recording). Watching a match back showed the moves with the personality stripped out.

Both halves were needed: a step renders as chat only when it carries **speaker AND text**, so
setting one is silently invisible. `agent_says` was also added to the live SSE subscription, or
live and replay would disagree about whether a match had any conversation.

Verified against a real recording from the running arena:

```
115 events → 13 rounds, 13 reveals, 61 chat lines   (was 0)
```

Mutation-verified (restoring the `break` fails two of four new tests), ordering pinned so a line
lands against the move it accompanied, blank says dropped. `tsc` clean, 110 frontend tests pass.
Committed in `Pyyol_client` (separate repo) as `36d19f3`.

### UI glitches removed, and a bug in the replay link itself

Three faults, one cause: the viewer hooks had to answer before they knew anything.

- **The flash.** On first render, with discovery still in flight, each hook fell straight
  through to its last branch — the scripted DEMO on the dashboard, or "no match right now" in
  the Live Arena — and swapped a moment later. So a fabricated game, or a denial that anything
  was happening, rendered in front of the real thing. `loading` is now a mode of its own and
  each viewer renders a skeleton sized like the board it becomes, so the page does not jump
  either.
- **"Watch the replay" did not replay.** Every hook's replay effect opened with
  `if (preferredMatchId) return;` — naming a match SKIPPED the recording. A finished match is
  the only kind anyone watches back, so the one path the feature exists for had no source.
- **A finished match could masquerade as live.** `/watch` serves a finished match's backlog, so
  connecting to a named one flipped `live` true and rendered it under a LIVE ribbon. The hooks
  already refused this for DISCOVERED matches; naming one bypassed the check.

All three games, because all three carried the identical code.

### Broken images

`profile.avatar ? <img/> : initials` only asks "is there a url", never "did it load", so a dead
url rendered the browser's broken-image glyph inside the avatar circle with the initials sitting
right there unused. `SafeImg` drops the image on error and renders the fallback the surrounding
component would have shown anyway — applied to the three places a user's own picture appears
and to the dashboard game tiles. The failure is keyed BY URL rather than a boolean reset in an
effect, so a new src still gets a fresh chance without an extra render.

### Checked and found already correct

- **Telemetry** (`/traces/telemetry`) renders everything the backend sends — KPIs, failures by
  cause, round hotspots, latency percentiles, and the per-arena table — and is already
  responsive (the wide table scrolls in its own container rather than stretching the page).
- **Ordering and pagination**: `ORDER BY COALESCE(started_at, created_at) DESC`, 20 per page,
  page in the URL so a link to page 4 is a link.

**Left for whoever picks this up:** the mode chooser is pills (All / Ranked / Sandbox) rather
than two large cards — it works, is URL-driven and responsive, so it was left alone rather than
restyled at the end of a long session; say the word if cards are wanted. The "Watch the replay"
action is on the match DETAIL page only. Mafia/Monopoly replays share the fixed hook and are
covered by the same code path, but only Goofspiel was driven end to end against a real
recording. `replay_hash` is still `""`, and a match with no recording returns `"events": null` —
the replay page should say "no recording" rather than render an empty board.

## Open queue, in priority order

### 1. Phase 4 — reward cost skill (PARTIALLY done)

Done: the proof-outranks-timing half, and the metric it depends on.

**Not done.** Cache-hit ratio, cost per decision, and tokens per decision are not in the P-Index.
`cost_per_win`, `tokens_per_decision` and coverage-gated cost basis already exist in
`internal/rating` (`coverage.go`, `edge.go`, `groups.go`) — the gap is the P-Index integration,
the sample-size floor and the published interval. Rank on **quality per cost**, never cost
alone. Read `meter_source='gateway'` only.

Also unaddressed: `/v1/leaderboard/developers` ranks developers by P-Index rather than agent
Elo, so Phase 3's filter does not apply to it. Whether cost-and-quality rankings should exclude
unverified developers belongs here, where those figures are defined.

### 2. Groq end-to-end — NOT STARTED

One real coin game, prompt caching on, traces verified. The key is still at `groq.env` in the
**previous** session's scratchpad
(`/private/tmp/claude-501/-Users-macbookair-pyyol/a725c9c8-.../scratchpad/groq.env`, mode 600).
Groq is OpenAI-wire so cache should be `prompt_tokens_details.cached_tokens` (a *subset*) —
confirm rather than assume, and add a fixture. Verify: move bound, cost non-zero, cache tokens
present, `meter_source=gateway`, span in ClickHouse. **Rotate the key afterwards.**

### 3. A rejected move should earn no deadline extension — DECIDED, not implemented

Decided: it should not extend. A seat whose move was refused is not waiting on a model, and
extending holds up an opponent who staked real coins. The reasoning is at `tryExtend`.

Not implemented because "was a move rejected for this seat this round" is not derivable from the
match row, the decision log (a refused move never becomes a decision) or the liveness probe, and
must survive across instances. It needs a durable per-(match, round, seat) marker written by
`tryAct` — a schema change plus a settlement-affecting behaviour change, which deserves its own
commit.

### 4. ~~Pre-existing integration-test failures~~ — DONE

All six are resolved and `go test ./internal/store/` is green on a clean database. Three were
real fixture defects (a "gateway-verified" seat with no decision log or bindings; a test that
never migrated; a deny-gate stub that withheld a payout without recording the hold). Four were
tests asserting on a global worker's return value, which measured the database rather than the
scorer — those now assert on their own rows and SKIP, with the backlog size, when a shared
database makes them meaningless.

A finding fell out of it: the skill backlog is **2.57M unscored decisions, 99.5% Monopoly**
actions the scorer declines by design, with the ~11.7k scorable Goofspiel rows queued behind
them. Every live batch logs `scored:0 unscorable:500`, so the skill dimension of the P-Index is
receiving nothing while that drains. Belongs with Phase 4.

### 5. Unify the two hold records

Have the Mafia hold path write `payout_holds` alongside `held_settlements`, so "these coins are
retained" is one fact rather than two that can drift. The audit now reads both, which makes the
alert correct — it does not make the data model right.

### 6. Phase 5 backlog, then Phase 6 publish LAST

107 suppressed frontend lint errors; JS SDK warnings; full `-race` suite >10min; the 38
`matchmaker pairing failed` from `high_human_likelihood` — **recheck these now**, the
proof-outranks-timing change is aimed squarely at them and was not re-measured after deploying.

Publish only when the rest is green: the SDK contract changed in Phase 1 and changed again with
range bindings.

---

## What I did NOT verify

- **Two test suites hit Go's 10-minute default timeout** and need `-timeout` raised (or a
  drained database): `internal/modelboard` under `-race`, and `internal/store` against the LAB
  database (the skill tests each spend ~40–90s measuring a 2.57M-row backlog). Both pass
  otherwise — `internal/store` is green end to end on a clean database.
- **Mafia and Monopoly range bindings end to end.** `CanonPlan` is game-general and the Mafia
  no-target case is pinned in the shared fixtures, but only Goofspiel was driven through a real
  match with a span.
- **The 38 pairing failures.** The fix is deployed; the count was not re-measured.
- **Streamed range bindings.** The stand-in provider emits a plan over SSE and `ExtractStream`
  feeds the same `CanonPlan`, but no `-bind-stream -bind-batch` run was made.
- **Any frontend surface.** `ranked` is served by the API; nothing consumes it yet.

---

## Environment left running

- **Arena:** `pyyol-backend` (rebuilt from HEAD), `pyyol-pg`, `pyyol-redis`, `pyyol-web`.
- **Lens:** `pyyol-lens-{clickhouse,postgres,nats,minio,ingest,processor}`. `query-api` stopped
  (it requires `QUERY_API_KEY`, not weakened for a test). The processor container carries
  swapped `/usr/local/bin/backfill`.
- **Lab:** `pyyol-toolprovider` — **now serving from THIS session's scratchpad**
  (`/private/tmp/claude-501/-Users-macbookair-pyyol/025a7dd5-.../scratchpad/toolprovider.py`),
  extended with an `X-Lab-Plan` header so "the model" can decide several rounds in one call.
  That path is ephemeral; copy the script somewhere durable before relying on it again.
- Spent population containers were removed.

`backend/.lab-server` and `backend/.lab-gamelab` are rebuilt from HEAD. Both are gitignored, as
are `tracing/backend/.backfill|.processor` and `tracing/docker-compose.override.yml` (added this
session — they were untracked and unignored, so `git status` kept offering 24MB of binaries).
