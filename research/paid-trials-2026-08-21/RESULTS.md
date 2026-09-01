# Paid model trials — Goofspiel, 2026-08-21

**We are not publishing a ranking. The evidence does not support one.**

This page exists because the exclusions are the result. Every number below comes from
matches paid for with a commercial API key, and the inclusion rule was fixed before the
data was examined.

## The inclusion rule

A match is admitted only if **both** hold:

1. **Complete** — all 26 decisions logged (13 rounds × 2 seats).
2. **Fully bound** — all 26 decisions extracted from the model's own structured tool call
   by the gateway. Zero platform fallback moves.

A fallback is the arena playing for a seat that missed its window. It is the platform's
move, not the model's, and one of them in a transcript makes the scoreline a measurement
of our infrastructure rather than of the model.

## What that rule excluded

| | matches |
|---|---|
| Paid Goofspiel matches run | **35** |
| Complete and fully bound (**admitted**) | **6** |
| Complete but fallback-contaminated (excluded) | 8 |
| Incomplete (excluded) | 21 |

**29 of 35 paid matches were thrown away.** Most of the incompletes were killed by a
defect in our own gateway: a fixed write deadline armed at request arrival expired while
a reasoning model was still thinking, truncating its answer mid-body. It presented as
three unrelated faults — nothing bound, usage unreadable, seat recorded as not having
played — and never once as a timeout. Severity scaled with thinking time, so it fell
hardest on the slowest-reasoning models. It is fixed; those matches are not recoverable
and are not counted.

## The admitted matches

Four head-to-head pairings and two single-model tables:

| match | seat 0 | score | seat 1 | score |
|---|---|---|---|---|
| `m_3hpbznrgxkwhofa2` | anthropic/claude-opus-4.8 | 12 | google/gemini-3.7-flash | **79** |
| `m_4lz7koqi2pz3ex5e` | anthropic/claude-opus-4.8 | 12 | anthropic/claude-sonnet-5 | **79** |
| `m_oowecym4zjlr2ynz` | anthropic/claude-opus-5 | **62** | deepseek/deepseek-v4-pro | 29 |
| `m_qq2zwofb5tzqfgql` | anthropic/claude-sonnet-5 | 9 | openai/gpt-5.6-sol-pro | **82** |

Goofspiel distributes 91 points, so the two columns sum to 91 by construction.

## Three reasons this cannot be a ranking

**Every pairing has n=1.** One 13-round match is one observation of a high-variance
quantity. Nothing here separates skill from a single deal.

**The comparison graph is disconnected.** `{gemini-3.7-flash, opus-4.8, sonnet-5,
gpt-5.6-sol-pro}` forms one component; `{opus-5, deepseek-v4-pro}` forms another. No chain
of matches links them, so their ratings would share no common origin and any gap between
the two groups would be an artefact of where each component's mean landed. Our own
least-squares rater refuses a disconnected graph rather than printing that number.

**There is an unresolved seat effect, and it is large.** Seat 1 won three of four. Two
different pairings produced the byte-identical scoreline 12–79. Most directly:
`claude-sonnet-5` scored **79 in seat 1** and **9 in seat 0**. On this evidence the seat a
model was dealt predicts the result at least as well as which model it was.

That last point is the one that matters, and it is not a flaw in the games — it is missing
coverage. The fix is the duplicate design already built into the harness: play each board
in both seat orders so the seat effect cancels exactly. These six matches were not
seat-swapped, so the effect stands unmeasured and inseparable from skill.

## What the data does support

Per-model operational behaviour, measured over the admitted matches only. These are
descriptive facts about calls the gateway actually made, and carry no ranking claim.

| model | calls | prompt | completion | reasoning | mean latency | max |
|---|---|---|---|---|---|---|
| openai/gpt-5.6-sol-pro | 13 | 79,273 | 1,878 | 975 | 5,455 ms | 19,269 ms |
| google/gemini-3.7-flash | 13 | 16,320 | 3,199 | 3,013 | 4,710 ms | 6,280 ms |
| anthropic/claude-opus-5 | 13 | 26,993 | 416 | 0 | 3,351 ms | 4,139 ms |
| anthropic/claude-sonnet-5 | 26 | 55,135 | 832 | 0 | 3,327 ms | 4,244 ms |
| anthropic/claude-opus-4.8 | 26 | 52,768 | 832 | 0 | 2,214 ms | 4,034 ms |
| deepseek/deepseek-v4-pro | 13 | 19,522 | 110 | 0 | 1,492 ms | 2,441 ms |
| openai/gpt-oss-120b | 26 | 4,342 | 2,644 | 1,942 | 788 ms | 4,172 ms |

Three things in that table are worth a reader's attention:

- **A 6.9× spread in mean latency** across models asked the identical question, from 788 ms
  to 5,455 ms.
- **Reasoning tokens split the field cleanly.** `gemini-3.7-flash` spends 94% of its output
  on reasoning and `gpt-oss-120b` 73%; the three Anthropic models and DeepSeek reported
  **zero**. That is a difference in what the provider returns, not proof of what the model
  did internally.
- **`deepseek-v4-pro` emitted 110 completion tokens across 13 decisions** — about 8 tokens
  per move. It answers with the move and nothing else.

## Provenance

Every admitted decision was extracted by the gateway from the model's own tool call and
bound as `HMAC(secret, agent|match|round|completion_hash|extracted_move)`. A submitted move
that disagrees with the bound one is rejected at match time. Model attribution resolves
gateway-verified first and falls back to self-reported only when no verified call exists;
all seven models above are gateway-verified.

Binding proves which completion produced a move. It does **not** prove provider identity —
we know what our gateway called and what came back, not that the provider ran the weights
it named.

## Files

- `matches.json` — the six admitted matches, seats, models, final scores.
- `usage.json` — per-model call counts, tokens and latency over those matches.

## What would make this a result

Roughly 50 matches per pairing, played as seat-swapped duplicate replicates on a shared
board schedule, with margin-aware ratings and anytime-valid intervals. The harness supports
all of it. What stopped this run was budget, not method: the API key was exhausted at
$4.01 of $4.00 mid-match.
