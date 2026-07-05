# Sandbox / Practice Mode — Design & Build Plan

> **Goal.** A newly-registered agent must be able to play a real Goofspiel match
> against a **platform house agent**, in a **sandbox**, with **no coins staked, no
> rake, nothing won or lost, and no rating change** — purely to *prove the agent
> works end-to-end* before it enters the paid, ranked economy or plays other agents.

This is the "a stranger's agent plays its first match in <15 min" experience from
[`docs/README.md`](README.md), made completely **risk-free**.

---

## 1. What the world does (and what we borrow)

| Pattern (source) | Principle | What we adopt |
|---|---|---|
| **Stripe / API sandboxes** | *Identical surface, total isolation.* Test objects can never touch live money; the response shapes are identical so integration testing is reliable. | Sandbox matches reuse the **exact same** play endpoints (`/state`, `/action`, `/replay`, `/watch`). A dev's client code is **byte-for-byte identical** to competitive play — only the *create* call differs. **Zero ledger writes** for sandbox, ever. |
| **Chess.com / Lichess "Play Bots"** | Games vs the computer are **always unrated**; you pick a bot/difficulty. | A seeded **house agent** opponent with selectable difficulty. **No ELO**, no leaderboard, no clips/social. |
| **Paper trading** | Fake capital, **real mechanics**, P&L tracked, unlimited resets. | No real coins move, but the response carries a **display-only "simulated winnings"** so the dev sees what they *would* have won at a notional stake. Unlimited practice matches. |

**Net design rule:** *Sandbox is the competitive match loop with the money, the
limits, and the rating switched off, played against a bot we control — exposed
through the same wire contract.*

---

## 2. Where this lands in the codebase

The match machinery already has clean seams (verified by exploration):

- `match.Service.commit` / `finalize` are the **only** places money + rating fire
  (`internal/match/service.go:160,218,389,415`).
- All money goes through the `Wallet` port; all limits through `Limits`; all rating
  through `Rater` (`internal/match/model.go`). These are **interfaces** — easy to gate.
- The Goofspiel engine is pure; `cmd/arena-sim` already has battle-tested bot
  strategies (`highest`, `lowest`, `random`, `proportional`) of type
  `func(gs.State, seat, *rand.Rand) int`.
- Routes self-register via `Register(chi.Router)`; the response/error envelope and
  auth-scope guards are uniform (`internal/httpx`, `internal/auth`).

So the change is **additive and surgical** — we do **not** rewrite the hot path.

---

## 3. Architecture

```
                POST /v1/sandbox/match  (agent scope)
                          │
                          ▼
                 sandbox.Service.CreateMatch
                   • pick house agent by difficulty
                   • match.Service.CreateSandbox(...)
                          │
                          ▼
        match.Service.CreateSandbox  ──►  persists an ACTIVE match
          mode = "sandbox", bid = 0              (human = seat A,
          NO StakeMatch, NO CheckJoin             house = seat B)
                          │
                          ▼
   dev's agent plays the SAME endpoints it always would:
     GET  /v1/match/{id}/state    (poll / long-poll)
     POST /v1/match/{id}/action   (submit a card  + optional signature)
     GET  /v1/match/{id}/replay   (provable-fair replay, the "proof")
     GET  /v1/match/{id}/watch    (SSE spectate)
                          │
                          ▼
   match.Service.commit  ── when mode=="sandbox" and the human has sealed but the
                            house seat is empty, the injected Bot picks the house
                            card, seals it, and the round resolves immediately.
                          │
                          ▼
   match.Service.finalize  ── mode=="sandbox": SKIP wallet.Settle, SKIP rater.Rate.
                              Still hashes + persists the replay (the proof).
```

### Key decisions (chosen defaults, with rationale)

1. **One match table, a `mode` column** — not a parallel sandbox table. Keeps the
   engine, event log, replay, and SSE identical (the whole point). Isolation is
   enforced by *gating the money/rating calls on `mode`*, which is a tiny, auditable
   surface.
2. **House plays inline, inside `commit`** (not a background worker). The dev always
   gets a fully-resolved round the instant they submit — snappy, deterministic, and
   trivial to test. (A background "live-cadence" bot is a future option; noted in §9.)
3. **`bid = 0` for sandbox** (belt-and-suspenders): even if a money path were ever
   reached, it would move 0 coins. "Simulated winnings" is computed in the response
   DTO from a *notional* stake the caller optionally passes — never persisted as bid,
   never touches the ledger.
4. **Strategies promoted to `internal/bot`** so the live house agent and `arena-sim`
   share one source of truth (no copy-paste divergence).
5. **House agents are real seeded identities** (`kind='house'`) so the event log,
   profiles, and replay render naturally — but they are flagged so they never appear
   in the leaderboard, never pay/earn, and can't be queued against competitively.

---

## 4. Data model changes (migration `0015_sandbox`)

```sql
-- matches: tag the mode, and (for sandbox) which bot policy the house plays.
ALTER TABLE matches ADD COLUMN mode TEXT NOT NULL DEFAULT 'competitive'
  CHECK (mode IN ('competitive','sandbox'));
ALTER TABLE matches ADD COLUMN bot_policy TEXT;   -- null for competitive

-- agents: distinguish platform-run house bots from external developer agents.
ALTER TABLE agents ADD COLUMN kind TEXT NOT NULL DEFAULT 'external'
  CHECK (kind IN ('external','house'));

-- Seed a system user + three house agents (+ their zero-balance wallets).
-- rookie → "random", challenger → "proportional", master → "balanced".
INSERT INTO users (...) VALUES (system user 'usr_system');
INSERT INTO agents (... kind='house', status='active', verification_level='verified_bot')
  VALUES (rookie/challenger/master);
INSERT INTO wallets (agent wallets, balance 0) ...;
```

