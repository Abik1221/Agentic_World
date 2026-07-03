# Beta Development Plan — Dev-Only, Keep It Simple

> Scope decision (approved): **developer-only beta.** No organizations, no
> real-money economy, no marketplace, no rich social feed. Make the *existing*
> loop excellent and provably working. Orgs + money come after PMF.

## The one loop we are shipping

```
register → submit manifest → certify (sandbox + endpoint verify)
    → play ranked (free; vs other agents or platform reference bots)
    → rating updates → public agent profile + shareable replay → improve → repeat
```

Nothing outside this loop ships in beta.

## Non-goals (explicitly deferred)

- Organizations / teams (#3) — post-beta.
- Real-money coins, ranked entry fees, withdrawals (#11) — gated on legal review.
- Skill marketplace / AI templates (#2), full social feed (#8) — post-beta.
- Spectator reasoning-summary / predictions (#4 polish) — fast-follow, not beta.

## Guiding principles (keep it simple)

- **Reuse before build.** Ratings, matches, replay, spectator, sandbox, manifest
  all exist — assemble them, don't rebuild.
- **One new backbone only:** a small transactional-outbox event bus. Everything
  else is a thin read/projection or a gate on existing code.
- **Every feature = an endpoint + a test.** No feature is "done" until an
  integration test drives it against a live stack.
- Additive migrations only; free/dev-only means no money code paths change.

---

## P0 — Prove the current loop actually runs (do first)

Goal: turn "the backend should work" into "here is a green test that proves it."

| Task | Detail | Acceptance |
|---|---|---|
| P0.1 Stack up | `deploy/docker-compose.yml` (Postgres+Redis) or local binaries; env from `.env.example` | `store.Open` connects; `/healthz` + `/readyz` green |
| P0.2 Migrations | Apply 0001–0020 via `make migrate` | `schema_migrations` at head, not dirty |
| P0.3 Existing e2e | Run `tests/integration/money_flow_test.go` | passes (or documented gaps) |
| P0.4 Loop e2e (new) | New integration test: signup → submit manifest (JSON) → set endpoint-secret → verify (against an in-test stub agent) → assert certified/active → read public profile | green under `-tags=integration` |
| P0.5 One-command | `make beta-up` (or documented) that spins deps + migrates + seeds + serves | a fresh clone reaches the loop in one command |

**Exit metric:** one command spins the stack and a scripted developer completes
register → certified manifest → public profile, verified by a passing test.

### P0 RESULT — ✅ done, and it caught 4 real bugs

Ran live (Postgres 15 + Redis 7 + host server + `migrate` v4.17.1). The loop test
(`tests/integration/agent_loop_test.go`) is green: signup → manifest → endpoint
secret → verify (real HTTP to a stub agent) → certified/active → public profile.
`go vet`, gofmt, full unit suite, and both integration tests all pass.

Bugs found and fixed (none of these could have deployed before):

1. **Duplicate migration versions** (0013/0014/0015 each used twice) — `migrate`
   refused to run at all. Renumbered to a unique monotonic sequence (now 0001–0024).
2. **`0019_profile_flow` UNIQUE(owner_user_id)** contradicted the `identity`
   model (one human → N agents) and the seed (1 owner : 3 house agents); migration
   aborted dirty. Removed the incorrect unique index (0002's non-unique index
   already covers owner lookups).
3. **`idx_wallets_system_kind` too broad** — `UNIQUE(kind) WHERE agent_id IS NULL`
   also captured user wallets, so only ONE developer could ever sign up (2nd got
   500). Fixed in **migration 0024** → `WHERE agent_id IS NULL AND user_id IS NULL`.
4. **`ActiveManifest` ambiguous column** — my own M1 query JOINs `agents`
   (also has `public_id`) with unqualified columns → 500 on the public profile.
   Qualified all columns with the `m.` alias. (Unit tests used fakes, so real SQL
   was never exercised — this is exactly why P0 exists.)

Minor (non-blocking, noted): the demo-agent seeder logs a guarded WARN on a
duplicate system wallet — idempotency nit in `EnsureDevAgents`, safe to leave.

Run recipe: `make compose-up` (or deps + `make migrate`), start the server with
`AGENT_VERIFY_ALLOW_PRIVATE=true`, then `make test-e2e`.

---

## P1 — MVP: close the loop for a solo developer

Ordered by dependency. Each is intentionally small.

### P1.1 — Event bus (the only new backbone)
- `events` table (transactional outbox) + a dispatcher goroutine (mirror the
  SMS-outbox pattern the sibling repo trusts).
- Emit past-tense facts: `agent.certified`, `match.finished`, `season.rolled`,
  `badge.awarded`. Consumers idempotent (keyed by event id).
- **Why first:** notifications, profile stats, and badges all subscribe to it.
- **Acceptance:** emitting an event in a tx persists one row; dispatcher delivers
  once; redelivery is idempotent (test).

### P1.2 — Certification gate
- Ranked/tournament entry requires an **endpoint-verified + sandbox-certified**
  active manifest version (reuse `manifest.verify` + `devplatform` certify).
- **Acceptance:** uncertified agent → 403 on ranked join; certified → allowed
  (integration test).

### P1.3 — Seasons (lifecycle, not just a field)
- `seasons` table (id, starts_at, ends_at, status); scheduled roll emits
  `season.rolled`; snapshot leaderboard → `season_standings`.
- Read APIs: current season leaderboard + a season's final standings.
- **Acceptance:** rolling a season freezes standings and starts a fresh board
  (test with a forced clock).

### P1.4 — Public agent profile + stats (assemble existing)
- One public read: identity + active manifest (`publicView`) + current rating +
  season history + win-rate + elo history + matches played + badges.
- **Acceptance:** `GET /v1/agents/{id}/profile` returns the full card for a
  public agent; 404 for unknown/private.

### P1.5 — Shareable replays
- Public replay token + read API (timeline/events/winner) + OG metadata for
  link unfurls. Reuse engine replay + `replay` package.
- **Acceptance:** a finished match yields a public link that renders a replay
  without auth.

### P1.6 — Badges (subset)
- `achievements` + `agent_achievements`; award on events: `certified`,
  `first_win`, `season_champion`, `verified_developer`.
- **Acceptance:** certifying an agent awards `certified` exactly once (idempotent
  via event id).

### P1.7 — Reference-bot seeding
- Guarantee a solo dev always finds a ranked match: if no human opponent is
  queued within N seconds, seat a platform reference bot (bots exist).
- **Acceptance:** a lone agent joining ranked completes a full match.

**Exit metric (the YC demo):** a brand-new developer, with nobody else online,
can register → certify → play a ranked match → get a shareable replay + a public
profile with a badge — end to end, on a fresh stack.

### P1 RESULT — ✅ all done, proven live against Postgres + Redis + the server

Every item shipped with an integration test driving the real HTTP API. Full
`go test ./...` green (0 failures), `go vet` clean, gofmt clean, 7 integration
tests pass.

- **P1.1 event bus** — `events` outbox (migration 0025) + dispatcher
  (`internal/events`); `agent.certified` + `season.rolled` emitted in-tx. Live:
  certify → event persisted → delivered → published.
- **P1.2 certification gate** — ranked queue (`POST /v1/queue`) requires a
  verified manifest. Live: uncertified → 403 `agent_not_certified`; certified →
  202. Sandbox/practice stays open.
- **P1.3 seasons** — `GET /v1/seasons/current` (bounds + remaining) + season
  roller emitting `season.rolled` with the champion (migration 0026). Live: 18
  seasons rolled + published on startup.
- **P1.4 public profile** — certification badge + developer-declared model +
  supported games + ELO/season history on `GET /v1/agent/{id|slug}/profile`.
- **P1.5 shareable replays** — already served by public `GET /v1/match/{id}/replay`
  (full event log + seed reveal + move-signature verdict); `recent_matches[].match_id`
  is the public replay library. Capstone test fetches a real finished match's replay.
- **P1.6 badges** — `agent_badges` (migration 0027) awarded off the event bus
  (`certified`, `season_champion`, `first_win`), idempotent, surfaced on the
  profile. Live proof.
- **match.finished + first_win** — competitive matches emit a transactional
  `match.finished` (in `repo.Finish`'s tx; nil for sandbox). A badge handler awards
  `first_win` to the winner (idempotent ⇒ earned on the first win). Match-finished
  notifications already existed via the social fan-out (`finishHook → social`).
  Unit-proven: competitive emits payload+winner, sandbox emits nil, first_win
  idempotent, tie no-ops.
- **P1.7 solo-dev opponent** — met by the existing **sandbox** (dev vs
  auto-playing house bot, free/unranked, always available). Ranked reference-bots
  are deferred (they entangle coins/rating — a post-beta money decision). Capstone
  test plays a full sandbox match to completion → public replay.

**Bugs fixed across P0+P1 (6):** dup migration versions; wrong `UNIQUE(owner)`
index; wallet index blocking multi-dev signup; ambiguous-column SQL in
ActiveManifest; season-0 leaderboard defaulting in championOf; `newMetrics(nil)`
panic. Migrations now 0001–0027, all apply clean.

**Deferred (unchanged):** organizations, real-money economy, marketplace,
full social feed, ranked reference-bots — post-beta.

---

## Sequencing

P0 (all) → P1.1 event bus → P1.2 cert gate → P1.3 seasons → P1.4 profile →
P1.5 replays → P1.6 badges → P1.7 seeding. Each lands with its own integration
test; the suite is the definition of done.
