# Pyyol — finish the integrity pipeline

Read `Agentic_World/CLAUDE.md` first (invariants, Docker build commands, provider rules) and
`Agentic_World/HANDOFF-SESSION.md` (what landed, with evidence, plus every open item and why).
This prompt is the ORDER OF WORK. Do not reorder it — later items depend on earlier decisions.

Repo root: `/Users/macbookair/pyyol`, arena at `Agentic_World/backend`, Lens at `Agentic_World/tracing`.

---

## PHASE 0 — confirm the platform is healthy, and confirm the last session's work survived

Read the last ~15 commits, then verify:

- ledger audit clean; `escrow_balance == held_for_review + open_matches`
- mafia matches COMPLETING (not just starting); queue orphans == 0
- no house-created match has `bid > 0`
- **completion binding still active** — the boot log should say
  `completion binding active: a submitted move that contradicts the model's own output is rejected`
- **`rollup_hourly` filtered to `meter_source='gateway'` equals `events_raw` filtered the same way**
- **a fully-bound honest seat reports 100% coverage**, not 76–93%

The last three are new and are the things most likely to have regressed. If any is false, fix that
first.

**⚠️ NOTHING FROM THE LAST SESSION IS COMMITTED.** ~43 changed/new paths in the `Agentic_World`
working tree, spanning the arena backend, both SDKs, shared conformance fixtures and the Lens
backend. Review `git status` and `git diff` before touching anything, and commit in coherent
chunks (completion binding / deadline fix / provider generality / Lens fixes) so a bisect means
something later.

---

## 1 — Phase 3 implementation: staked but unranked   (DECIDED, one product call left)

The policy is settled: an agent that never routes through the gateway **may play staked and win
coins, but is excluded from the ranked surfaces.** The incentive to verify is reputational, not
financial.

Half of it is already true and half is not:

| surface | excludes unverified? |
|---|---|
| Model board (`internal/modelboard/build.go`, `no_verified_model`) | YES — correct |
| Arena skill ratings (`ratings` table) | **NO — 69 of 87 rated agents are unverified** |

**Do not simply filter unverified agents out of rating.** TrueSkill/Elo quality depends on a
connected comparison graph; removing ~79% of the rated population degrades the ratings of the
VERIFIED agents too — the same separability concern the model board already publishes.

Recommended: **keep computing their ratings, stop PUBLISHING them.** The graph stays intact, and
the public ladder shows only verified agents. Alternatives, if you disagree: a separate labelled
"unverified" tier, or exclusion from rating entirely (worst statistically, and it makes an agent's
first verified match its first rated one).

ASK which of the three before implementing — it depends what the ladder is FOR.

**DONE WHEN:** an unverified agent can win coins on a staked table and does not appear on the
published ladder, AND a verified agent's rating is demonstrably unchanged by the filter (compare
before/after on the same match set — this is the whole reason not to exclude them from rating).

---

## 2 — Range bindings: make coverage mean "decisions a model made"   (unblocks 3 and 4)

**This is the highest-value item and the reason the ranked threshold is still 0.**

Measured last session with a purpose-built honest population (`gamelab -bind-fail-pct`,
`-bind-batch` — deterministic on (match, round, seat), so runs repeat):

| archetype | behaviour | bound / rounds | coverage |
|---|---|---|---|
| perfect | binds every turn | 6/6 | 100% |
| flaky | 15% of calls fail, plays on | 5/7–7/7 | 71–100% |
| batcher | one call plans 3 rounds | 3/9–4/9 | **33–44%** |

The honest floor is set by **batching**, and batching is exactly what Phase 4 exists to REWARD
("cost optimisation must be rewarded, never penalised"). So the share rule punishes the behaviour
the cost board rewards, and **no threshold reconciles them**: above ~33% voids honest batchers,
below ~33% lets a cheat binding 1 round in 3 through.

The metric is wrong, not the number. Fix: **let one binding cover a RANGE of rounds.** If a single
completion legitimately decides rounds 4–6, those three rounds ARE model-backed and must each
count.

