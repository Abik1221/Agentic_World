# Surviving free-tier rate limits

Status: **research + plan.** Date: 2026-08-13.

The worry: developers on free API tiers get rate-limited and cannot finish a match. This
checks that against real 2026 limits and Pyyol's actual call demand, and the answer reframes
the problem — the wall is real, but it is **not** the one it looks like.

---

## What the free tiers actually give you (August 2026)

| provider | RPM | TPM | requests/day |
|---|---|---|---|
| Groq free | 30 | **6,000** | 14,400 |
| Gemini Flash free | 10 | 250,000 | **250** |
| Gemini Flash-Lite free | 15 | 250,000 | 1,000 |
| OpenRouter `:free` | 20 | — | **50** (1,000 after a one-off $10) |

Two changes worth knowing: Google removed Gemini **Pro** from the free tier (April 2026) and
cut free quotas 50–80% in December 2025, so "free tier" now means Flash. Groq's limits are
per **organisation**, not per key — five keys do not give you 150 RPM.

## What Pyyol actually demands

One agent, one match, one model call per decision:

| game | decisions per agent per match |
|---|---|
| Goofspiel | **13** (one per round) |
| Mafia | ~10–15 (speak + vote per day, night only for acting roles) |
| Monopoly | ~10–20 |

A Goofspiel match runs about 2–4 minutes, so 13 calls ≈ **4–5 RPM sustained**.

## The finding: RPM is not the wall

Against 4–5 RPM, every free tier above has headroom — even Gemini Flash's 10 RPM. So the
"cannot get through round one" failure is **not** the platform's turn cadence hitting a
per-minute cap. Three different walls are real, and they are not the obvious one:

**1. Requests per DAY.** OpenRouter free gives 50/day. At 13 calls a match that is **under
four matches a day**, then the key is dead until midnight. Gemini Flash's 250/day is ~19
matches. This is the binding constraint for most hobbyist keys, and nothing in Pyyol
currently tells a developer it exists until they hit it.

**2. Tokens per minute — and this one IS ours.** Groq free allows 6,000 TPM. `mafia.AgentView.Public`
ships the **entire public transcript on every turn**, uncapped. A 12-seat table on day three
can put a single prompt near or past 6,000 tokens, so **one call** exhausts a minute's budget.
That is not the developer's prompt being wasteful; it is the platform's view growing without
bound. Already identified in DESIGN-inference-scheduling.md as Task 1 — this makes it urgent
rather than tidy.

**3. Agents calling on EVENTS rather than turns.** This is where "cannot finish round one"
genuinely comes from. Mafia broadcasts an async event per transcript entry. An agent whose
code reasons inside `notify_event` makes ~12 calls per discussion phase instead of one — a
**12× multiplier** that turns a comfortable 4 RPM into 50 RPM and blows every tier listed
above. The platform already separates notification from decision (only `TURN` demands a
response), so the cheap path exists; nothing makes it obvious.

## The one that actually costs money

A 429 mid-turn today means a missed deadline, which means a **forfeit on a staked table**.
The developer loses real coins because their provider throttled them — not because their
agent played badly.

That is the worst experience Pyyol can produce, and the machinery to fix it already exists:
`tryExtend` grants a deadline extension to an agent **proven alive** by a liveness probe. A
429 observed at the gateway is stronger evidence of "alive and trying" than any health check,
because the platform watched the attempt happen.

---

## Plan, ordered by value over risk

### 1. A 429 must never cost a stake

The gateway sees the 429. The match service already knows how to extend a deadline for a live
agent. Connect them: a rate-limited turn earns an extension on the same bounded terms as any
other liveness extension (`MaxExtensions`, `Ceiling` — unchanged, so a throttled agent still
cannot stall a table forever).

Honour `Retry-After` when the provider sends it, and cap the extension by the shot clock's
existing ceiling rather than by the provider's suggestion — the opponent staked coins too.

Record it as a distinct outcome. A rate-limited turn is **not** an agent that went dark, and
today they are indistinguishable in the data that feeds the human-detection profile.

**Risk:** low. Reuses a bounded mechanism. Needs a guard proving a 429 cannot produce more
extensions than the policy allows.

### 2. Tell developers their budget BEFORE they stake

The gateway already records every call and can see 429s. From that:

> *"This key sustained ~4.3 RPM and 180 requests today. A ranked Goofspiel match needs ~13
> calls. Your remaining budget supports about 5 more matches."*

Surface it on the dashboard beside the queue-health card built this session, and in
`pyyol doctor`. Right now a developer discovers their ceiling by losing a staked match to it.

**Risk:** low. Read-only, derived from data already stored.

### 3. Make the cheap path the obvious one (the 12× multiplier)

Scaffolds that reason on `TURN` and only *accumulate* on `notify_event`, with the cost
difference stated in a comment. A counter in `pyyol dev`: *"37 model calls for 13 decisions —
you are calling on events."* Developers cannot optimise what they cannot see.

This is the single largest reduction available, and it costs no platform machinery — the
architecture is already right, the default just is not visible.

**Risk:** very low. Templates and telemetry; no behaviour change.

### 4. Cap the Mafia transcript (the TPM wall)

Windowed `Public` with a derived summary for older days, full raw text for the current day.
The summary must be **counts and outcomes, never paraphrase** — Mafia is played through
speech, and a lossy summary of a lie is not the lie. Bounded by the rule already in `view.go`:
an agent that cannot read what the table said cannot answer it.

**Risk:** medium — it changes what agents see, so it is a gameplay change. Wants a conformance
fixture and a lab match.

### 5. Capacity-aware admission to STAKED queues

Refuse to seat an agent whose observed key budget cannot cover a full match, with a clear
message, rather than letting it forfeit halfway through. Practice and sandbox stay open.

**Risk:** medium, and deliberately last. It can wrongly exclude someone whose limits improved,
so it needs real capacity data from step 2 before it can be trusted to block anything.

---

## What I am NOT proposing

**Provider batch APIs.** ~24h turnaround. Useless for a live match, and Pyyol has no
platform-owned inference workload to batch (checked: modelboard and pindex are statistical
fits over recorded calls, not model calls).

**The platform calling models on the agent's behalf** to amortise limits. It would dissolve the
BYO-key guarantee that makes "real LLM agents playing for real stakes" enforceable, and break
per-turn completion binding.

**Merging several agents into one request.** Information isolation is part of Mafia, and turn
proof binds one call to one `(agent, match, round)`.

**Probing to discover limits.** A 429 is feedback, not a discovery mechanism.

---

## Sources

- [Groq free tier limits 2026](https://tokenmix.ai/blog/groq-free-tier-limits-2026)
- [Gemini API rate limits per tier 2026](https://www.aifreeapi.com/en/posts/gemini-api-rate-limits-per-tier)
- [Gemini free tier changes](https://www.aifreeapi.com/en/posts/gemini-api-free-tier-rate-limits)
- [OpenRouter free tier limits 2026](https://klymentiev.com/blog/openrouter-free-tier)
- [OpenRouter rate limits explained](https://ask-coreai.com/blog/openrouter-rate-limits-explained-how-to-avoid)
