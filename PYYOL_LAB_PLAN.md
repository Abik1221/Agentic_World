# Pyyol Lab — build plan

> Turning Pyyol from a staked arena that *has* a leaderboard into a **measurement instrument**
> with an absolute scale, and splitting the research surface cleanly from the money surface.
>
> Status: plan of record. Every claim below is anchored to a finding in the audit
> (`file:line` cited inline). Nothing here is aspirational without being marked as such.

---

## VERDICT AFTER EXTERNAL AUDIT (2026-08-20) — READ FIRST

An independent audit reimplemented this system in Python and measured it. Summary:

- **The statistics are correct.** The bound, the n-1 variance, the delta/2 union split, the
  alpha-spend summing to delta, the off-grid peeking prevention, and the v=0 symmetry argument
  all verified. One mis-citation: the bound is Audibert-Munos-Szepesvari, not Maurer-Pontil.
- **The estimand is genuinely unoccupied.** Nobody has published an exploitability measurement
  of an LLM's behavioural policy with a finite-sample guarantee. Closest: Equilibrium Residuals
  (2605.10410, matrices not extensive-form, non-frontier models) and GENSTRAT (2605.23238,
  abstracted CFR+ solver, disclaimed as not-Nash).
- **The instrument is NOT ready to rank models.** Measured Kendall tau = 0.49 between certified
  bound and true exploitability at DefaultSpec; 25% of pairs inverted.
- **"Cannot be farmed" was FALSE — and is now FIXED.** A dict that memorised the prober drove
  the certified bound to 0.000. Board randomisation + a bootstrap prober mixture together
  remove 97% of the attack's advantage and it no longer zeroes the bound. The original false
  claim stays retracted in the package docs, with the measurements.

**Do not publish rankings from this yet.** Required first, in order:

1. Randomise the prize order per match over all N! permutations (kills the memorisation attack
   AND the 566-node lookup-table critique; the estimand improves to expected exploitability).
2. Ship a certified UPPER bound. With ~566 nodes, simultaneous multinomial confidence sets over
   sigma plus DP gives `eps in [lo, hi]`, which DOES order agents. **Nobody has a two-sided
   certified exploitability interval for any agent, LLM or otherwise** — this is a stronger
   paper than the current one.
3. Randomise the prober (mixed BR / fresh tie-breaking) and enforce Phase B statelessness.
4. Phase A >= 1000 matches, and stop stopping on "LB > 0" — that rule publishes the WEAKEST
   bound the schedule could certify, which is pessimal for ranking. tau goes 0.49 -> 0.83-0.92.
5. Replace max(EB, Hoeffding) with a one-sided Waudby-Smith-Ramdas betting confidence sequence:
   informative at n=128 where the current bound is vacuous (~4x fewer matches), natively
   time-uniform, and it deletes the alpha-spending grid entirely.
6. Publish reference points: eps(random), eps(always-lowest), eps(equilibrium)=0, eps(a 50-line
   solver). Without them the number is uninterpretable.
7. Put the model in the certificate: provider, dated snapshot, temperature, prompt hash,
   scaffold hash, tokens, dollars. For a benchmark premised on "hold scaffold constant, vary
   model", the scaffold is not in the hash and the model is not in the certificate.
8. Null-boundary coverage simulation under the ACTUAL stopping rule, >=1000 replications.
9. Second game (Kuhn or Leduc, both solved, both in OpenSpiel) turns "one game" into "a method".

Terminology fix: this is **group-sequential with geometric alpha-spending** (Lan-DeMets), not
"anytime-valid". Coverage holds on the grid {n0*2^k}, not for all t.

---

## Build status (updated as work lands)