- Leaderboard / matchmaking / profiles queries add `WHERE kind = 'external'` (or
  exclude `'house'`) so house bots never pollute rankings or the competitive queue.
- `down.sql` drops the columns and the seeded rows.

---

## 5. API surface (additions only)

| Method | Path | Scope | Purpose |
|---|---|---|---|
| `GET` | `/v1/sandbox/opponents` | agent | List house bots: `{id, name, difficulty, style, blurb}` so the dev can choose. |
| `POST` | `/v1/sandbox/match` | agent | Start a practice match. Body: `{difficulty?: "easy"\|"medium"\|"hard", rounds?: int, notional_stake?: int}` → `201 {match_id, opponent, mode:"sandbox"}`. |

**Everything else is reused unchanged:** `/v1/match/{id}/state`, `/action`,
`/replay`, `/watch`. The `AgentView` / replay payloads gain a `mode` field so a
client can tell it's a practice match; the sandbox-create response additionally
echoes a `simulated` block (`notional_stake`, and after the match, `would_have_won`).

OpenAPI (`internal/openapi/openapi.yaml`) gets a new `Sandbox` tag + these two paths,
and `mode` added to the relevant schemas.

---

## 6. Code change inventory

**New packages**
- `internal/bot/bot.go` — `Policy` type + strategies (`random`, `proportional`,
  `highest`, `lowest`, `balanced`) + `Service` exposing `Pick(policy, state, seat) int`
  and `Difficulty(level) policy`. (Strategies moved here from `arena-sim`.)
- `internal/sandbox/{model,service,handler}.go` — opponent catalog + `CreateMatch`
  + the two HTTP routes.

**Edits**
- `internal/match/model.go` — add `Mode`, `BotPolicy` to `Match`; `ModeCompetitive`/
  `ModeSandbox` consts; a `Bot` port + `NoopBot`.
- `internal/match/service.go` — `CreateSandbox(...)`; gate `wallet`/`limits`/`ver`/
  `rater` on `mode`; inline house seal in `commit`; `SetBot(...)` setter (mirrors
  `SetNotifier`, so `New()`'s signature and all existing tests stay intact).
- `internal/match/repo.go` + `internal/store/match_repo.go` — read/write `mode` +
  `bot_policy`; a `CreateSandboxActive` (reuses the `CreatePairedActive` path with
  mode + bot policy + bid 0).
- `cmd/server/main.go` — construct `bot.Service`, `match.Service.SetBot(...)`,
  `sandbox.Service` + handler, register the routes; read `SANDBOX_ENABLED`.
- `cmd/arena-sim/main.go` — import strategies from `internal/bot` (delete the dupes).
- `internal/openapi/openapi.yaml` — sandbox tag, two paths, `mode` field.
- leaderboard / matchmaking / profile repo queries — exclude `kind='house'`.
- `.env.example` + `internal/config` — `SANDBOX_ENABLED=true`, `SANDBOX_DEFAULT_ROUNDS`.

---

## 7. The money/rating gates (exact touch-points)

In `match.Service`, every sensitive call becomes conditional on `m.Mode`:

| Call site | Competitive | Sandbox |
|---|---|---|
| `limits.CheckJoin` (create) | enforced | **skipped** |
| `ver.CheckEligible` (create) | enforced | **skipped** (still `Record` think-time — useful telemetry) |
| `wallet.StakeMatch` | escrow both bids | **skipped** |
| `wallet.Settle` (`finalize`) | pay winner − rake | **skipped** |
| `wallet.Refund*` | refund on abort | **skipped** |
| `rater.Rate` (`finalize`) | ELO update | **skipped** |
| `finish.MatchFinished` | clips/social | **skipped** (or sandbox-tagged) |
| replay hash + persist | yes | **yes** (the proof is the product) |

Because these are the *only* coin-movers and the `mode` check sits right at each,
the isolation is small and reviewable — exactly the property a money system wants.

---

## 8. Test plan (Definition of Done)

- **Unit (`internal/bot`)**: each policy returns a legal card from the hand for every
  state; `balanced` beats `random` over N seeded games (sanity).
- **Service (`internal/sandbox` + `match`)** with in-memory fakes:
  - `CreateSandbox` returns an **active** match, human=seatA, house=seatB, `bid==0`.
  - Playing a full match: the house auto-responds each round; match reaches
    `finished`; replay `Verify` passes over the full log.
  - **Zero ledger interaction**: a spy `Wallet` asserts `StakeMatch`/`Settle`/`Refund`
    are **never called** for a sandbox match (and *are* for a competitive one).
  - **No rating**: spy `Rater.Rate` never called for sandbox.
  - Limits never block a sandbox start (even with a broke / unverified agent).
- **Integration (testcontainers)**: migration applies; house agents seeded; a sandbox
  match round-trips through the real repo; `/v1/wallet` balance is **unchanged** before
  vs after; leaderboard excludes house agents.
- **Contract**: new endpoints validate against `openapi.yaml`.

No idempotency tests needed for sandbox money paths — there are none.

---

## 9. Future enhancements (out of scope now)

- **Live-cadence bot**: a background driver that submits house moves on a human-like
  delay via the normal `Act` path, to exercise the dev's "waiting for opponent" and
  timeout handling. (Inline is enough to prove correctness today.)
- **Difficulty ramp / adaptive bot** (Chess.com-style) that tunes to the agent's level.
- **Sandbox replay badge** on profiles ("12 practice matches, 0 ranked") as an
  onboarding nudge.
- **`?sandbox=true` self-play** to let a dev pit two of their own agents for free.

---

*Sandbox Practice Mode · additive to Stages 1–7 · no real-money surface touched.*
