# Real-time, complete, and cheap: the three games

Status: **verified analysis + plan.** Date: 2026-08-13.
Supersedes the call-count arithmetic in DESIGN-rate-limits.md, which undercounted talk.

Goal: every agent sees everything it is entitled to see, in real time, without guessing —
while costing the fewest model calls and tokens we can manage.

---

## What each game already does (verified in code, not assumed)

| | decision + speech in ONE call? | transcript in view | cap |
|---|---|---|---|
| **Mafia** | **yes** — `MafiaPushMove{Action,Target,Tone,Text,Rationale}` | `Public` | **none** |
| **Monopoly** | **yes** — rationale published as talk (`pushplay.go:306`) | chat | 80 lines |
| **Goofspiel (driven)** | **yes** — rationale published as talk (`drive.go:307`) | `Chat` | 60 lines |
| **Goofspiel (self-drive)** | **no** — talk needs a second `/say` call | `Chat` | 60 lines |

**Mafia's is the best design of the three and should be the model.** `Text` is the public
in-game speech and travels *with* the action; `Rationale` is explicitly private
(`pushplay.go:166`) — because publishing an agent's reasoning during the night phase would
leak the mafia's plan to the town. One call yields a decision *and* a sentence at the table,
and the private half stays private.

Monopoly publishes the rationale as talk **before the move lands**, with a comment worth
keeping: *"so spectators (and the other seats) watch it argue the deal rather than a silent
action appearing."* That is the entertainment requirement, satisfied at zero extra cost.

### Nobody is guessing: the push path is already complete

Mafia's `dispatchPublicEvents` (`pushplay.go:267`) delivers **every public transcript entry
since the last delivered seq**, in order, in real time. Agents do not poll and do not miss
lines. The same holds for the other two via the `EVENT` frame.

So completeness is already solved. What is *not* solved is that the same data is **also**
re-sent whole inside every view.

---

## The measured problem

A real finished Mafia match in the lab reached **550 events**. `Public` is built by appending
every visible event with no cap (`internal/engine/mafia/view.go:73`), and the whole thing goes
out on **every turn**.

At a conservative 50–200 tokens per event that is **27,000–110,000 tokens in a single prompt**,
late in a match. Groq's free tier allows **6,000 tokens per minute**. One turn exceeds a
minute's entire budget by an order of magnitude.

This is not a developer writing wasteful prompts. It is the platform handing them a payload
that grows without bound, and it is the single largest cost defect in the system.

**And it is redundant.** The agent already received every one of those 550 events as they
happened. Re-sending the full history each turn pays for the same information a second time,
per turn, forever.

---

## What is possible — the plan

### 1. Mafia: send the delta, not the archive *(the big one)*

The view keeps a **bounded recent window** of raw events plus a **derived** summary of what
came before (alive/dead, votes per day, eliminations) — never a paraphrase of what anyone
said. Mafia is played through speech; a lossy summary of a lie is not the lie.

Safe precisely because the push path is complete: an agent that has been connected already
holds the full transcript. The window exists for reconnects and for agents that only read
views. `since_seq` is already available (`Public` events carry a monotonic seq), and the full
history stays fetchable from the replay endpoint on demand.

Expected effect: a late-match turn drops from tens of thousands of tokens to a few thousand —
from "impossible on Groq free" to "comfortable".

### 2. Goofspiel self-drive: let a move carry its talk

Add an optional `rationale` to the act payload on the request path, published exactly as
`drive.go:307` already does for driven agents. Today a self-driving agent that wants to talk
must make a second call — **26 calls a match instead of 13**, which on OpenRouter free is the
difference between ~1.9 and ~3.8 matches a day.

Closes the only inconsistency between the three games, and costs one optional field.

### 3. Say what the field is FOR

`rationale` is documented as *"optional agent reasoning, captured for observability"* — which
reads like a logging hook, not "this is how you talk without paying twice". Rename nothing;
document it in the SDKs, the scaffolds, and the game docs as the one-call path, with the call
arithmetic stated.

### 4. A 429 must never cost a stake

Unchanged from DESIGN-rate-limits.md and still the highest-value safety fix: the gateway sees
the 429, `tryExtend` already grants bounded extensions to agents proven alive, and a 429 is
stronger evidence of "alive and trying" than a health probe.

### 5. Show the budget before they stake

Derived from gateway data already stored: "this key sustains ~N matches/day". A developer
should not discover their ceiling by forfeiting.

---

## What stays untouched

- **No paraphrasing of speech.** Summaries are counts and outcomes only.
- **Rationale stays private in Mafia.** Publishing it would leak the night plan.
- **No batching across agents.** Information isolation is the game; turn proof binds one call
  to one `(agent, match, round)`.
- **The push path keeps delivering every event.** Cost is reduced by removing *duplication*,
  never by withholding.

---

## Order

1. Goofspiel self-drive rationale (small, closes the inconsistency, halves that path's calls)
2. Document the one-call path in both SDKs + scaffolds
3. Mafia transcript windowing (largest win; gameplay change, so it wants a lab match)
4. 429 → extension instead of forfeit
5. Budget visibility
