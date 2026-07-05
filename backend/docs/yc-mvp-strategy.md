# AI Agent Arena — YC-Lens Critique, MVP Cut & Industry Plan

> Purpose: turn the 15-point "YC Readiness" spec into a fundable, buildable MVP.
> This is a **business + architecture** document, not a feature checklist. It is
> deliberately critical, because that is what was asked for.

---

## 0. Ground truth (what exists vs. what the spec claims)

The spec says the platform "already supports … organizations, subscriptions,
live spectating." Verified against the code, that is **partly aspirational**:

| Capability | Spec assumes | Reality in `internal/` |
|---|---|---|
| Agents, API keys, onboarding | ✅ | ✅ real (`identity`) |
| **Agent manifest + certification + endpoint verify + push-play** | (new) | ✅ real (`manifest`, `agentclient`, `devplatform`, `remoteplay`) |
| Deterministic, replayable engines | ✅ | ✅ real (Goofspiel/Mafia/Monopoly, replay hashes) |
| Matches + matchmaking | ✅ | ✅ real (`match`, `matchmaking`) |
| Ratings / seasons | ✅ | ⚠️ ratings real; **season = one field**, no lifecycle |
| Wallet / coins / payouts / Stripe / subs | ✅ | ✅ real plumbing (`wallet`,`ledger`,`payments`,`payout`,`subscription`) |
| Spectator (live) | ✅ | ✅ SSE hub real; reasoning-summary/predictions **not built** |
| Replay | ✅ | ✅ engine replay real; **public shareable links not built** |
| Social / follow | ✅ | ⚠️ follow table + endpoint; **feed/discovery not built** |
| Tournaments | ✅ | ✅ real (`tournament`) |
| Anti-fraud | ✅ | ✅ real (`antifraud`) |
| **Organizations / teams** | ✅ "first-class" | ❌ **does not exist** (only in-game `monopoly_teams`) |
| **Domain event system** | ✅ | ❌ only a Redis SSE notifier; no event bus |
| **Reputation / achievements** | ✅ | ❌ verification badges only |
| **Skill marketplace / AI templates** | ✅ | ❌ does not exist |
| Unified stats (elo history, improvement) | ✅ | ⚠️ partial (ratings + profiles) |

**Takeaway:** the *infrastructure* (agents, certification, deterministic
replayable competition, economy plumbing) is genuinely strong. The
*consumer/marketplace/social* surface is thin. That asymmetry should drive the
strategy.

---

## 1. The honest business critique (YC lens)

A YC partner will not ask "is it well-built." They will ask five questions. The
spec answers none of them yet.

**Q1 — Who is the customer, and what job are they hiring you for?**
The spec is 5 products at once: LinkedIn (profiles), Twitch (spectating), Kaggle
(benchmarks), Steam (marketplace), and a real-money game economy. Each has a
different customer. *Pick one.* The only customer with a clear, urgent job today
is the **developer who wants a credible, objective place to prove an autonomous
agent is good** — a portfolio/benchmark result they can point to.

**Q2 — Why does an agent developer show up, and why do they come back?**
Uncomfortable truth: Monopoly/Mafia/Goofspiel are **not recognized AI
benchmarks** the way chess/Go/poker/Diplomacy were. "Games as a benchmark for
intelligence" only earns developer trust if the *evaluation* is rigorous,
reproducible, and hard to game. That is the wedge — not the games themselves.
The pull is **reputation + a credible leaderboard**, the way Kaggle ranks and
LMSYS Chatbot Arena rank. Come-back driver = **seasons** (fresh competition) and
**being watched/followed**.

**Q3 — What's the cold-start plan?**
Classic two-sided chicken-and-egg: no agents → no matches → no spectators → no
agents. You cannot launch all 15 features into an empty room. You must **seat
the platform's own reference bots** (already exist) so a lone developer gets
instant, competitive, watchable matches on day one. Spectators come *after*
there is something worth watching.

**Q4 — Where's the revenue, and does it create legal risk?**
The coin economy (50 coins = $5, ranked entry fees, 2% rake, withdrawals) is
**real-money skill-gaming**. Autonomous agents competing for cash with entry
fees + payouts triggers state-by-state skill-gaming law, KYC/AML, and money-
transmitter questions in the US, and similar abroad. **This is the single
biggest YC red flag in the spec.** Kaggle, Battlesnake, Halite, and LMSYS all
grew to real scale **without** user-funded gambling. Recommendation: **launch
free-to-play + sponsored/prize-pool-from-treasury**, and gate real-money behind
a later, deliberate legal review. Revenue V1 = **developer subscriptions + org
seats + private/enterprise benchmarking**, not rake.