Sketch (verify against the code before committing to it):
- the SDK declares the span a completion covers, and the move tool carries a move per round
- the gateway binds each round in the span from that one completion — shared `completion_hash`,
  one receipt per round, each receipt over that round's own extracted move
- match-time enforcement is unchanged: each round still compares its submitted move to its own
  bound move

Keep the existing safety rules absolutely intact: **absence never rejects**, fails open on a read
error, and binding only ever happens on a PROVEN call (on an unbound call the match/round are
unverified headers, so recording a move against them would let an agent write a move into any turn
— the control would become the cheat).

**DONE WHEN:** a batching agent that plans 3 rounds per call scores ~100% coverage, not ~33%; a
substituted move inside a batched span is still rejected; and the conformance fixtures cover the
range shape in all three languages.

---

## 3 — Phase 2: set `RANKED_INTEGRITY_MIN_PCT` from the corrected distribution

Only after (2). Re-run the mixed population, read the honest floor now that batching no longer
depresses it, and set the threshold below it with margin.

```bash
/src/.lab-gamelab -game goofspiel -tier low -bind                    # perfect
/src/.lab-gamelab -game goofspiel -tier low -bind -bind-fail-pct 15  # flaky provider
/src/.lab-gamelab -game goofspiel -tier low -bind -bind-batch 3      # batching
```

Note the two denominators are DIFFERENT and both are correct: the share rule derives its own from
the engine's round count (`match.rankedIntegrityFailed`), while `CoverageFor` resolves the proof
slot per game for the developer-facing figure. Do not "unify" them without understanding why.

**DONE WHEN:** the threshold is set from an observed distribution you can show, and no honest match
is voided by it over a sustained run.

---

## 4 — Phase 4: reward cost skill instead of punishing it

Now unblocked by (2).

- A cache HIT is a real API call with usage metadata, billed ~10%. Caching never hides a call.
- Add to P-Index: cache-hit ratio, cost per decision, cost per win, tokens per decision.
- Rank on **QUALITY PER COST**, never cost alone — cheapest is trivially won by the weakest model
  answering badly. Floor the sample size and publish an interval, exactly as the model board ranks
  by lower bound.
- Read `meter_source='gateway'` only. The rollups now separate meters (migration 006); a query that
  omits the filter blends an unfakeable measurement with the agent's own claim, and the two
  genuinely disagree.
- ~112k `verification_pending: high_human_likelihood` are the TIMING detector correctly flagging
  deterministic lab agents. Consider making a cryptographic proof OUTRANK a statistical timing
  guess when both are present.

---

## 5 — Lens hardening (both found last session, neither fixed)

- **`span_id` is never set: 0 of 57,301 events carry one.** So `spans` is dead weight for arena
  traces and any UI reading it shows nothing. Decide: the emitter should set `span_id`, or `spans`
  should be dropped from the arena's projection set. Its broken column list went unnoticed for
  exactly this reason.
- **Every Lens projection except `spans` is still a positional INSERT.** Each is one migration away
  from the same `21-into-38` failure that broke the whole backfill. Give them explicit column
  lists.

---

## 6 — Consider: a rejected move should earn no deadline extension

Bounded now (3/round after migration 0085), but `tryExtend` extends because the seat is unsealed
and the endpoint answers `/health` — and an agent that SUBMITTED and was REFUSED is demonstrably
not still thinking. Would cut a cheating seat's forfeit from ~2 min/round to one window. This is a
behaviour change, not a bug fix; decide deliberately.

---

## 7 — Groq end-to-end run (user-requested)

One real coin-based game through Groq with prompt caching on, verifying traces and accuracy end to
end. Ask the user for the key; it is rate-limited, so use caching and keep the run small.

Groq is OpenAI-wire, so its cache field would be `prompt_tokens_details.cached_tokens` — a SUBSET
of `prompt_tokens`, handled by the prompt-family rule in `usagenorm.go`. **Confirm what Groq
actually returns rather than assuming**, and add a conformance fixture for the real shape.

Verify: the move is bound, cost is non-zero and correct, cache tokens appear, `meter_source=gateway`,
and the span reaches ClickHouse with `extracted_move`. Remind the user to rotate the key afterwards.

---

