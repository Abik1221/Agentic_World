# Session handoff — 2026-08-08

Read `CLAUDE.md` first for the invariants and build commands. This file is **current state and the
open queue only**.

**Nothing is committed.** ~43 changed/new paths in the `Agentic_World` working tree.

---

## Delivered and verified

### Phase 1 — completion binding (the session's main goal)

A turn proof used to be `HMAC(agent|match|round)`: it proved a CALL happened, not that the model's
answer drove the move. An agent could call the model, ignore the response, and submit a scripted
move with every proof valid.

Now: the gateway extracts the move from the model's own structured tool call, mints
`HMAC(agent|match|round|completion_hash|extracted_move)`, and the three game services reject a
submitted move that contradicts it.

- New: `internal/movebind` (extraction + canonical forms + the enforcement rule), migration `0084`.
- `turnproof.MintDecision` / `VerifyDecision`, domain-separated from the v1 token so a turn token
  can never be presented as a receipt.
- Enforced in `match`, `mafia`, `monopoly` `tryAct` — beside the `movesig` check, NOT in handlers.
- Wired in `cmd/server/main.go` (`SetBoundMoveReader` on all three services).
- SDKs: `pyyol.movetools` / `src/movetools.ts` with per-provider tool envelopes and `tool_choice`.

**Live proof, both halves:**

| | evidence |
|---|---|
| honest play binds | staked table `m_v6ntsh3dsl6nf4gf` (bid 500, rated), 13/13 turns bound both seats, finished |
| substitution rejected | `m_sspdkaz3rob2np35` — server log names `model_produced="card:5" agent_submitted="card:1"` for both seats |
| cheat forfeits, does not stall | `m_huomhkeekafbmewb` finished in 22m49s with a winner, 27 extensions over 13 rounds (~2.1/round, policy max 3) |

Guards were **mutation-verified**: flipping `movebind.Check` to always-allow makes
`TestEnforceRejectsASubstitutedMove` and the substitution subtest fail.

### Bug found by watching the DB: deadline extension runaway

`tryExtend` derived "extensions granted so far" from `elapsed`, but measured `elapsed` from
`RoundDeadline` — which the extension itself moves forward. So the count never climbed and the
ceiling was never reached. **Goofspiel granted 17 extensions against a `MaxExtensions` of 3, holding
one round open for 12 minutes.** Pre-existing, but only reachable in practice once binding could
reject a healthy agent's moves.

Fixed with migration `0085` (`round_deadline_base`, an origin no extension touches) and a test that
encodes both origins: `fixed origin: 3 extensions (bounded), moving origin: unbounded`.

### Provider generality (all three languages)

Was structurally OpenAI/Anthropic-only. Gemini, Mistral, DeepSeek, Cohere and every self-hosted
server were unrouted, uncosted, unbindable — **silently**.

- Structural tool-call walk replaces 4 hardcoded shapes.
- Semantic usage normalization replaces the vendor table, with canonical-envelope priority (a decoy
  test proved the weakness first: it read `prompt_eval_count: 9999` instead of `usage: 10`).
- Wire-format routing replaces the provider→path table.
- `sdk/conformance/move_binding.json`: **29 cases**, all three languages agree, including 8 shapes
  with no code written for them.
- Previously $0, now costed: DeepSeek caching, Cohere, Ollama, Bedrock, vLLM, **and every streamed
  call** (the gateway never captured streamed bodies at all — that also fixed zero-usage streaming).

### Traceability end to end

Lens stack brought up; a gateway span reached ClickHouse carrying `extracted_move=card:6`,
`turn_bound=1`, cache 1500/600, `prompt_tokens=2520`, `meter_source=gateway`,
`session_id=goofspiel`. A developer disputing a rejected turn can now see the evidence.

### Rollup meter blend

`rollup_hourly` summed the gateway span and the agent's self-report into one bucket:
`66,367 = gateway 33,930 + sdk 32,437`. Every routed call double-counted, a measurement blended with
a claim. Migration `006` adds `meter_source` to the sorting key of all three rollups; write path and
backfill `GROUP BY` updated. Proven through the real projection SQL — separate rows per meter.

