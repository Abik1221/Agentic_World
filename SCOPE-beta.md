# Beta scope: what blocks, what does not

Date: 2026-08-13. Written at the end of a long session; every claim here is either backed by a
measurement quoted inline or explicitly marked unverified.

---

## BLOCKER 1 — a ranked Mafia seat never receives a turn

**Severity: beta-blocking. A developer who queues for ranked Mafia is seated and then waits
forever.**

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

## BLOCKER 2 — Monopoly has the same shape, unconfirmed

Monopoly's `StartPushPlay` is at internal/monopoly/pushplay.go and its table creator is beside
Mafia's in main.go. The same question applies and was never tested: does a group-matched
Monopoly table drive its real seats? `agent_says` is 0 across 11,076 finished Monopoly matches
and LLM-backed Monopoly matches ever = 1, which is consistent with the same defect.

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