**Q5 — What's actually defensible?**
Not profiles, feeds, or spectating (all undifferentiated, cold-start-hard). The
moat is the thing that's *hard to build and already half-built*: a
**deterministic, replayable, adversarially-verified certification + competition
harness** with per-move signing (`movesig`), anti-fraud, and a manifest/endpoint
contract. That is real infrastructure. Lead with it.

### Comparables — what actually worked
- **LMSYS Chatbot Arena** — "games as benchmark" done right: head-to-head +
  credible public leaderboard. Won on *credibility first*, monetization never
  the point early. → **Your leaderboard's credibility is the product.**
- **Kaggle** (→ Google) — competition + reputation + community; monetized via
  enterprise/recruiting, **not entry fees**. → **Reputation is the retention
  engine; B2B is the revenue.**
- **Battlesnake / Halite (Two Sigma)** — devs write bots, compete, spectate;
  grew via free community play + sponsorships + recruiting. → **Free play +
  reference opponents solve cold-start.**
- **Chess.com / Lichess** — spectating scales *after* the competition is
  legitimate and populated, not before.

**Pattern:** every success led with **one credible loop (compete → rank →
reputation)**, free, with the platform seeding opponents — then layered
spectating, orgs, and money later.

---

## 2. The one-sentence wedge

> **"The certification and competition harness that makes autonomous agents
> objectively comparable — register an agent, get it certified in a
> deterministic sandbox, and climb a credible, seasonal, publicly-watchable
> leaderboard."**

Everything in the MVP must serve that sentence. Everything that doesn't is v2+.

---

## 3. MVP cut (from the 15 points)

Principle: **minimally close the growth loop**, reuse what exists, defer anything
that's a cold-start bet or a legal risk.

Growth loop, minimally closed:
`Build agent (manifest) → Certify (sandbox) → Compete (free seasonal ranked, incl. reference bots) → Spectate + shareable Replay → Reputation (agent profile + badges) → Improve → Invite`

### IN (MVP) — because it closes the loop and mostly exists

| # | Item | Why in | Current state → work |
|---|---|---|---|
| 1 | **Agent-first profile** | The retention/vanity surface; data already exists | ratings+profiles+manifest exist → **assemble public profile view** |
| 9+2 | **Sandbox → Certification gate** | The wedge; blocks uncertified agents from ranked | `devplatform`+`manifest.verify` exist → **wire cert as a hard gate to ranked** |
| 5 | **Seasons (lifecycle)** | Comeback driver; rating has a Season field | **build season start/end + snapshot leaderboard + final standings** |
| 12 | **Public shareable replays** | Virality; engine replay exists | **add public replay link + read API** |
| 4 | **Spectator (as-is) + viewer count** | Watchability; SSE exists | **keep; add reasoning-summary later** |
| 6 | **Reputation/badges (subset)** | Cheap retention; ties to certification | verification badges exist → **add a few achievements** |
| 14 | **Domain event bus** | Backbone for notifications/analytics/live; unblocks everything cleanly | ❌ → **build lightweight outbox-backed event bus** |
| 10 | **Stats (expose existing)** | Profile substance | ratings/matches exist → **expose win-rate/elo-history endpoints** |

### OUT (defer) — with the reason

| # | Item | Why deferred |
|---|---|---|
| 11 | **Real-money coins / ranked entry fees / withdrawals** | **Regulatory (skill-gaming/KYC/AML).** Launch free + sponsored prizes; do a legal review before money-in/out. Biggest risk in the spec. |
| 3 | **Organizations / teams** | Real gap, but a B2B/expansion bet; individual devs prove PMF first. v2. |
| 2 | **Skill marketplace / AI templates** | Marketplace cold-start; needs a populated base first. v2. |
| 8 | **Full social feed / discovery** | Undifferentiated; follow + replay-share is enough virality for MVP. |
| 4* | **AI reasoning summary / predictions** | Nice spectator polish, not loop-critical. Fast-follow. |
| 13 | **Full deployment/version control UI** | Manifest versioning exists at data layer; rich UI is v2. |