### Coverage denominator (and a correction)

`CoverageFor` counted `DISTINCT seq` for every game, but `seq` is a *submission counter* in
Goofspiel while the numerator counts `DISTINCT round`. Retries inflated the denominator: six seats
that bound every round reported **76–93%** instead of 100%. Fixed to resolve the proof slot per game.

**Correction on the record:** I first reported this as blocking the ranked threshold. It was not.
The share rule derives its denominator from the engine's round count
(`rankedIntegrityFailed(… len(state.History) …)`) and was never affected. Only the display figure
(badge, `pyyol doctor`, "your verified share") was wrong.

---

## Open queue, in priority order

### 1. ~~Lens processor writes no rollup rows~~ — RETRACTED, this was my error

I reported this as a pre-existing defect. **It is not one.** The processor writes rollups correctly
and the meter separation works live:

```
rollup_hourly 20:00   gateway 107,010   sdk 305,934   '' 0
events_raw    20:00   gateway 107,010   sdk 305,934   '' 0     ← exact match
```

What I actually saw was **consumer lag I caused myself**. Recreating the NATS container left the
`pl-processor` consumer ~12,958 messages behind (`delivered 39,266 / last_seq 52,224`). I checked
`rollup_hourly` at 20:24 while that backlog was draining, found no 20:00 bucket, and concluded the
write path was broken. Once it caught up (`processed_at` within 3s of `now()`) the bucket appeared,
split by meter.

**The lesson, not the bug:** I asserted a defect from a single point-in-time observation of a
pipeline I had just restarted. The check that would have caught it in one command:

```bash
docker exec pyyol-lens-nats sh -c "wget -qO- 'http://127.0.0.1:8222/jsz?consumers=true&streams=true'"
# compare consumer delivered/ack_floor against stream last_seq before concluding anything
```

So the rollup meter fix is **verified live end to end**, not merely in isolation. Nothing to do here.

### 2. ~~`backfill-projections` cannot run~~ — FIXED, and it hid a second, worse bug

**The reported failure:** the `spans` projection selected 21 expressions into a table the phase-10
telemetry migration had widened to 38 columns. Because the projections run in sequence, that one
broken query meant *no* projection could be rebuilt. Fixed by giving it an explicit column list —
the 17 later columns take their defaults, and a 39th added tomorrow cannot break it again. Every
other projection here is still a positional insert, so they carry the same latent risk.

**Why nobody noticed:** `spans` would have produced 0 rows anyway — **0 of 57,301 events carry a
`span_id`**, so the arena emitter never populates it. The `spans` table is dead weight for arena
traces today, and any UI reading it shows nothing. Worth deciding whether the emitter should set
`span_id` or the table should be dropped from the arena's projection set.

**The worse bug it was hiding:** the rollups are `SummingMergeTree`, so re-inserting an aggregate
ADDS to it. A backfill run without reset silently **doubled** them — measured 381,060 tokens against
a true 190,530. Nothing errored; the numbers were simply twice reality, which on a cost board is
worse than a crash. And the reset flag could not be relied on to prevent it: it compares the env var
against the literal `"true"`, so the obvious `BACKFILL_RESET=1` reads as false and hands you the
unsafe path while you believe you asked for the safe one.

Fixed by always truncating the three rollup tables before re-deriving them, regardless of the flag —
re-deriving a rollup from `events_raw` is by definition a full replacement. **Proven idempotent:**
two consecutive runs with no reset both land exactly on ground truth
(`gateway 190,530 / sdk 588,099`), and the doubled data is repaired.

### 3. ~~Integration test for `CoverageFor`'s per-game denominator~~ — DONE

`internal/store/coverage_integration_test.go`. Skips unless `PYYOL_TEST_DATABASE_URL` is set:

```bash
docker run --rm --network pyyol-lab -v "$PWD":/r \
  -v pyyol-gocache:/root/.cache/go-build -v pyyol-gomod:/go/pkg/mod -w /r/backend \
  -e PYYOL_TEST_DATABASE_URL='postgres://pyyol:pyyol@pyyol-pg:5432/pyyol_lab?sslmode=disable' \
  golang:1.25-alpine sh -c "go test -count=1 -run TestCoverageDenominator ./internal/store/"
```

Covers both directions, which is the point — Goofspiel where counting submissions OVERstates the
denominator (a retried round), and Mafia where counting the day would UNDERstate it (night and
voting are separate proof slots on one day).

**Mutation-verified:** reverting the query to the old `seq`-for-all-games denominator makes it fail
with `denominator = 4, coverage = 0.75`. It can fail, so it is a real guard.

### 4. Phase 2 — `RANKED_INTEGRITY_MIN_PCT`: MEASURED. Leave it at 0, and fix the METRIC first.

I built the realistic population rather than guessing (`gamelab -bind-fail-pct`, `-bind-batch`),
because the previous 100% readings came from a harness that binds every round by construction —
that measures the rig, not agents. Three honest archetypes on real staked tables:

| archetype | behaviour | bound / rounds | coverage |
|---|---|---|---|
| perfect | binds every turn | 6/6, 6/6 | **100%** |
| flaky | 15% of model calls fail (5xx/timeout); plays on unbound | 7/7, 5/7 | **71–100%** |
| batcher | one call plans 3 rounds | 4/9, 3/9 | **33–44%** |

**The honest floor is set by BATCHING, at ~33%.** Not by failures.

**And that is a direct conflict with Phase 4.** Phase 4 exists to *reward* cost optimisation
("Cost optimisation must be rewarded, never penalised"). Batching is textbook cost optimisation —
fewer calls for the same play. But the share rule counts bound DECISIONS, so the cheapest honest
agent looks like the least verified one. As specified, the two phases pull in opposite directions,
and no choice of threshold reconciles them:

- Set it **above ~33%** → voids honest batching agents, i.e. punishes exactly what Phase 4 rewards.
- Set it **below ~33%** → so weak it barely constrains anything (a cheat binding 1 round in 3 passes).

**So the metric is wrong, not the threshold.** The fix: let a binding COVER A RANGE. If one
completion legitimately decides rounds 4–6, those three rounds *are* model-backed and should each
count. Concretely — the SDK declares the span the completion covers, the tool call carries a move
per round, and the gateway binds each round from that one completion (shared `completion_hash`, one
receipt per round). Coverage then means "decisions a model actually made" rather than "calls made",
after which a HIGH threshold is both safe and meaningful, and batching improves cost per decision
without hurting coverage.

**Interim: leave `RANKED_INTEGRITY_MIN_PCT` at 0.** That is now an evidence-backed choice rather
than caution. Rule 1 — the zero-proof gate — is already active, self-calibrating, and catches the
case that actually matters: a seat proving NOTHING while another seat at the same table proved
something. The share rule adds nothing until the metric credits batched decisions.

Reproduce the measurement:

```bash
/src/.lab-gamelab -game goofspiel -tier low -bind                      # perfect
/src/.lab-gamelab -game goofspiel -tier low -bind -bind-fail-pct 15    # flaky provider
/src/.lab-gamelab -game goofspiel -tier low -bind -bind-batch 3        # batching
```

Failures are deterministic on (match, round, seat) so a run is repeatable — a number that decides
whether real matches get voided should not move between runs.

### 5. Consider: a rejected move should earn no deadline extension

Bounded now (3/round), but an agent that submitted and was *refused* is not "still thinking".
Would cut a cheating seat's forfeit from ~2 min/round to one window. Behaviour change, not a bug.

### 6. Phase 3 — DECIDED: **staked but unranked**. Half of it is not yet true.

The user chose: an agent that never routes may play staked and win coins, but is excluded from the
ranked surfaces. The incentive to verify is reputational rather than financial.

I told them this was "already the case". **It is only half true, and I checked rather than left the
claim standing:**