| Item | Package | State |
|---|---|---|
| Exact Goofspiel solver | `internal/gops` | **built**, mutation-verified |
| Certified exploitability (CELB) | `internal/exploit` | **built**, Monte-Carlo validated |
| Stored-view bridge + census | `internal/exploit/view.go` | **built** |
| Chess-clock time control | `internal/timecontrol` | **built**, mutation-verified — Lab-track only; the 4x exploit it was meant to fix does not exist (see 0.2) |
| Three-way seed reconciliation | `internal/harnessseed/reconcile.go` | **built and WIRED** — the embedded export now fails it and will not seed |
| Arena-normalised ranking | `internal/arenanorm` | **built**, mutation-verified |
| Model board ordering | `internal/rating` (`orderModels`) | **built and WIRED** — no longer ordered by latency |
| `ratable()` — integrity voids ratings | `internal/integrity` (`FilterRatable`) | **built and WIRED** — Goofspiel path; Mafia/Monopoly still pass an empty verdict |
| Seat mirroring | — | not started |
| Sequential certification (cost) | `internal/exploit/sequential.go` | **built** — 9.2x fewer matches than a fixed sample, coverage verified under adversarial stopping |
| Ladder spec + run state machine | `internal/ladder` | **built** — spec hashing, prober digest, phase transitions |
| Ladder schema | `migrations/0097` | **built** — applied, reversed and re-applied against real Postgres; every constraint exercised |
| **LAYER 2: verifiable attestation bundle** | `internal/attest` | **built** — Ed25519-signed, hash-chained, and verification is RECOMPUTATION not trust. 12 tamper cases caught |
| Two-sided interval on the certificate | `internal/ladder` (`Finish`) | **built and WIRED** — the certificate now publishes both halves plus Separable |
| Kuhn poker solver (validation oracle) | `internal/kuhn` | **built** — NOT a playable game and not being added as one. Value -1/18 verified; equilibrium exploitability 0 both seats; never-bluffing costs exactly 1/9. Used to prove the estimator works on private-information, chance, asymmetric games |
| Precision-based stopping | `internal/exploit/sequential.go` | **built and WIRED** — replaces stop-on-significance; Phase A raised to 1600 |
| Randomised prize orders (multi-board) | `internal/exploit/orders.go` | **built and WIRED** — 8 boards; attack 97% closed; migration + live repo verified on Postgres |
| Mixed prober (bootstrap BR mixture) | `internal/exploit/mixed.go` | **built and WIRED** — 1.4% tightness cost, 42% of the attack removed, residual documented |
| **Certified UPPER bound (two-sided interval)** | `internal/exploit/upper.go` | **built** — BHC simultaneous confidence region + greedy L1 reallocation folded into the same DP. Coverage verified; certified ordering makes ZERO wrong calls |
| Certification runner | `internal/ladder/service.go` | **built** — end-to-end test produces a valid certificate against a known-exploitability agent |
| SQL repo | `internal/store/ladder_repo.go` | **built** — exercised against real Postgres; caught a publish bug |
| Arena Driver (real matches) | — | **not started** — the only remaining gap; the Driver port is defined, nothing implements it against the live engine |
| Ed25519 receipts, hash chains, spec pinning | — | not started |

Full backend build green; `go test ./internal/... ./cmd/...` green across all packages.

Three live behaviour changes so far: the harness export no longer seeds, the public model board
is no longer ordered by latency, and a Goofspiel match voided for integrity no longer moves a
rating.

---

## 0. The thesis

Every competitor measures **win rate against a pool of other LLMs** (GTBench, GAMA-Bench,
MAgIC, TMGBench, LLMsPark). That number is pool-relative: it moves when the pool changes, it
can be farmed by choosing opponents, it saturates, and it has no absolute meaning.

Pyyol can publish something none of them can: **certified distance from optimal play**, on a
game where optimal play is exactly computable, produced by an agent whose model calls are
cryptographically bound to its moves.

Two things make this ours specifically:

1. **Goofspiel.** Standard finite n-card GOPS — zero-sum, perfect recall, one integer action
   per turn. `Config.Cards` is a config field (`engine/goofspiel/state.go:57-63`), so a
   solver-tractable small-n variant is one line, not a rewrite. `input_json`
   (`migrations/0074`) already stores the exact turn view including `legal_actions`, and
   `skill/goofspiel_view.go:49-114` already reconstructs opponent hand and remaining prizes
   **exactly, not estimated**. Per-infoset empirical action distributions are already in the DB.
