# Where the game-theoretic LLM benchmark field actually is — August 2026

Six research agents, ~500k tokens, arXiv API + OpenReview + web. This is the honest map:
what is taken, what is mathematically impossible for us, and the three things left.

Read the caveats at the bottom before betting on any of it.

---

## 1. Taken. Do not build here.

| concept | owner | reference | date |
|---|---|---|---|
| Equilibrium **rating** on a live leaderboard | **Google DeepMind** | `polarix`, Kaggle Game Arena Werewolf | Feb 2026, in production |
| CCE as a **ranking aggregation rule** | **DeepMind** (Marris, Liu, Gemp, Piliouras, Lanctot) | Deviation Ratings, [2502.11645](https://arxiv.org/abs/2502.11645) | Feb 2025 |
| Evaluation-as-a-game | DeepMind | [2502.20170](https://arxiv.org/abs/2502.20170), ICLR 2025 | Feb 2025 |
| Anytime-valid stopping **in imperfect-information games** | Tsinghua (Longbo Huang) | AV-AIVAT, [2608.06362](https://arxiv.org/abs/2608.06362) | **6 Aug 2026** |
| Certifying opponent deviation from equilibrium | Tsinghua | [2607.28520](https://arxiv.org/abs/2607.28520) | Jul 2026 |
| **External** regret as an eval metric | Park, Liu, Ozdaglar, Zhang | [2403.16843](https://arxiv.org/abs/2403.16843), ICLR 2025 | Mar 2024 |
| QRE / strategic sophistication | two independent groups | [2603.10029](https://arxiv.org/abs/2603.10029); [2502.20432](https://arxiv.org/abs/2502.20432) | 2025 **and** 2026 |
| Cognitive hierarchy / level-k | CHBench | [2508.11944](https://arxiv.org/abs/2508.11944) | Aug 2025 |
| Benchmarks as a manipulable mechanism | **Hardt** (Chen, Zhang, Hardt) | [2603.08371](https://arxiv.org/abs/2603.08371) | Mar 2026 |
| Leaderboard rigging as social choice | Gordienko et al. | [2605.23628](https://arxiv.org/abs/2605.23628) | May 2026 |
| Cost–capability Pareto frontiers | HAL; Oxford | [2510.11977](https://arxiv.org/abs/2510.11977); [2606.26836](https://arxiv.org/html/2606.26836v1) | 2025–26 |
| Insurance priced off model performance | **Munich Re aiSure** | [2412.04166](https://arxiv.org/abs/2412.04166) | Dec 2024 |
| Real-money agent arena at scale | dev.fun Poker Arena | 30k+ agents, 1.2M hands week one | Jun 2026 |
| LLM-as-deceiver in social deduction | ~30 papers | Werewolf, Mafia, Among Us, Avalon | saturated |
| Shapley, revealed preference, Bayesian persuasion | multiple | — | saturated |

Two claims we must never make: **"first to price off a capability measure"** (Munich Re) and
**"first real-money agent arena"** (dev.fun). Both are false and trivially checked.

---

## 2. Mathematically dead in OUR game. This is the money-saver.

Our substrate is two-player zero-sum with an exact solver. Three fashionable concepts are not
merely weak there — they are constant or undefined:

- **Price of Anarchy / Stability = 1, identically.** Constant-sum means the payoff sum is the
  same at every outcome, so numerator and denominator are the same number. No estimator
  rescues this.
- **Correlated / coarse correlated equilibrium collapses to the minimax value.** Every CE has
  payoff exactly (v, −v); CCE marginals form a Nash equilibrium. Proof: [2304.07187](https://arxiv.org/abs/2304.07187).
  No payoff-based score can separate CE from CCE from Nash here.
- **Stackelberg / commitment is worth exactly zero.** By the minimax theorem the leader's
  commitment value equals the maximin value equals the Nash value.

All three encode *gains from coordination, correlation, or commitment*, and zero-sum is
precisely where those are worthless. Reviving them requires general-sum or 3+ players — which
costs us the exact solver, our main technical asset. That is a genuine strategic fork.

**The Stackelberg one is the trap.** It is fashionable, reads as sophisticated, and Hardt's
result makes it look validated. But Hardt's Stackelberg game is between *developers and the
benchmark* — a general-sum meta-game where commitment genuinely has value. That validity does
not transfer to a zero-sum card game.

---

## 3. What is actually left

### 3a. **Exploitation velocity — the best idea here, and it is a reframing**

Everyone in the poker/game-solving literature computes a best response to measure
exploitability. **Everyone reports the converged scalar and throws the best-responder's own
learning curve away as amortized compute cost.** Nobody has ever treated that curve as a
property of the *target* agent.

But it is one. Two agents can share an identical converged exploitability and be completely
different risks: one leaks its whole edge in twenty hands, the other takes two thousand. For
anyone with money on the table that difference is the entire question, and no benchmark
reports it.

The negative evidence is the cleanest in this whole document:

- arXiv full-text query `("how quickly" OR "how fast") AND "exploit" AND "opponent"` →
  **zero results**
- LBR ([1612.07547](https://arxiv.org/abs/1612.07547)) and ISMCTS-BR ([2004.09677](https://arxiv.org/abs/2004.09677)) built the machinery and report converged
  mbb/g only
- **Profit is the Red Team** ([2603.20925](https://arxiv.org/abs/2603.20925), Mar 2026) built a learned profit-maximising adversary
  against six LLMs — and reports **only the endpoint**, from 20 episodes after optimisation.
  No learning curves, no rounds-to-exploit. They own "LLMs are exploitable by an adaptive
  adversary." They do not attempt "how fast."

The structural template already works in an adjacent field: **Adaptive Adversaries**
([2607.18063](https://arxiv.org/abs/2607.18063), 20 Jul 2026) reports attack success as a function of adaptive rounds —
0–1% at turn 1 rising to 5.4–14% over 15 rounds. That is a rounds-to-exploit curve. It is for
jailbreaks, it is four weeks old, and somebody will port it.

**Why this one is ours to take:** it is non-degenerate at zero-sum; the machinery is already
built (`exploit/mixed.go` bootstrap prober mixture, exact `BestResponse`, `sequential.go`);
and it is the literal risk model for a staked arena — "how many hands before an adapting
opponent starts beating this agent" is the question a developer putting money down actually
asks.

**Naming matters here.** "Exploitability" in LLM-land is now overwhelmingly cybersecurity
vocabulary — searches drown in ExploitBench and jailbreak papers. Use
**best-response sample complexity** or **time-to-exploit**, never "exploitation velocity"
alone.

### 3b. Absolute, pool-independent exploitability per model — BUILT, UNSHIPPED

Confirmed twice, by two agents using different methods. The hard negatives:

- The canonical survey **Game Theory Meets LLMs** ([2502.09053](https://arxiv.org/abs/2502.09053)) contains **zero** occurrences of
  "exploitability" or "NashConv" in its full text.
- `abs:"NashConv"` returns **exactly one paper in all of arXiv** — non-LLM.
- **GTBench full text verified: zero occurrences** of "exploitab", "nashconv" or "best
  response". Its "Regret Value" is a crude one-shot hindsight deviation against the opponent's
  *realised* action (auction: `b1−(b2+1)`). It is not ε.
- All four cs.CL papers with "exploitability" in the abstract use it in the **security** sense.
- **PokerSkill** ([2605.30094](https://arxiv.org/abs/2605.30094), May 2026) runs LLMs against GTOWizard and Slumbot and reports
  only mbb/hand (GPT-5.5 XHigh −57±21, Claude Opus 4.6 −80±29). LLM poker, no exploitability,
  no LBR.

**Closest published quantity, and it is DeepMind's:** *Steering Language Models with
Game-Theoretic Solvers* ([2402.01704](https://arxiv.org/abs/2402.01704), Gemp, Lanctot et al.) prints NashConv in a results
table — but that is the *CFR solver's* residual, a convergence diagnostic. Its **"CFR Gain"**
(0.106, 1.596) measures how much a player gains by switching to CFR against a population of
baseline LLM agents, which is effectively a lower bound on the unsteered LLM's exploitability.
Single model, no ranking. They are one step away and have been since January 2024.

Also near: *Safe Equilibrium Policy Optimization* ([2605.30854](https://arxiv.org/abs/2605.30854), EMNLP 2026) penalises
exploitability in GRPO — but over a **fixed exploiter pool**, and the authors explicitly call
it "exploit-pool advantage rather than true worst-case exploitability."

Why the incumbents do not cover it:
- `polarix` solves an equilibrium **over a population** → pool-relative. Change the pool,
  change the number.
- AV-AIVAT answers **"is agent A stronger than agent B"** → pairwise.
- Neither answers **"how far from optimal is this one model, absolutely."**

That is `ε(σ) = u(BR(σ), σ)` with `v = 0` by symmetry, which is what `internal/exploit`
computes and `cmd/labcert` runs. It exists, it is tested, it has never been pointed at a
frontier model.

### 3c. Behavioural CCE-gap — the distinction that matters

DeepMind uses CCE as an **aggregation rule over a static payoff matrix of model-vs-task
scores**. Nobody uses CCE as a **behavioural measure of what agents actually do when they
interact**. Different products.

Park et al. stopped one inferential step short: they established regret as an eval metric and
wrote that "equilibria will emerge," but the phrase "coarse correlated equilibrium" appears
**zero times** in their paper. The step — *no-regret LLM play ⇒ measure the empirical CCE-gap
and rank by it* — is unclaimed.

Caution: we would be building in a room where DeepMind owns the vocabulary.

### 3d. The weld — anytime-valid equilibrium-deviation certificates

Two halves exist and have never been joined:

- **Statistics half:** Gauthier, Bach & Jordan, [2601.05427](https://arxiv.org/abs/2601.05427) — e-value test supermartingales
  detecting deviation from Nash/CE/CCE in repeated games. Code released. **Zero LLMs.**
- **LLM half:** Park et al. — measured LLM regret in exactly those games. **Zero statistics on
  the measurement**: post-hoc, fixed-horizon, no confidence interval on regret.

Nobody has welded them. Estimated window **6–12 months**; Longbo Huang's group is closest.

### 3e. The Φ-regret deviation lattice

Swap / internal / Φ-regret on real LLM agents is unclaimed — those strings do not appear in
Park et al. at all. The formal machinery is Morrill, Greenwald & Bowling,
[2102.06973](https://arxiv.org/abs/2102.06973) (ICML 2021): a lattice of deviation types, of which exploitability is only
the **top**. The graded, cognitively-bounded classes underneath say *what kind* of opponent
beats a model — beatable only by a solver-driven best response is a very different commercial
risk from beatable by a fixed swap rule anyone can write in an afternoon.

---

### 3f. Duplicate design / common random numbers — free, unclaimed, and fixes our actual problem

The single most practically valuable finding in the sweep.

**Common random numbers as a named, imported technique is absent from LLM evaluation.** Three
separate papers — Miller's *Adding Error Bars to Evals* (Anthropic, Nov 2024), Sharma
([2512.24145](https://arxiv.org/abs/2512.24145)), Dong et al. ([2602.03061](https://arxiv.org/abs/2602.03061)) — each independently **rederive** the paired /
control-variate estimator from first principles **without citing the sixty-year-old
simulation and operations-research literature**. Miller never writes "common random numbers,"
"control variates," or "antithetic variates."

And the canonical games version is untouched: a full-text search for
`duplicate AND variance AND LLM AND game` returns **zero results**. **Duplicate bridge** —
where the same deal is played by both sides and only the difference is scored — is the
original CRN design, it is a century old, and nobody has imported it to an LLM game benchmark.

**Even AV-AIVAT leaves this open.** Our nearest competitor evaluates over "71,439 **paired**
HUNL hands" but **treats the pairing mechanism as assumed rather than specified**, and does not
cite the CRN / duplicate-poker tradition at all. The variance reduction is claimed; the design
that produces it is unexamined.

For us this is not a paper idea first, it is a **fix**. `METHODS.md` §3 admits deal luck is
uncontrolled, and that is the confounder that produced separability 0.00 and four reversals.
Replaying the identical prize sequence with seats swapped removes it — at no extra API cost
beyond the mirrored leg, which we were already paying for seat counterbalancing.

Contrast with the scaffold-variance question (§3g), which is contested: there we would be
arguing we did it *better*; here we would be arguing we did it *first*.

### 3g. Scaffold-vs-model variance — contested, and the field contradicts itself

Worth knowing but not worth leading with. Three careful papers give answers three orders of
magnitude apart on how much the scaffold matters: **7.80×** the model ([2605.23950](https://arxiv.org/abs/2605.23950), Tulane,
but a 3×3 fixed-effects toy), **0.4%** of variance ([2603.10044](https://arxiv.org/abs/2603.10044), pre-registered,
assessor-blinded, N=62,808 — the most rigorous single study found), and **1.5%** ([2604.18805](https://arxiv.org/abs/2604.18805),
2PL IRT + Bayesian GLM).

Nobody has reconciled them, and **everyone who looked carefully agrees the model×scaffold
interaction dominates the main effects** — agent×task interaction runs 4–13× the agent main
effect ([2608.11323](https://arxiv.org/abs/2608.11323), proper G-theory, but no scaffold facet).

Correction for the record: HAL does **not** say scaffold impact is "unexamined." Its actual
phrasing is *"comparisons across scaffolds are rare."* HAL also does **zero inferential
statistics** — 21,730 rollouts, single run per configuration, descriptive frontiers only.

## 4. Do this first, because it is free

**AIVAT gives a median 54× variance reduction** in imperfect-information game evaluation
(AV-AIVAT, measured over 71,439 paired HUNL hands). It is a control-variate technique: it costs
no extra matches.

This is the direct fix for the failure documented in `METHODS.md` — deal luck uncontrolled,
separability 0.00, four conclusions reversed by five extra matches. Same spend, far more power.

**And it exposes a weakness in our own stack.** `internal/exploit/sequential.go` uses
group-sequential alpha spending — coverage on a *grid*, not anytime-valid. AV-AIVAT uses
genuine confidence sequences. On that axis the state of the art is now ahead of us.

---

## 5. What the incumbents get wrong (our credibility opening)

- **LMArena continuously monitors, continuously re-ranks, and publishes confidence intervals
  computed as if it had stopped once.** That is invalid under optional continuation. None of
  ~30 arena-adjacent papers has fixed it.
- **Anthropic's *Adding Error Bars to Evals*** ([2411.00640](https://arxiv.org/abs/2411.00640)) — the field's de facto standard —
  is entirely fixed-n. Its power-analysis framing is the opposite of adaptive stopping.
- **METR** uses hierarchical bootstrap; no sequential machinery.
- **statsforevals.com**, the community's ~60-reference stats guide, has **zero entries** on
  sequential testing, e-values, confidence sequences, or anytime-valid inference.
- **HAL is frozen** — Princeton spent ~$40,000 building the best cost-controlled agent
  leaderboard and stopped updating it. Read that as a signal about commercial pull.

The applied evaluation community has not absorbed the anytime-valid literature at all.

---

## 6. Non-technical risk nobody in the research raised

**Real-money staked matches between agents is gambling in most jurisdictions.** dev.fun runs on
Monad; Realbet is offshore crypto. This constrains the commercial claim more than any technical
question in this document, and no paper surveyed addresses it.

Second tension, permanent: a rating good for **pricing bets** needs calibration; a benchmark
needs **construct validity**. They are not the same objective.

---

## 7. Honest verdict

**We cannot invent a new field.** DeepMind reached equilibrium rating first and ships it in
production; Tsinghua reached anytime-valid game evaluation two weeks ago. Anyone claiming
otherwise has not looked.

**There is a real, defensible, narrow position:** absolute pool-independent exploitability, the
deviation lattice, and the anytime-valid equilibrium weld — running on infrastructure
(completion binding, contamination immunity by construction, real stakes) that none of them
have.

That is a strong technical moat and a publishable contribution. It is not a new field, and the
window looks like about a year.

---

## Caveats — read before betting

- The session's **200-call web search budget was exhausted partway**; later work ran on the
  arXiv API and direct fetches. **Non-arXiv venues (AAMAS, EC, IJCAI, ACL Anthology) are
  under-sampled.**
- arXiv's API indexes **title/abstract/comments, not full text**. A paper using a concept only
  in its body would be missed. For "does this concept *headline* a metric," abstract-level
  absence is strong evidence; for anything else it is weak.
- A large share of 2026 hits in these niches are single-author, no-venue, template-shaped
  preprints. **Discount apparent crowding by roughly half** — but not the named groups.
- **Unverified and worth closing:** GOE-LLM (OpenReview served a bot-check on every fetch);
  UK AISI methodology (domain blocked); Chatbot Arena's detailed statistics (fetches failed;
  the *absence* of anytime-valid methods is corroborated, the positive description is
  second-hand); Kaggle's pooled Bradley–Terry unified leaderboard internals.
- **Design threat, not just a citation:** Poker Arena ([2606.13815](https://arxiv.org/abs/2606.13815)) ablated persistent
  memory and found it helped GPT (+114.6 chips/session) while HURTING Claude (−42.5) and Kimi
  (−109.4). Any cross-episode adaptation metric measures the memory-interface choice as much as
  the model. Address it explicitly or the result gets attacked on it.
- **Named competitive threats:** Marc Lanctot's DeepMind group (wrote OpenSpiel, popularised
  NashConv, already publishing on LLM eval); Longbo Huang's Tsinghua group (two papers in three
  weeks); GTO Wizard (already runs LLMs against a near-Nash agent, [2603.23660](https://arxiv.org/abs/2603.23660); explicitly
  flagged computing a best response as out of scope).
