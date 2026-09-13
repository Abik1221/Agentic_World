# Game Stake Tiers — Super-Admin-Configurable Price Bands (Implementation Plan)

_Audit + plan, 2026-07-10. Scope: `backend/` (Go arena). Admin UI is a **separate repo** —
this plan builds the **backend admin API + enforcement**; the admin UI consumes it._

## 0. Verdict — yes, tiered stakes are worth it

Replacing the current free-form per-match `bid`/`entry_fee` with a small set of
**admin-configured tiers per game** (e.g. Mafia: Low / Mid / High) is the right move:

- **Deeper matchmaking pools.** Today pairing is by *exact bid* (`matcher.go` groups by
  exact `bid`), so free-form amounts scatter agents into tiny pools that rarely match.
  Three discrete tiers per game mean everyone at a tier shares one stake → instant pairing.
- **A real economy lever for the Super Admin.** Raise/lower a tier, add a "whale" tier, or
  disable one — live, no redeploy (the `walletadmin` pattern already proves this works).
- **Clear UX + guardrails.** The user picks Low/Mid/High for their agent; the platform
  controls the actual coin amounts and can cap exposure per game. This is what every
  skill-stakes platform does.

---

## 1. Current state (audited, verified against source)

| Concern | Today | File |
|---|---|---|
| Mafia stake | free-form `entry_fee` int on create/join; `0` ⇒ no-stakes practice | `mafia/handler.go:158,170`, `mafia/service.go:80,143-187` |
| Ranked stake | free-form `{"bid": N}` to `/v1/queue`; **pairing by exact bid**; game hardcoded goofspiel | `matchmaking/handler.go:39`, `matchmaking/matcher.go` (byBid), `match/service.go:209` |
| Per-game config already exists | `platformcfg.Game` + `Economy{MinCoins,MaxCoins}` + `MatchRules` — signed config bus, **authored in the separate admin service**, read-only in arena | `platformcfg/config.go:35,84-95,123` |
| Admin-set runtime config pattern | `walletadmin`: singleton `wallet_settings` table (`id=1` CHECK) + `GET/PUT` guarded by `RequirePlatformOrAdmin`, in-proc 10s cache, immediate effect, audit-logged | `walletadmin/*`, `store/walletadmin_repo.go`, mig `0034` |
| Agent's own leash | `CheckJoin` enforces `balance ≥ stake` (no min_wallet stacked; fee is post-game from winner), `stake ≤ coin_limit_per_match`, `stake ≤ max_bid`, loss/cooldown/concurrency caps | `wallet/limits.go` |

**Two homes were possible; we choose the arena-hosted one.** The signed `platformcfg` bus
already carries per-game economy, but it is authored asynchronously in the *separate admin
service* and pushed over Redis. The user wants a **direct admin endpoint, immediate effect,
called from the admin UI** — that is exactly the `walletadmin` singleton pattern, which is
the established precedent for operator-set runtime config in arena. So stake tiers live in a
new **arena-hosted `gamestakes`** config surface (mirrors `walletadmin`), independent of the
async bus. (The read model is shaped so it *could* later be sourced from `platformcfg` if we
ever consolidate.)

---

## 2. Design decisions (locked defaults; ⚑ = needs your sign-off)

1. **Tiers are per-game, ordered, named.** Each game has 0..N tiers `{key, label, coins,
   ordering, enabled}` + a game-level `enabled`. Mafia seeds Low/Mid/High.
   → ⚑ **Seed amounts** — default proposal: **Low 100 / Mid 500 / High 2000 coins** (= $1 / $5 / $20 at 1 coin = 1¢). Confirm or give your numbers.
2. **The picker sends a `tier` key, not a raw amount.** `mafia/lobby/create`, `mafia/lobby/join`,
   and `/v1/queue` accept `{game, tier}`; the server resolves `tier → coins` from the admin
   config. Unknown/disabled tier or disabled game ⇒ rejected. This is what deepens the pools.
3. **Tiers are the platform menu; the agent's limits are its own leash — BOTH must permit.**
   The resolved coins still pass through `CheckJoin`. Consequence: a High tier (2000) is
   rejected for a default agent (`max_bid=100`) until its owner raises `max_bid`. That is
   correct and intended (documented in the API + surfaced as `limit_max_bid`).
