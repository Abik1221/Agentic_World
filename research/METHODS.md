# Pyyol Lab — methods and construct validity

This document exists so a reviewer can decide what our numbers are worth without running
anything. It follows the 2026 reporting conventions for LLM evaluation: state the phenomenon,
justify the sampling, isolate the confounders, analyse the errors, document every adaptation,
and say what prevents contamination.

Where a section says we do not do something, that is the finding, not an omission.

---

## 1. The phenomenon being measured

**Claim under test:** that a language model, driven by a fixed agent scaffold, chooses better
moves in a zero-sum imperfect-information game than another model driven by the identical
scaffold.

**What that deliberately is not:**

- Not a claim about the model alone. The unit under test is the *system* — model plus scaffold
  plus prompt plus tool schema plus endpoint under a shot clock. A different scaffold could
  reorder the results, and we say so wherever we report an ordering.
- Not a claim about general strategic ability. Goofspiel is one game with one payoff structure.
- Not a claim about reasoning quality. We observe moves and token counts, never the content of
  a chain of thought.

**Why Goofspiel.** It is zero-sum, simultaneous-move, imperfect-information, and — with equal
starting hands — *symmetric*. Symmetry gives the game a value of exactly **0** with no
equilibrium solve required, so a score differential is an absolute quantity rather than a
statement about the opponent pool. Very few games available to an LLM benchmark have a known
value; most leaderboards therefore measure only relative standing.

---

## 2. Sampling strategy, and why it is currently insufficient

Round-robin over all pairings, with a subset replayed with seats swapped.

**Stated plainly: the sample is too small to order these models.** In the 2026-08-20 run every
Wilson interval overlapped every other, separability was 0.00, and going from 13 to 18 matches
reversed four conclusions including which model beat which. Published comparators run
**50 matches per pairing** (GTBench); we ran 1–3.

We report the reversal rather than the first, tidier result. A benchmark that publishes only
the run that looked conclusive is selecting on outcome.

**Power.** No prospective power analysis was performed before the run. That was a mistake and
is the reason the budget was spent at an uninformative sample size. Any future run states the
detectable effect size before spending.

---

## 3. Confounders, and what is done about each

| confounder | control |
|---|---|
| Seat/first-mover advantage | Pairings replayed with seats swapped. Only partially applied — 6 of 10 pairings in the last run. |
| Deal luck | **Not controlled.** Each match draws its own prize order, so score variance includes deal noise. Common random numbers across seat-swapped legs would remove most of it and is not yet implemented. |
| Scaffold differences | Eliminated by construction: one agent implementation, one prompt builder, one tool schema. Only the model identifier varies. |
| Prompt starvation | The seat's **entire view** is sent — history, running scores, opponent's remaining hand, legal actions, shot clock. An earlier version sent a one-line summary, which made questions about memory and long-horizon play unanswerable by construction. |
| Output-budget bias | `max_tokens` is 4096 for every model. At 256 a reasoning model spends its budget thinking and never emits the tool call, which records as the model failing to decide. A cap that converts "thinks first" into "cannot play" is a thumb on the scale. |
| Client impatience | Gateway client timeout 180s. At 30s the slowest model's turns timed out and were recorded as it failing, when it was us giving up. |
| Opponent-pool composition | Not controlled in the win-rate view — this is inherent to relative metrics and is the reason exploitability (§7) is the preferred measure. |

---

## 4. Error analysis

**Bind rate and fallback rate are different measurements and must both be reported.**

- **Bind rate** — of the model calls that were *made*, the share whose move was extracted from
  the model's own structured tool call and HMAC-bound to
  `(agent, match, round, completion hash, extracted move)`. In the last run: **100%**, zero
  illegal moves.
- **Fallback rate** — the share of *turns* where no model call happened at all and the arena
  substituted a legal move because the seat missed its window. In the last run: **64 of 615
  decisions, 13.9%**, and only **1 of 20 matches** was played end to end by the models alone.

A bind rate of 100% alongside a fallback rate of 14% is not a contradiction: the first says
nothing dishonest got in, the second says how much of the play was actually the model's. An
earlier version of our published page reported the first as though it implied the second. It
does not.

Fallbacks are not uniformly distributed. They concentrate in the slowest models, which means
the models most likely to time out are the ones whose measured play is most diluted — and
removing those matches moved one model from mid-table to a clean last. Any published board
therefore records the fallback tolerance it was computed at (`seed-research -max-fallbacks`,
default 0).

