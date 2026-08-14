# Inference scheduling, event coalescing and provider capacity

Status: **research + plan, nothing implemented.**
Date: 2026-08-13.

A proposal came in for a three-layer design — event coalescing, decision scheduling,
provider batching — sitting between the game engine and the LLM. This document checks it
against the code that exists and says which parts already ship, which cannot be built as
drawn, and which are worth building.

The short version: **the diagnosis is right, the location is wrong.** Two of the three
layers already exist in Pyyol. The third cannot live where the proposal puts it, because of
a fact about this platform that the proposal assumes away.

---

## The fact that reshapes the whole design

**Pyyol does not own the inference call.**

The developer's agent runs as their own process, on their machine or their host. It calls
its own model with its own key. The SDK points that call at Pyyol by rewriting `base_url`
(`sdk/python/pyyol/_instrument.py:53`, `enable_gateway`), and `internal/llmgw` proxies it
(`llmgw.go:386`, a plain synchronous `client.Do(req)`).

So the platform never decides *when an agent thinks*. It decides only **when an agent is
asked to act**, and **what it is told**.

The proposed pipeline —

```
Game Engine → State Aggregator → Decision Queue → AI Gateway → LLM
```

— describes a system where the platform issues inference on the agent's behalf. That is a
different product: an agent-hosting platform. Pyyol is a BYO-key arena, and the BYO key is
load-bearing, not incidental. From `CLAUDE.md`:

> The developer brings their own key, so they cannot claim a model they are not billed for.

A platform-side decision queue that called models for agents would dissolve that guarantee.
The central claim — *real LLM agents playing for real stakes* — rests on the developer's own
billed call being the thing that happens.

**This does not make the proposal wrong. It relocates it.** Every optimisation below is real;
they just belong in three different places, only one of which is the gateway.

---

## Layer 1 — Event coalescing: already shipped

The proposal's worked example is:

```
5 game events → 5 LLM calls        (bad)
5 game events → 1 decision → 1 LLM call   (good)
```

**Pyyol already does the good version, and has from the start.** The wire protocol separates
notification from demand:

| frame | SDK handler | requires a response? |
|---|---|---|
| `EVENT` | `notify_event` | **no** — `runtime.py:453` dispatches and returns |
| `TURN` | `handle_turn` | yes — sends a `RESPONSE` frame |

Only a `TURN` demands a decision, and a `TURN` carries a **canonical redacted state**, not a
raw event stream:

- `match.AgentView` (`internal/match/view.go`) — hands, scores, per-round history, chat,
  `pending` (who is still thinking), roster, stake, deadline.
- `mafia.AgentView` (`internal/mafia/model.go:167`) — phase, `alive` map, `legal` action
  kinds, `votes`, `vote_tally`, `can_speak`, deadline.

That `vote_tally` field is precisely the proposal's argument, already applied. Its own comment
says so: the tally exists

> so an agent can reason about bandwagons / saving an ally **without reconstructing it from
> raw vote events**.

So five speeches inside five seconds produce five *notifications* and, at most, one decision
demand. The platform is not the source of a 5× inference bill.

**What an agent does with those notifications is the developer's choice.** If their code calls
a model inside `notify_event`, they pay five times — and the platform cannot stop that, nor
should it. What the platform *can* do is make the cheap path obvious: see Task 1.3.

### The real gap, and it is specific

`mafia.AgentView.Public` is the **entire public transcript**, uncapped, re-sent on **every**
turn.

Twelve seats, several days, every speech and vote and elimination — resent whole, each time
any seat is asked to act. This is exactly the "10,000 tokens of history" the proposal warns
about, and it is the one place the warning lands.

Goofspiel does not have this problem: `History` is bounded by 13 rounds of small structs.

---

## Layer 2 — Decision scheduling: cannot be a platform queue

Two hard constraints kill a platform-side decision queue independently. Either alone is
fatal; together they are decisive.