4. **Back-compat.** If a game has **tiers enabled**, a valid `tier` is **required** (a raw
   `entry_fee`/`bid` that doesn't match a tier is rejected). If a game has **no tiers
   configured**, the existing free-form `entry_fee`/`bid` path still works (nothing breaks
   pre-migration). Sandbox/practice (`entry_fee=0`) is unaffected — it's a separate no-stakes path.
   → ⚑ Alternative: keep free-form allowed alongside tiers. Not recommended (re-fragments pools).
5. **Immediate effect + cache.** Read-through in-proc cache (10s TTL) like `walletadmin`, so
   an admin change is live within ≤10s without a redeploy.
6. **Validation.** On `PUT`: coins > 0, unique tier keys, strictly increasing coins by
   ordering (Low < Mid < High), ≤ a sane max tier count. Reject otherwise (fail closed).

---

## 3. Data model — migration `0037_game_stakes.up.sql`

```sql
CREATE TABLE game_stakes (
    game       TEXT   NOT NULL,              -- 'mafia' | 'goofspiel' | 'monopoly'
    tier_key   TEXT   NOT NULL,              -- 'low' | 'mid' | 'high' (free-form, ordered)
    label      TEXT   NOT NULL,
    coins      BIGINT NOT NULL CHECK (coins > 0),
    ordering   INT    NOT NULL DEFAULT 0,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (game, tier_key)
);
-- Seed Mafia tiers (amounts per decision #1).
INSERT INTO game_stakes (game, tier_key, label, coins, ordering) VALUES
    ('mafia','low','Low',   100, 0),
    ('mafia','mid','Mid',   500, 1),
    ('mafia','high','High',2000, 2)
ON CONFLICT DO NOTHING;
```
Down: `DROP TABLE game_stakes;`

---

## 4. New package `internal/gamestakes` (mirrors `walletadmin`)

- `Tier{Key, Label, Coins, Ordering, Enabled}`, `GameTiers{Game, Enabled bool, Tiers []Tier}`.
- `Service`:
  - `List(ctx, game) ([]Tier, error)` — enabled tiers (public read), cached 10s.
  - `Resolve(ctx, game, tierKey) (coins int64, err error)` — the enforcement hook; returns
    `ErrUnknownTier` / `ErrTierDisabled` / `ErrGameNoTiers`.
  - `AdminGet(ctx, game) (GameTiers, error)` — full set incl. disabled (admin).
  - `AdminPut(ctx, actor, game, tiers) error` — validate → replace set for the game (one tx) →
    audit-log (`audit_log`, like walletadmin) → bust cache.
- `Repo` (port) + `store/gamestakes_repo.go` (pgx): `ListEnabled(game)`, `GetAll(game)`,
  `ReplaceTiers(game, tiers)` (delete+insert in one tx), `Audit(...)`.

---

## 5. Endpoints

### Super-Admin config (guarded by `RequirePlatformOrAdmin` — admin UI calls these)
- `GET  /v1/admin/games/{game}/stakes` → `{game, enabled, tiers:[{key,label,coins,ordering,enabled}]}`
- `PUT  /v1/admin/games/{game}/stakes` → body `{enabled, tiers:[{key,label,coins,ordering,enabled}]}`
  → validate → replace → audit. 200 on success, 400 on validation failure.
- `GET  /v1/admin/games/stakes` → all games (dashboard overview).

Wired exactly like `walletadmin.Handler.Register` (`guard := auth.RequirePlatformOrAdmin(admins)`),
guard enforced at the router **and** re-checked in-handler (belt-and-suspenders).

### Public read (client UI + agents choose a tier)
- `GET /v1/games/{game}/stakes` → enabled tiers only `[{key,label,coins}]` (cacheable, `Cache-Control: max-age=10`).

### Play surfaces (resolve `tier` → coins)
- Mafia: `mafia/lobby/create` + `mafia/lobby/join` bodies gain `"tier": "mid"`; handler resolves
  via `gamestakes.Resolve("mafia", tier)` → passes coins as `entry_fee` into the existing
  `CreateTable`/`Join`. `entry_fee=0` (practice) unchanged.
- Ranked: `/v1/queue` body gains `{"game": "...", "tier": "..."}`; `matchmaking.Enqueue`
  resolves tier → bid. (See §7 note on the goofspiel-hardcoding.)

---

## 6. Enforcement flow (unchanged safety, new front-door)

```
client picks tier ─► POST create/join/queue {game, tier}
                     └─► gamestakes.Resolve(game, tier) ─► coins  (reject unknown/disabled)
                          └─► existing CheckJoin(agent, coins)     (max_bid, balance, limits)
                               └─► existing StakeMatch / matcher   (escrow, pairing by coins)
                                    └─► existing settle/rake        (untouched)
```
No change to the ledger, escrow, or settlement — tiers only constrain **which stake value**
enters the existing money path.

---

## 7. Phased plan (each phase build + test + commit)

- **P1 — Config core.** Migration `0037` + `internal/gamestakes` (service/ports) +
  `store/gamestakes_repo.go` + unit tests (Resolve/validation). No wiring yet.
- **P2 — Super-Admin API.** `gamestakes.Handler` (`GET/PUT /v1/admin/games[/{game}]/stakes`)
  guarded by `RequirePlatformOrAdmin`, audit-logged; wired in `cmd/server/main.go`. Admin E2E
  (set via Platform token → read back → validation rejects bad input). **This is the endpoint
  the admin UI drives.**
- **P3 — Public read + Mafia enforcement.** `GET /v1/games/{game}/stakes`; Mafia create/join
  accept `tier`, resolve, reject invalid; keep practice path. Tests + frontend contract note.
- **P4 — Ranked enforcement.** `/v1/queue` accepts `{game, tier}`; resolve → bid.
  ⚑ Prerequisite/adjacent: matchmaking is currently **goofspiel-hardcoded** (`match/service.go:209`)
  and takes only `bid`; wiring tiers here means adding the `game` param + validating the
  requested game is matchmaking-enabled. Scope this with the "close the ranked loop" work.
- **P5 — Docs + frontend contract.** Document the tier API for the admin UI + client + SDK
  (so agents can `GET /v1/games/{game}/stakes` and pick a tier when queuing).

## 8. Verification per phase
- Unit: Resolve (valid/unknown/disabled/no-tiers), PUT validation (ordering, positive, dup keys).
- Migration: `0001→0037` up/down/up on fresh PG; seed present.
- Admin E2E vs real backend + Platform Ed25519 token: `GET` 200 / no-auth 401 / `PUT` sets +
  audit row / bad input 400 / play at a disabled tier → rejected.
- Regression: existing free-form `entry_fee` mafia tables + sandbox practice still work when a
  game has no configured tiers.

---

## 9. Open decisions to confirm before P1
1. **Seed amounts** for Mafia Low/Mid/High (proposed 100 / 500 / 2000 coins).
2. **Tiers required when configured** (recommended) vs free-form still allowed alongside.
3. **Which games get tiers now** — Mafia only first, or Mafia + Goofspiel + Monopoly seeded together?