**Match completeness.** Two matches in the last run "finished" only because their agents were
killed mid-game; the arena played the remainder and the surviving seat won 81–10 against an
opponent that had stopped answering. These are excluded by the fallback guard. Without it they
would have entered the board as wins and credited a model for its opponent's outage.

---

## 5. Adaptations from standard practice, declared

- **Bradley–Terry with match-level bootstrap** rather than raw win rate, because the schedule is
  unbalanced. Resampling *matches* rather than decisions is deliberate: decisions inside one
  match are not independent, and resampling them manufactures intervals that are far too tight.
- **A weak prior** of half a virtual game per pairing. Without it an undefeated model has no
  finite maximum-likelihood strength and the bootstrap regularly produced ratings near 9900,
  which reads as a broken board rather than an uncertain one.
- **Margin is discarded** by Bradley–Terry: 79–12 and 46–45 count identically. In a game scored
  0–91 that throws away most of the signal. A margin-aware model is not yet implemented.
- **Elo presentation.** Ratings are shown on the conventional 1500-centred scale for
  readability. Elo cannot represent non-transitive relationships, which are common in this
  setting; `separability` is published alongside so the reader can see how much of the ordering
  the evidence supports. It was 0.00 at any tolerance and 0.10 at the published one.

---

## 6. Contamination

**Structural, not mitigated.** There is no question set. Prize orders are randomised across a
published set of boards, the game is solved exactly rather than scored against a stored answer
key, and the agent sees a generated game state rather than a retrieved item. There is nothing
to leak into a pre-training corpus and nothing to memorise between runs.

This is a stronger position than the usual defences — rolling questions, post-cutoff sourcing,
private held-out sets — which slow contamination rather than remove the surface.

Two caveats we do not paper over. The *rules* of Goofspiel are certainly in pre-training data;
what cannot be memorised is the specific board and opponent. And a single fixed board would be
memorisable, which is why the spec cycles a set of them and pins the set in the run's hash.

---

## 7. What we consider the correct metric, and why it is not in the last run

Win rate against a pool is pool-relative: it moves when the pool changes, it can be farmed by
choosing weak opponents, and two labs cannot compare results a year apart.

**Exploitability is absolute.** For a two-player zero-sum game with value `v`,

```
eps(sigma) = max_{sigma'} u(sigma', sigma) - v
```

For the symmetric ladder `v = 0`, so `eps(sigma) = u(BR(sigma), sigma)` — a best response, no
equilibrium solve. Zero means unexploitable. The opponent pool does not appear in the formula,
so no matchmaking manipulation moves it.

This is the poker community's standard measure (Local Best Response, Lisý & Bowling), and to our
knowledge it is not used by any published LLM leaderboard. Our implementation
(`internal/exploit`, driven by `cmd/labcert`) adds two things that literature generally lacks:

- a **certified upper bound** as well as a lower one. A lower bound alone cannot order agents —
  our own audit measured Kendall τ = 0.49 against true exploitability with 25% of pairs
  inverted. Only when two agents' intervals are disjoint is their order certified.
- **group-sequential stopping** with alpha spending, so an obviously exploitable agent is cheap
  to certify and only near-optimal ones consume the full budget. Naive "stop when the bound
  looks good" invalidates the guarantee; the schedule is pre-committed.

**The 2026-08-20 run did not use it.** It used the pool-relative method the machinery exists to
replace. That is the single largest methodological gap in the published result, and it is ours,
not the field's.

---

## 8. Known gaps

- **Provider substitution is not verified.** Completion binding proves a move came from the call
  the gateway made; it does not prove the provider served the model it advertised. A substituted
  or quantised model behind the API binds identically. Model-equality testing
  (`internal/modeleq`) addresses this and is reported separately.
- **Harness variance is unmeasured.** The field reports identical weights scoring 10–20 points
  apart across harnesses. We have never run the same models through a second harness.
- **No item-response-theory indices.**
- **Single game configuration** in the last run.
- **Deal luck uncontrolled** (§3).

---

## 9. Released artefacts

Each run directory contains the evidence, not just the summary:

| file | contents |
|---|---|
| `matches.json` | every eligible match: seats, model per seat, final score, coins, fallback and decision counts |
| `decisions.json` | every model call: match, round, model, provider, upstream host, bound flag, prompt/completion/reasoning tokens, latency, HTTP status |
| `usage.json` | per-model aggregates |
| `README.md` | the run's findings, including retractions |

Figures come from `agent_model_calls` and `match_players` — the gateway's own records.
**`agent_match_decisions.model` is the agent's self-declared string and must never be used for
model attribution**; it reports the persona, not the model that ran.
