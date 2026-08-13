# Beta scope: what blocks, what does not

Date: 2026-08-13. Written at the end of a long session; every claim here is either backed by a
measurement quoted inline or explicitly marked unverified.

---

## BLOCKER 1 — a ranked Mafia seat never receives a turn — **FIXED AND VERIFIED (856d99b)**

Ranked Mafia: 6 seat decisions arrived, `staked_mafia_last30min=1`, and the free-practice
regression still passes (4 `mf_` tables, 8 decisions).

**Severity when open: beta-blocking. A developer who queued for ranked Mafia was seated and then
waited forever.**

`StartPushPlay` (internal/mafia/pushplay.go:182) is the ONLY code path that drives a real
agent's Mafia seat by POSTing turns to its endpoint. A group-matched table is created by
`mafiaTableCreator.CreateStartedTable` (cmd/server/main.go:2192) via `CreateTable` + `Join` +
`JoinHouseSeat`, which starts the match — and **nothing spawns a pusher for the seated agents.**

Evidence: a `-game mafia -tier low -bind` run funded all four seats, enqueued them, `group_queue`
showed them `matched`, 17 mafia matches were created, and **zero turns reached the agents'
/play endpoints**. Historical LLM-backed Mafia matches: 27, all of which must have come from the
free-practice `StartPushPlay` path rather than from ranked play.

**Fix shape:** whatever `StartPushPlay` does to attach the pusher must also happen when a table
is started by the group matcher. Goofspiel already has the equivalent — `match.maybeDrive`
spawns its driver on a freshly-paired match — so the pattern exists to copy.

**Do not** verify this by watching a match complete: a Mafia table with unresponsive real seats
still finishes, because the engine force-plays a seat that never acts. A forfeit and a decision
look identical on the wire. Verify by asserting turns ARRIVED (agent-side log, or
agent_model_calls rows for that match).

## BLOCKER 2 — Monopoly had the same shape — **FIXED AND VERIFIED (856d99b)**

Same commit, and now confirmed in the DATABASE rather than the run log. Two group-matched
tables, all four gamelab agents driven:

| match | agent | decisions | rounds | ok |
|---|---|---|---|---|
| mp_3uues4cllrfutvug | ag_4sj7r6wpon7nkagd | 2,294 | 0→999 | 2,038 |
| mp_3uues4cllrfutvug | ag_q7sir6xhx7vfo2it | 2,321 | 1→1000 | 2,065 |
| mp_tnuyunkhjpoanzth | ag_3i3xlexwuivjtm4d | 2,301 | 1→1000 | 2,045 |
| mp_tnuyunkhjpoanzth | ag_lakjltjiey43j64n | 2,246 | 0→999 | 1,990 |

The run log alone was NOT sufficient evidence and nearly misled me: the agents were serving
Mafia matches concurrently, and the only monopoly lines were `/initialize` acks. What made
those acks conclusive was their SHAPE — `ready_asker.go` sent `game:"goofspiel"` plus a
`deadline_ms`, and these carried `game:"monopoly"` with no deadline, so they could only have
come from the driver at `monopoly/pushplay.go:237`. The DB query is what actually settled it.

**Left open, not beta-blocking:** a ranked Monopoly match runs to the 1000-turn cap and writes
~4,600 decision rows. With instant lab agents that takes 16 seconds; with real agents thinking
3s it is ~100 minutes of wall clock. Worth a product decision on the turn cap before real
tables run long.

---

## Shipped this session, with the evidence

| change | commit | evidence |
|---|---|---|
| Ready-check chain, countdown, watch prompt | several | live lab runs |
| Phase warning (rule → view → both SDKs → Mafia → web rim) | 6f99e73, 9dac325, de48251, b48b969 | 8 unit tests + mutation proof |
| Queue telemetry (data, 2 APIs, 2 UIs) | a995711, c8c5e07, c8fe37b, 1dbf3d3, 2f97b76 | 4 event kinds live; funnel derives correctly |
| Adaptive window on every round + recorded round start | f150484, 6758982, 5771bac | think-time 19–915ms → 3.5–38s |
| Postgres guards for both SQL write paths | 423222b | mutation-proven (COALESCE→now() goes red) |
| 429 never costs a stake | 157c75b | 3 guards: bounded, round-scoped, fails closed |
| Goofspiel one-call talk + JS SDK parity | 8263fdc | **100 agent_says in 30 min** vs 3.8% of matches historically |
| Mafia transcript windowed at the payload boundary | ba30c0c | **47 kB → 3.7 kB per turn**, measured on the real 550-event match |
| Harness verified the wrong game (4 places) + Say lock + Monopoly window | 6ec0e63 | `-bind` refusal verified by running it |
| Mafia completion binding | ff3b096 | builds and vets; **never executed** (see BLOCKER 1) |

## Not blocking beta

- **One `match_busy` still appears under 4-agent contention.** The retry reduced it, did not
  eliminate it. Talk is best-effort and a lost line costs nothing but the line.
- **Monopoly game-end payload is 233 kB** (measured; I had guessed multi-MB). Within normal
  body limits.