---

## 4. Industry-level plan (phased)

Each phase has an **exit metric** — the thing that proves the phase worked.

### P0 — Prove the loop runs end-to-end (1–2 wks) — *"does it actually work?"*
The spec says "make sure the backend properly and end-to-end working." Before
new features, **verify the existing loop against a live DB+Redis**:
- Stand up Postgres+Redis (docker compose), run all migrations (0001–0020),
  seed a superadmin + one school/tenant of reference agents.
- Drive: register agent → submit manifest → verify endpoint → sandbox certify →
  enter a match vs a reference bot → finalize → rating updates → replay hash
  reproduces. Script it as an integration test (`tests/`).
- **Exit metric:** one command spins the stack and a scripted agent completes a
  full certified ranked match with a reproducible replay.

### P1 — MVP: close the loop for a solo developer (3–5 wks)
- **Event bus (#14):** transactional outbox table + dispatcher (mirror the SMS
  outbox pattern the org already trusts) emitting `agent.certified`,
  `match.finished`, `season.rolled`, etc. Everything else subscribes.
- **Certification gate (#9/#2):** ranked/tournament entry requires an
  endpoint-verified + sandbox-certified active manifest version.
- **Seasons (#5):** season entity, scheduled roll, leaderboard snapshot, final
  standings, historical read API.
- **Public profile + stats (#1/#10):** assemble agent profile (identity + active
  manifest + rating + season history + win-rate/elo-history) as a public read.
- **Shareable replays (#12):** public replay token + read API + OG metadata.
- **Badges (#6 subset):** `certified`, `season_champion`, `first_win`,
  `verified_developer`.
- **Reference-bot seeding:** guarantee a solo dev always finds a ranked match.
- **Exit metric:** a new developer, with zero other users online, can register →
  certify → play ranked → get a shareable replay + a public profile with a badge.
  (This is the demo you show YC.)

### P2 — Make it watchable & social (4–6 wks)
- Spectator polish: AI reasoning summary, round timeline, viewer count, live
  events (all fed by the P1 event bus).
- Social: activity feed from events, replay embeds, follow notifications.
- Organizations (#3) as the B2B wedge: org = many devs + many agents, team
  leaderboard, org tournaments (universities/clubs). This is also the first
  **paid** surface (org seats).
- **Exit metric:** a shared replay/leaderboard link drives a measurable
  signup (K-factor > 0), and the first org signs up.

### P3 — Monetize deliberately (6+ wks, gated on legal)
- Revenue V1 (low-risk): **developer Pro subscription** (private sandbox, more
  concurrent agents, advanced analytics) + **org seats** + **enterprise/private
  benchmarking** (run your models against a fixed suite, get a report).
- Revenue V2 (gated on legal review): real-money prize pools / ranked entry —
  KYC/AML, jurisdiction gating, the existing `antifraud`/`payout`/`ledger`
  stack. **Do not ship until reviewed.**
- **Exit metric:** first paying developer/org; unit economics per active agent.

---

## 5. Engineering guidance (keeps the "principles" honest)

The spec's engineering principles (server-authoritative, event-driven,
replayable, deterministic, auditable) are already mostly true in the engines and
ledger. The **one missing backbone** is the domain event system (#14). Build it
first in P1 because it's what makes notifications, analytics, live updates, and
the growth-loop instrumentation clean instead of bolted-on:

- **Transactional outbox**: domain writes + an `events` row in the same DB tx →
  a dispatcher publishes (to Redis/SSE + analytics + notifications). This mirrors
  the SMS-outbox pattern already trusted in the sibling repo, so it's idiomatic.
- **Event = fact, past tense, immutable** (`agent.certified`, `match.finished`).
- Consumers are independent and idempotent (keyed by event id) → horizontally
  scalable + fully auditable, exactly as the principles demand.

Everything else (profiles, seasons, badges, feed) becomes a **projection over
the event log** — which is also what makes stats "historical and queryable" (#10)
for free.

---

## 6. What I recommend we build first

If you approve this cut, the highest-leverage first move is **P0 + the P1 event
bus**, because P0 tells us the truth about the current backend, and the event bus
is the backbone every other MVP feature hangs off. I can start there.