## 8 — Phase 5 backlog, then Phase 6 SDK publish LAST

- 107 suppressed frontend lint errors; JS SDK 68 warnings (0 errors)
- full `-race` suite exceeds 10 min
- 38 `matchmaker pairing failed` from `high_human_likelihood` — recheck after (4)

Publish only when everything above is green: version bumps + CHANGELOGs for python and js, confirm
CI drops Python 3.9 (EOL Oct 2025, `requires-python >=3.10`), conformance fixtures verified in both
languages, full test/lint/build green. **The SDK contract changed in Phase 1 and changes again in
(2), so publishing earlier forces an immediate breaking release.**

---

## Do not break these — each was hard-won, with tests pinning it

- **The house never stakes.** `internal/bot` `houseStake() == 0`. Guards: `TestHouseStakeIsZero`,
  `TestNoHardcodedStakeReachesAHouseTable`.
- **Certification asymmetry.** A USER's agent certifies on every table. Only platform bots are
  exempt, only at zero fee, only via an EXPLICIT id list built at boot — never a slug or framework
  pattern.
- **Never cut an exemption into a fraud control.**
- **Controls live on the service that ESCROWS, not the handler.** The stake floor was bypassed by
  the bot runner for exactly this reason; completion binding sits in `tryAct` for the same reason.
  When a check exists but the bug survives, **suspect placement.**
- **Absence never rejects** (completion binding), and it **fails open** on a read error.
- **Public route surface is pinned** (`internal/httpx/public_routes_test.go`).
- **Ledger audit + escrow reconciliation run on workers. Never auto-"repair" a ledger.**
- **Deception index:** never parse message text; role-conditional; publish the chance baseline and a
  Wilson interval; a seat with no votes is UNSCORED, not 0%.
- **Classify providers by wire format and key MEANING, never by vendor.** An unreadable shape must
  be LOUD, never silently costed at zero.
- **`sdk/conformance/*.json` is shared by Go, Python AND JS.** Change extraction or normalization in
  one language, change all three and add a fixture.

## How to work — this is what actually found the bugs

- **Deploy and watch the database.** A green build is not evidence. Every real bug last session was
  found in the DB, not the test suite.
- **A test that passes for the wrong reason is worse than none.** Prove a new guard would FAIL —
  mutate the source temporarily and watch it go red.
- **Verify your own claims before reporting them.** Two claims were retracted last session: a
  "processor writes no rollups" defect that was really consumer lag from a container I had just
  restarted, and an "already the case" that was only half true. Both were caught by checking, not by
  thinking harder.
- **Check the pipeline before diagnosing the code.** For Lens, compare consumer `delivered`/
  `ack_floor` against stream `last_seq` first:
  `docker exec pyyol-lens-nats sh -c "wget -qO- 'http://127.0.0.1:8222/jsz?consumers=true&streams=true'"`
- **When one fix retires several problems, that is the right shape.** If you are tuning a third
  constant, the shape is probably wrong.
- **State what you did NOT verify.** Honest partial results beat clean-sounding summaries.

## Environment

Arena on the `pyyol-lab` network: `pyyol-pg` (`psql -U pyyol -d pyyol_lab`), `pyyol-redis`,
`pyyol-backend` (source-mounted, runs the prebuilt `/src/.lab-server`; also joined to
`tracing_default` so it can reach `ingest-api`), `pyyol-toolprovider` (stand-in Anthropic endpoint
returning a `play_card` tool call, card from an `X-Lab-Card` header, plus an SSE variant).

Lens: `pyyol-lens-{clickhouse,postgres,nats,minio,ingest,processor}`. `query-api` is stopped — it
requires `QUERY_API_KEY` and that guard should not be weakened for a test.

The gateway needs `TURN_PROOF_SECRET` and `PYYOL_LLM_GATEWAY_ENABLED=true` or it records calls and
can prove none. Uncommitted files outside `backend/`: `tracing/docker-compose.override.yml` (drops
only conflicting host ports) and `tracing/.env`. Swapped binaries live in the processor container
(`/usr/local/bin/processor`, `.../backfill`) — rebuild the image properly before relying on them.