2. **Completion binding.** `HMAC(secret, agent|match|round|completion_hash|extracted_move)`
   (`turnproof/turnproof.go:36`) proves an LLM call was made *for this decision* and the
   submitted move equals the move inside it.

Nobody else has either. Together they are the moat.

---

## 1. The two tracks

The audit found the separation already half-built and undocumented as a product surface.
`kind='harness'` (migration `0094`) is defined as: LLM-backed, certified like a real agent,
**unrated, zero-stake, excluded from every user-facing board**. That is precisely the research
track. It is currently used only to *hide* platform data, never to *publish* it on its own terms.

We formalise it into two first-class tracks.

| | **Pyyol Lab** (research) | **Pyyol Arena** (developer) |
|---|---|---|
| Purpose | LLM/agent benchmarking, publishable science | competition for real coins |
| Stakes | **always zero** | staked, escrowed, raked |
| Agent kind | `harness` + a new `lab` kind for external researchers | `external` |
| Matches | `rated=false`, `lab_rated=true` | `rated=true` |
| Opponents | reference bots, solvers, **best-response probers**, other lab entrants | other developers, house bots |
| Reproducibility | seed + commit + replay hash **published**, transcripts exportable | seed revealed at finish; transcripts owner-scoped |
| Metrics | certified exploitability, regret, calibration, attribution | Elo/Glicko ladder, P-Index, payouts |
| Config | **pinned and hashed** per run; results carry `spec_version` | live platform config |
| Anti-fraud | not applicable (no money) | collusion, timing, integrity holds |
| SDK | `pyyol.lab` namespace, no wallet calls | full SDK |
| Admin | Lab section: runs, specs, exports, exclusion census | existing operations console |

**Why the split is load-bearing, not cosmetic.** Three of the worst audit findings dissolve
under it:

- The seeded lab export (`harnessseed/data/lab-2026-08.json`) attributes 642 decisions to
  `claude-opus-4`/`gpt-5.2`/`gemini-3-flash` while its own `agent_model_calls` evidence table
  contains **zero** frontier calls — only `groq/llama-3.1-8b-instant` (323) and
  `openrouter/gemma-4-26b` (175). Verified by direct parse. With a real Lab track, lab data
  lives on a lab board with lab provenance and never needs to be hidden.