- **SDK scaffolds still reason on events rather than turns** — the 12× call multiplier from
  DESIGN-rate-limits.md. Costs developers money on free tiers; breaks nothing.
- **Monopoly `-bind` is unwired** and refuses loudly rather than substituting Goofspiel.

## Deliberately dropped

- **Key-budget forecast.** Cannot be computed honestly: a BYO key is used outside Pyyol, Groq's
  limits are per-organisation, and reset windows are provider-specific. A confidently wrong
  capacity number is worse than none. Show observed 429s instead.

## The database was purged, and what it revealed

`agent_model_calls` was the keep rule — a match survives only if a model call was recorded
against it through the gateway. Result: **32,143 matches → 85. 33 GB → 131 MB.** Every kept
match still has its full history (spot-checked: 69 events, 26 decisions, 26 model calls — one
call per decision, as binding requires), and the purge asserted "no orphans" before COMMIT.

Done by staging the keep set, TRUNCATE, and restoring, rather than by DELETE: the keep set is
5,261 rows out of 50.8M in `match_events`, so a DELETE meant ~60M cascaded row versions, a
huge WAL and a VACUUM FULL afterwards. The cascade set was established by rehearsing
`TRUNCATE matches CASCADE` inside a rolled-back transaction — 11 children, all staged.

**Ratings, rank snapshots and developer_pindex were CLEARED, not preserved.** Every one was
fitted over the matches being deleted. A leaderboard whose numbers no surviving match can
explain is worse than an empty one; the workers recompute from what remains.

### Ready for the real-model batch — everything except the key

All three games are now completion-bindable end to end, verified through the lab stand-in:

| game | evidence |
|---|---|
| Goofspiel | bound every round; substituted move REJECTED (`agent_move_rejections`, `reason=completion_binding`) |
| Mafia | **93 bound, 0 failed** — the bind path's first execution ever (it previously "built and vetted, never executed") |
| Monopoly | **82 decisions, 20/14/9 bound** across three matches — the first Monopoly rows ever in `agent_match_bound_decisions` |

Groq is reachable from the backend container (`api.groq.com` returns 401 for a missing key, so
the network path and the upstream mapping both work). The gateway already has
`groq=https://api.groq.com/openai` in `LLM_GATEWAY_UPSTREAMS`.

The batch is one command per game once a key exists:

```bash
docker exec -e PYYOL_PROVIDER_KEY=gsk_… -e API_BASE=http://localhost:8080 -e AGENT_HOST=127.0.0.1 \
  pyyol-backend /src/.lab-gamelab -game goofspiel -tier low -bind \
  -bind-provider groq -bind-model llama-3.1-8b-instant
```

Pacing matters: Groq's free tier is 30 RPM / 6k TPM / 14.4k RPD. A Goofspiel match is ~26
calls, so a dozen matches per game is comfortably inside a day's budget; Monopoly is the one to
watch, since a match can run to the 1000-turn cap.

### The finding that blocks publishing benchmarks

| | calls | matches |
|---|---|---|
| reached a real upstream (groq, openrouter) | 140 | **6** |
| routed to the lab stand-in | 1,041 | 79 |

All 6 are Goofspiel. The other 79 are recorded as `anthropic / claude-opus-4` and similar but
**never left the machine** — `LLM_GATEWAY_UPSTREAMS` maps anthropic and openai to
`pyyol-toolprovider`. Those rows are honest about the gateway (a call was made and bound); they
are NOT evidence about a model, because no model was involved.

So: match data, P-Index and rejection/enforcement evidence are real and publishable. **Model
benchmarks are not**, and cannot be until a real provider key runs a batch across all three
games. Groq's free tier (14.4k requests/day) would cover it comfortably.

### Bot traffic regenerates immediately

Within 40 seconds of the backend restarting, matches went 85 → 96. The demo runner ticks every
2 seconds (`internal/bot/runner.go:47`). That is correct for a lab — the bots are what fill
tables so a real agent has opponents, and the min-seat policies depend on them. For a
production database it is the wrong default: **`DEMO_BOTS=false`** (cmd/server/main.go:1697).

## Owner decisions still outstanding

- Groq key for cross-model benchmarking
- GitHub Actions spending limit (blocks any deploy: 2,220/2,000 minutes, $0 limit)
- The coverage-fix merge
- Group-matchmaking product call
- Mafia lobby empty-state
- Migration 0089/0090 need a quiet window in production — 0089 failed here on a 5s
  lock_timeout behind an analytical scan and needed the backend stopped

## The lesson worth keeping

Four times this session a green build was wrong, and the database said so within minutes. The
worst case was the harness: `gamelab` accepted `-game mafia`, logged `game=mafia`, sized seats
for mafia, and ran **Goofspiel** — in four independent places. Every prior "verification" of two
of three games was therefore unfounded, including two I reported in this session before catching
it by reading round/prize/hand fields instead of the header.

**A verification tool that silently tests the wrong thing is worse than one that refuses.**
That is now enforced: `-bind` fails loudly on an unwired game.