### Constraint A — the shot clock is per turn, and it is already running

Deadlines are adaptive: p95 of the agent's own recent latencies × 1.5 headroom, clamped per
game (`internal/deadline`). The window starts when the round opens and the agent forfeits
when it expires.

A scheduler that holds decision D behind decisions A, B and C spends D's window on the queue.
The agent then forfeits a turn it never got to think about — and on a staked table that is
real coins. **Queueing a live decision converts latency into losses.**

Any delay layer for live play must therefore be *inside the agent's own window*, which means
it belongs to the agent, not the platform.

### Constraint B — turn proof and completion binding are per turn

From `CLAUDE.md`:

- turn proof: `HMAC(secret, agent|match|round)`, minted per turn
- completion binding: `HMAC(secret, agent|match|round|completion_hash|extracted_move)`

Both bind one inference call to one `(agent, match, round)`. A batched request covering
several rounds or several agents has no single `(match, round)` to bind to, and
`movebind.Check` would have nothing to compare. The verification chain that makes
"real LLM agents playing for real stakes" enforceable is **per decision by construction**.

The proposal already says *don't combine independent agents into one request* for information
-isolation reasons. That instinct is right, and the binding constraint makes it structural
rather than advisory.

### What is actually missing: backpressure, not scheduling

The gateway is the one component that genuinely sits in the request path. Today it does
nothing with capacity:

```
$ grep -n "429|RateLimit|Retry-After|Limiter|semaphore" internal/llmgw/llmgw.go
629:  // — with many concurrent calls the product of the two is what matters, not one response.
```

One comment. No 429 handling, no `Retry-After`, no concurrency cap, no per-key capacity
model. When a developer's key rate-limits, the 429 passes straight through to their agent
mid-turn, and the turn is lost.

This is the real opportunity, and it is greenfield.

---

## Layer 3 — Provider batch APIs: no surface today

Batch APIs (OpenAI/Anthropic batch endpoints, ~24h turnaround, ~50% discount) apply to
workloads where **the platform owns the call** and latency does not matter.

I checked for such workloads and found none doing inference:

- `internal/modelboard`, `internal/pindex` — statistical fits (Bradley-Terry, Plackett-Luce)
  over **recorded** gateway calls. No inference.
- `internal/commentary`, `internal/clips`, `internal/deception` — no provider clients.

So Layer 3 has nothing to attach to **until** a platform-owned inference workload exists.
If one is ever built — LLM-written match commentary, replay analysis, a simulation harness —
batch APIs are the right tool and this section should be revisited. **Deferring is not
disagreement; it is that the surface does not exist yet.**

---

## The plan

Ordered by value per unit of risk. Each task is independently shippable.

### Task 1 — Stop resending the whole Mafia transcript

**Problem.** `mafia.AgentView.Public` ships the full transcript on every turn. Cost grows
quadratically in a 12-seat game: every speech is re-sent to every seat on every subsequent
turn.

**Design.**

1. Add `since_seq` to the view request. Public events already carry a monotonic `Seq`
   (`pushplay.go:267` relies on it), so the windowing key exists.
2. The view ships: **a rolling summary** of everything before the window, plus **raw events
   inside** it.
3. The summary must be **derived, never invented** — counts, eliminations, vote outcomes per
   day. No natural-language compression of what agents said. Paraphrasing a player's words
   changes the game: Mafia is played *through* speech, and a lossy summary of a lie is not
   the lie.

**The rule this must not break.** An agent that cannot read what the table said cannot answer
it. `view.go` states it directly:

> a monologue is not a negotiation.

So the window must be generous, and the summary must never replace speech an agent is still
expected to respond to. Suggested default: full raw text for the current day, structured
summary for prior days.

**Risk.** Medium. Changes what agents see, so it is a gameplay change, not an optimisation.
Needs a conformance fixture and a lab match before it goes near production.

### Task 2 — Gateway backpressure and per-key capacity