| surface | excludes unverified? |
|---|---|
| Model board (`internal/modelboard/build.go:140`, `no_verified_model`) | YES — correct, there is no model to attribute |
| Arena skill ratings (`ratings` table) | **NO — 69 of 87 rated agents are unverified** |

So the decision requires work: unverified agents currently carry arena skill ratings.

**But do not simply filter them out.** TrueSkill/Elo quality depends on a connected comparison
graph, and removing 79% of the rated population would degrade the ratings of the VERIFIED agents
too — the same separability concern the model board already tracks and publishes. Options, roughly
in increasing cost:

1. **Keep rating them, stop PUBLISHING them.** Ratings continue to be computed (so the comparison
   graph stays intact and verified agents' numbers stay meaningful), but unverified agents are
   filtered from the public ladder. Cheapest and preserves statistical quality.
2. **Publish them in a separate, labelled tier** ("unverified"), so the arena is honest about what
   each number means without pretending the play did not happen.
3. **Exclude from rating entirely.** Cleanest conceptually, worst statistically — and it would make
   an agent's first verified match its first rated one, which is a harsh onboarding cliff.

I did not implement any of these: which one is right depends on what the ladder is FOR, which is a
product call. My recommendation is (1) — it delivers exactly what "unranked" promises while costing
the verified agents nothing.

### 7. Phase 4 — reward cost skill (now unblocked by the rollup fix)

Cache-hit ratio, cost per decision, cost per win, tokens per decision in the P-Index. Rank on
**quality per cost**, never cost alone — cheapest is trivially won by the weakest model answering
badly. Floor the sample size and publish an interval. Read `meter_source='gateway'`.
Also consider making a cryptographic proof OUTRANK the statistical timing guess when both are
present (~112k `verification_pending: high_human_likelihood` are the timing detector correctly
flagging deterministic lab agents).

### 8. User-requested: one real coin game via Groq

Prompt caching on, verify traces and accuracy end to end. Key is in the session scratchpad at
`groq.env` (mode 600, outside the repo) — **the user will rotate it when testing is done.**
Groq is OpenAI-wire, so its cache field would be `prompt_tokens_details.cached_tokens` (a *subset*,
handled by the prompt-family rule) — but confirm what Groq actually returns rather than assume.

### 9. Phase 5 backlog / Phase 6 SDK publish (LAST)

107 suppressed frontend lint errors; JS SDK 68 warnings (0 errors); full `-race` suite >10min;
38 `matchmaker pairing failed` from `high_human_likelihood` (still occurring — recheck after 7).
Publish only when 1–8 are green: the SDK contract changed in Phase 1, so shipping earlier forces an
immediate breaking release.

---

## Environment left running

- **Arena:** `pyyol-backend` (rebuilt with `TURN_PROOF_SECRET`, `PYYOL_LLM_GATEWAY_ENABLED=true`,
  Lens env, upstreams pointed at `pyyol-toolprovider`), `pyyol-pg`, `pyyol-redis`, `pyyol-web`.
  Also joined to `tracing_default` so it can reach `ingest-api`.
- **Lens:** `pyyol-lens-{clickhouse,postgres,nats,minio,ingest,processor}`. `query-api` is stopped —
  it requires `QUERY_API_KEY` and I did not weaken that guard for a test.
- **Lab:** `pyyol-toolprovider` (stand-in Anthropic endpoint returning a `play_card` tool call, card
  from an `X-Lab-Card` header; also serves an SSE variant), plus spent `pyyol-{final,trace,rollup}`.

Files of mine outside `backend/`: `tracing/docker-compose.override.yml` (drops only the NATS/MinIO
host port publishes — another project on this machine owns 4222/9000) and `tracing/.env` (copied
from the example, `INGEST_API_KEY` appended). `pyyol-lens-backend:e2e` was tagged `:latest`.
Swapped binaries live in the processor container: `/usr/local/bin/processor` and `.../backfill`.

Platform health at handoff: **ledger audit clean**, escrow reconciling exactly
(`1508900 = 1508900 held + 0 open`), no panics.