- Research needs full transcripts; the Arena needs owner-scoped privacy
  (`migrations/0073:20-24` — a turn view contains the seat's hidden information). One rule
  cannot serve both. Two tracks, two retention/declassification policies.
- Research needs pinned config; the Arena needs live config. Today `MinCoverage`,
  `MinComparisons`, the L2 penalties, the `pairwiseGames` set and the runtime
  `publishableHosts` allowlist are **absent from `model_board_history`**, so a three-month
  diff silently compares two different methods.

---

## 2. Phase 0 — credibility triage (do first, nothing else ships before it)

These are the findings that would embarrass us in front of a lab. All are small.

### 0.1 Three-way reconciliation as a boot assertion
`harnessseed` writes proof-shaped rows (`bound`, `bind_receipt`, `completion_hash`,
`verified_cost`) into the exact tables the model board's verification gate reads
(`harnessseed.go:322-347, 396-411`), unconditionally at boot (`cmd/server/main.go:1830`).
The embedded file currently contains:

- 574/574 `model_calls` marked `bound: true` — including **67 non-2xx** (30×400, 25×429,
  12×502) and **131 with zero tokens**
- 262/642 decisions with an **empty scaffold** (not comparable by our own rule, `0081:11-14`)
- **0/642** decisions with `input_json`; **0/37** matches with a seed or replay hash

Coverage (`bound ÷ decisions`) is the platform's headline verification metric and is inflated
by construction over this data. This is exactly the failure `pyyolbench/ledger.py:9-18` was
written to catch.

**Build:** `internal/harnessseed/reconcile.go` — verify, before any INSERT, that
(a) every `bound` call is 2xx **and** carries non-zero tokens, (b) each decision's declared
provider/model agrees with a `model_call` for the same (match, agent), (c) declared-vs-verified
model strings agree. On mismatch: refuse to seed, log the census, exit non-zero in strict mode.
*A single source of truth cannot be checked against itself.*

### 0.2 A declared time control — DOWNGRADED from P0 after verification

**The audit's 4x claim was wrong, and I verified it before building on it.** It reported that
`deadline.For` = `max(base, p95(own latency) x 1.5)` let a padder earn four times its
opponent's wall-clock in the same match. `deadline.For` is per-agent, which is what the audit
saw — but every caller maxes it across the seats first:

- Goofspiel (`match/service.go:341-359`) takes the LONGEST window over unsealed seats; the
  round deadline is shared.
- Monopoly (`monopoly/service.go:85-97`) does the same across the table, and states the
  reason: *"cutting an agent off because of who it was seated with is the one thing a deadline
  must never depend on."*
- Mafia never uses adaptive windows — `phaseWindow` is a per-phase engine constant
  (`mafia/service.go:889-895`).

There is no within-match asymmetry. This is **not** a live exploit and does not belong in
credibility triage.

**What is still genuinely wrong**, and why `internal/timecontrol` was still worth building:

1. **Between-match variance.** The window is the maximum over the seats present, so an agent
   drawn against a slow opponent gets more thinking time than the same agent drawn against a
   fast one. Fair within a match; a confound across a benchmark.
2. **Time is not a declared parameter.** A reproducible benchmark publishes its time control
   the way a chess tournament does. "However long the slowest agent in your pairing needed" is
   not a specification, cannot go in a spec hash, and cannot be reproduced by a third party.

**Revised plan:** the chess clock ships on the **Lab track only**, as part of the pinned spec,
where a declared equal budget is a requirement of the method. The Arena keeps adaptive
windows, which are the right shape for a product where the goal is that honest slow agents are
not cut off. Reclassified from Phase 0 to Phase 1.

### 0.3 Stop ranking by latency
`rating.go:408-412` sorts the public model board primarily on `Intelligence` =
`0.4·legal + 0.4·(1−fallback) + 0.2·speed` — a quantity containing **no information about
winning**. Two clean agents both score `800 + 200·speed`, so order is decided entirely by
latency; and the constants are 500/8000ms while migration `0078`'s own measurement records
**p50 = 6,988ms**. The median honest agent scores 0.135.

**Build:** rank by the `modelboard` lower bound. Keep `Intelligence` as a displayed column,
never a sort key. Re-derive its latency band from measured data or drop the speed term.

### 0.4 Per-arena win rates, never pooled
`ModelStat.WinRate = Wins/(Wins+Losses)` pools across arenas (`rating.go:620-623`), and
`BuildDeveloperEdges` keys baselines on `provider+"/"+model` with **no game dimension**
(`edge.go:134-142`). Goofspiel is 1-of-2; Monopoly is 1-of-n. A developer who queues only 1v1
gets a **+27-point "skill above model" edge** that is entirely arena mix. One-line exploit.

**Build:** win rate is per-(model, game) or it is not published. Aggregate only with explicit
per-arena weights that are part of the pinned spec.

### 0.5 Integrity must void ratings, not only money
`match/service.go:1488-1497` refunds a match whose seat proved not one LLM-backed decision —
then rates it anyway at `:1551-1558`. The leaderboard query has no `fraud_flags` join and no
`m.rated` filter (`store/rating_repo.go:260-280`). `RANKED_INTEGRITY_MIN_PCT = 0`
(`config/config.go:592`), and the code's own measurement records **3,863 of 3,895 seats proved
nothing** over 48h of staked ranked play (`cmd/server/main.go:1400-1403`).

A scripted non-LLM agent is refunded 100% of the time and climbs the ladder for free.

**Build:** one predicate — `ratable(match, seat)` — used by the rater, the boards and the
payout path alike. If money is voided, rating is voided.

### 0.6 Seat mirroring
Grep for `mirror|duplicate|counterbalance|swap seat` across `match`, `matchmaking`,
`groupmatch` returns **zero hits**. `CreatePaired` hard-assigns `SeatA` = the older queue entry
(`match/service.go:816-817`); Monopoly seat 0 always rolls first. The same deal is never played
by both sides.

**Build:** every Lab pairing plays twice on the same seed with seats swapped. Duplicate-bridge
scoring on Goofspiel separates deal luck from play quality entirely — the single cheapest
variance reduction available to us.

---

## 3. Phase 1 — the instrument (the moat)

### 3.1 `internal/gops` — an exact solver
Sequence-form / DP solver for small-n Goofspiel (n = 4,5,6), giving the exact game value `v`
and an exact best response to any fixed behavioural strategy. Deterministic, no RNG, no clock,
versioned like `internal/skill` (`scorer_version` precedent, `migrations/0077`).

### 3.2 `internal/exploit` — Certified Exploitability Lower Bound (CELB)

For a 2-player zero-sum game with value `v`, exploitability is
`ε(σ) = max_{σ'} u(σ', σ) − v`. Zero means unexploitable. It is **absolute** and
**opponent-pool independent**.

The naive estimator is wrong: a best response fitted to a noisy policy estimate overfits the
noise and *overstates* exploitability. The protocol that fixes it:

```
Phase A (fit)      entrant plays N1 matches vs a fixed reference mixture
                   estimate σ̂ per infoset (Dirichlet-smoothed)
Phase B (certify)  b = BR(σ̂), computed from Phase A ONLY
                   deploy b as a LIVE opponent for N2 matches
                   measure mean payoff ū_b
Report             ε̂_LB = ū_b − v, one-sided empirical-Bernstein bound
```

**Proposition.** `ε̂_LB` is a valid lower bound on true exploitability.

*Proof.* `b` is measurable w.r.t. Phase A, hence independent of Phase B, so `E[ū_b] =
u(b, σ_A)`. Exploitability is a maximum over all deviations, so `u(b, σ_A) − v ≤
max_{σ'} u(σ', σ_A) − v = ε(σ_A)`. ∎

Headline: **"this agent is *at least* this exploitable, 95% confidence"** — conservative in the
only direction that matters.

Running `b` **live** in Phase B is what keeps it clean. Evaluating a best response against
*logged* play needs off-policy correction, because `b` steers the game into infosets the logs
never visited. Ship the logged estimator as a cheap continuous monitor and the live probe as
the certified path; state the difference publicly.

**Why it cannot be gamed.** The only way to lower certified exploitability is to play closer to
equilibrium. There is no opponent to farm — the prober is computed against *you*. There is no
pool to manipulate — the pool is not in the formula. There is no prompt trick — a best response
finds whatever hole the policy has, wherever it is. Every competing benchmark can be gamed by
choosing whom you play. This one cannot, by construction.

**Why labs care beyond the report card.** Exploitability is exactly what self-play RL minimises.
It is a *training signal*, not just a score.

### 3.3 Ranking that admits its own limits
- Promote `internal/modelboard` to the centrepiece. It is already research-grade: Davidson-tied
  Bradley–Terry with a (developer, scaffold) fixed effect, per-match weight normalisation,
  **match-clustered** bootstrap, percentile intervals, ranked on the lower bound, published
  `RankStability` and `Separability`, splitmix64 for cross-Go-version reproducibility.
- **Add the cyclic residual.** Hodge-decompose the matchup matrix and publish the fraction a
  scalar rating *cannot* explain. Publishing our own limit is a credibility weapon.
- **Add Nash averaging** — provably invariant to redundant/clone entrants, which pre-empts the
  pool-composition critique that damaged LMArena.
  (Balduzzi et al., *Re-evaluating Evaluation*, NeurIPS 2018, arXiv:1806.02643)
- **N-player: stop using Elo.** `modelboard/build.go:70-86` already excludes Mafia with the
  correct reason in the comment — but the ladder still rates it role-blind
  (`mafia/service.go:1005-1013` collapses 3-mafia/9-town to placement 1/2; `PlayerResult` has no
  role field). Adopt equilibrium-based N-player rating
  (Marris, Lanctot, Gemp, Omidshafiei et al., arXiv:2210.02205) plus **Shapley** credit via
  counterfactual replay — axiomatically the unique fair attribution.
- Wire `internal/skill/ranking.go` (empirical-Bayes shrinkage, credible lower bound) to
  something. It is written, tested, and currently used by nothing.

### 3.5 Certification runner — design of record (not yet built)

Three decisions drive the cost, and all three are settled:

1. **Store the prober's DIGEST, not the prober.** It is a deterministic function of
   (spec, Phase A counts) and the solve is milliseconds. Storing a digest means a third party
   can recompute and verify it, and any solver change invalidates the certificate
   automatically instead of silently altering it. Reproducibility falls out for free.
2. **Aggregate Phase A counts, do not keep raw observations.** The fit/certify split is BY
   PHASE and declared in advance, not random, so aggregation loses nothing. Storage drops from
   O(decisions) to O(nodes) — bounded by the spec however many matches run.
3. **Sequential stopping** (built, `sequential.go`): 9.2x fewer Phase B matches than a fixed
   sample, which is a direct saving in the agent's inference spend.

Schema:

```sql
lab_ladder_spec(version PK, n, prize_order INT[], tie_rule, alpha, delta,
                phase_a_matches, phase_b_first_checkpoint, phase_b_max,
                budget_ms, per_move_ms, increment_ms, grace_ms,
                spec_hash TEXT NOT NULL, active BOOL)   -- immutable once referenced

lab_cert_run(id PK, agent_id, spec_version, phase, phase_a_done, phase_b_done,
             prober_digest TEXT)                        -- pinned at the A->B transition

lab_fit_counts(run_id, node_me, node_opp, node_carry, card, n,
               PRIMARY KEY (run_id, node_me, node_opp, node_carry, card))

lab_certify_payoff(run_id, match_id, seq, payoff,
                   PRIMARY KEY (run_id, match_id))      -- idempotent on re-drive

lab_certificate(run_id PK, spec_version, games, mean, stddev, lower_bound, delta,
                informative, stop_reason, prober_digest, inputs_hash, computed_at)
```

`spec_hash` follows the `pindex_config` pattern (canonical JSON, SHA-256) and is **immutable
once referenced** — the audit found `PutConfig` upserting published params in place, so "v3"
stopped meaning what it meant when agents were scored under it. That must not be repeated here.

### 3.4 Model-vs-scaffold attribution — the thing labs will pay for
Nobody can currently say whether an agentic system performs well because of the model or the
harness. We can: the gateway sees the calls and `agent_match_decisions.scaffold` is already
recorded per decision (`migrations/0081`).

```
g(E[y]) = μ + α_model + β_scaffold + γ_{model×scaffold} + seat + opponent + match
```

Crossed random-effects Bradley–Terry. The deliverable is
`τ²_α / (τ²_α + τ²_β + τ²_γ)` — **the share of outcome variance attributable to the model**,
with a standard error.

**Hard requirement:** α and β are identifiable only if the design is *crossed*. Build a **house
scaffold ladder** — a fixed set of reference scaffolds (naive / CoT / ToM-prompted /
tool-augmented) any model can be dropped into. Without deliberate crossing this is
unidentifiable no matter how much data accumulates. Design decision, not an analysis choice.

---

## 4. Phase 2 — make it externally checkable

What the trust chain proves today: *an LLM call was made for this decision, and the submitted
move equals the move inside it.* That is real and rare. It does **not** prove which model
answered — `llmgw.go:188-196` records `Provider`/`Model` as what the developer *asked for*;
only `UpstreamHost` records who answered. `manifest/schema.go:65-72` says so outright.

1. ~~**Ed25519 instead of HMAC**~~ **DONE for CERTIFICATES — `internal/attest`.** A signed,
   hash-chained bundle carrying the spec, the fit counts, the payoffs and the certificate, so a
   stranger recomputes rather than trusts. Bind receipts themselves are still HMAC; that is the
   remaining half of this item.
2. ~~**Hash-chain**~~ **DONE for the certificate series** (`attest.VerifyChain`): dropping or
   reordering a published certificate breaks every later link. Still open for the platform
   event log: `store/platform_events.go:55-61` signs each message with no
   prev-hash and no sequence number — delete, reorder or withhold events and every remaining
   signature still verifies. That is not a chain.
3. **Hash-chain the audit log.** `Super_Admin/server/internal/audit/audit.go` is 46 lines over a
   plain mutable table: no signature, no trigger, no `REVOKE`; the app connects as DB **owner**;
   `cmd/seed/main.go:44-56` ships a `TRUNCATE`; write failures are swallowed (`audit.go:37-39`)
   so a mutation can succeed unaudited.
4. **Commit to completion bytes** (Merkle commitment + selective disclosure) so a dispute is
   settleable without trusting the audited party.
5. **Pin the spec.** Copy the `pindex_config` pattern (`version`, `params JSONB`, per-row
   `config_version` + `inputs_hash`, `cmd/pindex-recompute` diffing) onto the model board and
   every Lab run. Fix `PutConfig`, which upserts params in place
   (`store/pindex_repo.go:808-814`) so "v3" stops meaning what it meant when people were scored
   under it.
6. **Publish the exclusion *rate*.** Today only numerators exist; the response carries no
   denominator, so a reader cannot compute a rate. The agent ladder publishes no census at all.

Then the honest public claim — *"Pyyol proves an LLM decided this move, and which upstream
served it"* — is still more than any benchmark offers, and survives a hostile reviewer.

---

## 5. Phase 3 — surfaces

**SDK (`pyyol.lab`).** No wallet calls. Run a spec, get a signed result bundle: seeds, replay
hashes, per-decision records, config hash, exclusion census, intervals.

**Admin (Lab section).** Specs and their versions; runs with pinned config hashes; the exclusion
census with denominators; per-agent coverage (bound ÷ decisions); export/declassification. The
audit found the console currently shows **zero** verification, coverage or exclusion information
— an operator can suspend an agent, certify an agent and activate a new scoring config without
ever seeing whether the affected decisions were proof-bound.

**Public.** A leaderboard whose headline is `ε̂_LB` with intervals, alongside the cyclic residual,
the exclusion rate and the coverage distribution.

---

## 6. Sequencing

| Phase | Ships | Gate |
|---|---|---|
| 0 | reconciliation assert, time control, sort fix, per-arena rates, `ratable()`, mirroring | nothing else ships first |
| 1 | `internal/gops`, `internal/exploit`, cyclic residual, Nash averaging, N-player rating | Phase 0 green |
| 2 | Ed25519 receipts, hash chains, spec pinning, exclusion rates | Phase 1 producing numbers |
| 3 | `pyyol.lab` SDK, Admin Lab section, public instrument board | Phase 2 verifiable |

Variance control is a prerequisite for every published number: the team's own target is
**30–50 matches per pairing** (`harness/bench/plan.py:89-92`); the seeded export contains 34
Goofspiel + 3 Mafia + 0 Monopoly matches total, and one standing is 3–0, which happens 1 time
in 8 by chance.

---

## 7. What we are NOT claiming

Honesty about limits is the product.

- We do not prove model identity. We prove an LLM decided the move and which upstream served it.
- Exploitability is certified for **Goofspiel** (and any future 2p0s game we solve). Mafia and
  Monopoly get regret, calibration and Shapley credit — not an equilibrium distance.
- Monopoly decision data is structurally lossy (256-decision cap vs ~400 decisions/seat;
  512 KiB seat input budget), so it is **not** usable for infoset analysis until that is fixed.
- Prompt-side influence is strategy, not fraud, and we do not try to detect it.