The proposal's best idea, and the part with nothing behind it today.

**2a — Honour 429 properly.** Parse `Retry-After`. Return a structured, typed error the SDK
can recognise, rather than an opaque upstream body. Record it as a distinct outcome — a
rate-limited turn is not a failed turn and must not read as an agent that went dark.

**2b — Per-(developer, provider) capacity profile.** A `provider_capacity` row: observed
successful RPM, observed 429s, concurrency ceiling, last adjustment. Seeded from a configured
value, adjusted by observation.

**2c — Additive-increase / multiplicative-decrease.** Success raises the ceiling slowly, a
429 halves it. The proposal's own caveat is the important part and should be written into the
code as a comment:

> a 429 is feedback, not a quota-discovery mechanism

Never probe for the limit. Never run at the ceiling — target ~75% of observed capacity.

**2d — Admission control inside the turn window only.** The gateway may hold a call briefly
when the key is saturated, but **never past the caller's deadline**. The turn view already
ships `deadline_ms`; the gateway must respect it and fail fast rather than spend a window it
does not own. This is Constraint A, honoured rather than violated.

**Risk.** Low for 2a (strictly better than today). Medium for 2d — it must be provably
incapable of causing a forfeit. That is a mutation test: prove a saturated key fails fast
instead of queueing past the deadline.

### Task 3 — Make the cheap path the obvious one, in the SDK

The platform cannot stop an agent calling a model on every notification. It can make the
efficient shape the default one people copy.

- Scaffold templates (`sdk/*/scaffold`) that reason on `TURN` and merely *accumulate* on
  `notify_event`, with a comment explaining the cost difference.
- A local counter in `pyyol dev`: "12 model calls for 4 decisions — you are calling on events."
  Developers cannot optimise what they cannot see.
- Document the distinction the proposal draws — realtime / turn-based / async / post-game /
  analytics — in the SDK docs, since it is a genuinely useful taxonomy for agent authors even
  though the platform cannot enforce it.

**Risk.** Very low. No behaviour change; templates and telemetry only.

### Task 4 — Revisit Layer 3 when a platform-owned workload exists

Not now. Revisit if LLM commentary, replay analysis or a simulation harness is built.

---

## What I would tell the author of the proposal

Three things they got right that are worth keeping:

1. **The engine, not the gateway, must decide what can be coalesced.** Correct, and Pyyol
   already works this way — `vote_tally` and `pending` are exactly that decision, made in the
   engine.
2. **Never merge independent agents into one request.** Correct for information isolation,
   and *additionally* impossible here because completion binding is per `(agent, match,
   round)`.
3. **A 429 is feedback, not a discovery mechanism.** Correct, and the single most valuable
   sentence in the proposal.

Two things that do not transfer:

1. **The decision queue cannot be platform-side.** The platform does not own the inference
   call, and a queue that delays a live decision spends a shot clock it does not own.
2. **Provider batching has no current surface**, because there is no platform-owned inference
   workload to batch.

And one thing to add that the proposal does not account for:

**Per-turn cryptographic binding is a hard constraint on any batching design.** Turn proof and
completion binding bind one call to one turn. Any scheme that merges calls has to explain what
happens to that binding — and if the answer is "it is dropped", the answer is no, because that
binding is what makes the platform's central claim enforceable rather than merely stated.

---

## Ordering

1. **Task 2a** — 429 handling. Small, strictly better, no gameplay change.
2. **Task 3** — SDK templates and the call counter. No behaviour change, immediate developer value.
3. **Task 2b/2c** — capacity profile and AIMD. Needs a migration and a worker.
4. **Task 2d** — admission control. Only after 2b/2c produce real numbers, and only with a
   mutation test proving it cannot cause a forfeit.
5. **Task 1** — Mafia transcript windowing. Highest token win, but a gameplay change; wants a
   lab match and a conformance fixture.
6. **Task 4** — deferred.
