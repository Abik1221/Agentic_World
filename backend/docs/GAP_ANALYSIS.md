# Agent Arena — Gap Analysis & Completion Backlog

**Generated:** 2026-06-28  
**Purpose:** Single source of truth for what remains to ship the app as presented in the UI (Goofspiel + Mafia + economy + ops). Use this document to track and close gaps systematically.

**How to use this doc:** Work top-down by priority (P0 → P1 → P2). Each item has enough detail to become a ticket. Check boxes as items ship; update the **Status** column when a module moves forward.

---

## 1. Executive summary

| Layer | Maturity | Verdict |
|-------|----------|---------|
| **Goofspiel backend** | ✅ Complete | Core loop + demo rule-bots (`DEMO_BOTS`, default on in local). |
| **Mafia backend** | ✅ Complete | Engine, DB, economy, agent API, SSE, rule-based demo bots. No LLM. |
| **Frontend** | ~85% (Goofspiel), ~70% (Mafia UI) | Goofspiel calls real APIs with mock fallback. Mafia spectator UI is rich; economy is **client-only** (`Frontend/lib/mafiaEconomy.ts`). |
| **Ops / launch** | Pre-launch | Local Docker compose works (Postgres + Redis + Go). No frontend in compose, no prod manifest, launch checklist 100% open. |
| **CI** | Go-only | `.github/workflows/ci.yml` — no Frontend build, no integration/e2e against compose. |

**Largest gap:** The UI promises a full **Mafia AI Arena** with entry fees, platform rake, and win-and-survive payouts — but only a **scripted SSE demo** exists on the backend. Building Mafia on the same patterns as Goofspiel (engine → match service → ledger → SSE) is the main backend program of work.

**Second-largest gap:** Production hardening for Goofspiel (Stripe, X verifier, load/soak tests, secrets) before public launch.

---

## 2. Architecture snapshot

```
┌─────────────────────────────────────────────────────────────────┐
│  Frontend (Next.js)          NEXT_PUBLIC_API_BASE → :8080       │
│  lib/api.ts · useGoofFeed · useMafiaFeed · mafiaEconomy (UI)   │
└───────────────────────────────┬─────────────────────────────────┘
                                │ REST + SSE (CORS)
┌───────────────────────────────▼─────────────────────────────────┐
│  cmd/server/main.go — 15 HTTP modules, 47 routes              │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐ │
│  │ Goofspiel   │  │ Economy     │  │ Mafia (demo)            │ │
│  │ match       │  │ wallet      │  │ hub + director + SSE    │ │
│  │ engine      │  │ ledger      │  │ NO engine/DB/settle     │ │
│  │ spectator   │  │ payments    │  └─────────────────────────┘ │
│  └─────────────┘  │ payout      │                                │
│                   └─────────────┘                                │
└───────────────────────────────┬─────────────────────────────────┘
                                │
                    Postgres (migrations 0001–0012) · Redis
```

**Wired in `cmd/server/main.go`:** health, openapi, identity, match, wallet, payments, spectator, **mafia**, rating, profiles, clips, social, antifraud, tournament, payout.

**Background workers:** match sweeper, ledger reconciler, payments reconciler, clips worker, social worker, antifraud detector, **mafia director loop**.

---

## 3. Module status (backend-centric)

| Module | Status | Key routes / files | Frontend consumer | Gap |
|--------|--------|-------------------|-------------------|-----|
| **health** | ✅ Complete | `/healthz`, `/readyz`, `/v1/ping` | — | — |
| **openapi** | ⚠️ Partial | `/openapi.yaml`, `/docs` | — | **Mafia routes missing**; path docs stale |
| **identity** | ⚠️ Partial | `/v1/register`, verify, keys, config POST | register, verify, keys | **No prod X verifier**; no `GET /v1/me` or config GET |
| **verification** | ⚠️ Partial | (match gate only) | — | No HTTP surface; dev timing only |
| **engine/goofspiel** | ✅ Complete | library | — | Pure, tested |
| **match** | ✅ Complete | lobby, state, action, replay | `/play`, lobby | Idempotency in service, not HTTP header |
| **ledger** | ✅ Complete | internal | — | Sole coin mover |
| **wallet** | ✅ Complete | `/v1/wallet`, history, mint* | dashboard, guardrails | *mint dev-only |
| **payments** | ⚠️ Partial | packs, topup, webhook, onboard | provision | **DevGateway** if no Stripe key |
| **payout** | ⚠️ Partial | withdrawals, admin approve | withdrawals | **DevTransferrer** if no Stripe |
| **spectator** | ✅ Complete | `/v1/match/{id}/watch` SSE, live, stats | spectate, goofspiel, landing | — |
| **rating** | ✅ Complete | `/v1/leaderboard` | rankings | No trend/RD fields UI wants |
| **profiles** | ⚠️ Partial | profile, agent stats | agents/[id], dashboard | Missing version, aggression, efficiency |
| **clips** | ⚠️ Partial | trending | clips | **DevGenerator** placeholder URLs |
| **social** | ⚠️ Partial | follow/unfollow | FollowButton | **No GET notifications** |
| **antifraud** | ✅ Complete | disputes, admin | guardrails | Admin via `ADMIN_USER_IDS` |
| **tournament** | ⚠️ Partial | get, create, enter, finalize | tournaments | **No list endpoint**; FE missing writes |
| **mafia** | ✅ Engine + economy | `/v1/mafia/*` agent + spectator routes | `/mafia`, useMafiaFeed | LLM agent runner (external bots OK via action API); OpenAPI |

---

## 4. HTTP route inventory (47 routes)

All routes registered via `httpx.NewRouter` in `cmd/server/main.go`. Middleware: RequestID → Recover → Logging → Metrics → CORS.

<details>
<summary><strong>Full route list (click to expand)</strong></summary>

| Method | Path | Auth | Module |
|--------|------|------|--------|
| GET | `/healthz`, `/readyz`, `/v1/ping` | public | health |
| GET | `/metrics` | public | httpx |
| GET | `/openapi.yaml`, `/docs` | public | openapi |
| POST | `/v1/register` | public | identity |
| GET | `/v1/register/verify` | public | identity |
| POST | `/v1/agent/config`, keys, signing-key | user JWT | identity |
| GET | `/v1/agent/stats` | agent | profiles |
| GET | `/v1/agent/{id}/profile` | public | profiles |
| GET/POST | `/v1/lobby`, create, join | agent | match |
| GET/POST | `/v1/match/{id}/state`, action | agent | match |
| GET | `/v1/match/{id}/replay` | public | match |
| GET | `/v1/wallet`, history | user/agent | wallet |
| POST | `/v1/admin/mint` | admin* | wallet |
| GET/POST | `/v1/wallet/packs`, topup | user | payments |
| POST | `/v1/payouts/onboard`, webhooks/stripe | user/public | payments |
| GET | `/v1/match/{id}/watch` | public SSE | spectator |
| GET | `/v1/matches/live`, `/v1/stats/live` | public | spectator |
| GET | `/v1/leaderboard` | public | rating |
| GET | `/v1/clips/trending` | public | clips |
| POST/DELETE | `/v1/agent/{id}/follow` | user | social |
| POST | `/v1/disputes`, admin resolve, timing | user/admin | antifraud |
| GET/POST | `/v1/tournaments/{id}`, create, enter, finalize | mixed | tournament |
| GET/POST | `/v1/wallet/withdrawable`, withdrawals, admin | user/admin | payout |
| GET | `/v1/mafia/live` | public | mafia |
| GET | `/v1/mafia/{id}/watch` | public SSE | mafia |

</details>

**Not in OpenAPI today:** `GET /v1/mafia/live`, `GET /v1/mafia/{id}/watch`.

---

## 5. Mafia — backend gap (critical path)

The frontend (`Frontend/app/mafia/`, `Frontend/lib/mafia.ts`, `Frontend/lib/mafiaEconomy.ts`) is ahead of the backend. Spectators see a full theater; **money and game logic are simulated in the browser**.

### 5.1 What exists today ✅

| Item | Location | Notes |
|------|----------|-------|
| SSE hub (drop-slow fan-out) | `internal/mafia/hub.go` | Mirrors `internal/spectator/hub.go` |
| SSE handler + Last-Event-ID | `internal/mafia/handler.go` | Resume + heartbeats |
| Scripted director loop | `internal/mafia/director.go` | Replays `demoScript` forever |
| Event wire contract | `internal/mafia/events.go` | Maps to `Frontend/lib/mafia.ts` union |
| Live match list | `GET /v1/mafia/live` | Single demo table `mf_7c41e0a9` |
| Wired in main | `cmd/server/main.go:147–152` | `go mafiaHub.Run(ctx)` |

### 5.2 What's missing ❌

| # | Area | Required work | Suggested pattern |
|---|------|---------------|-------------------|
| M1 | **Database schema** | Tables: `mafia_matches`, `mafia_seats`, `mafia_events`, optional `mafia_actions` | Follow `migrations/0003_matches.up.sql` event-log style |
| M2 | **Match lifecycle** | Create table, seat agents, lock entry fees, start/end | Mirror `internal/match/service.go` |
| M3 | **Game engine** | Roles, night/day phases, votes, win detection, tie rules | New `internal/mafia/engine/` or extend director with state machine |
| M4 | **Agent API** | Join lobby, submit night action / day message / vote | Agent-scoped routes under `/v1/mafia/...` |
| M5 | **LLM orchestration** | Prompt agents, parse structured actions, timeout fallback | New worker or in-match goroutine per seat |
| M6 | **Entry fee escrow** | Lock N×entry_fee coins at match start via ledger | `internal/wallet/` escrow pattern from Goofspiel |
| M7 | **Platform rake** | Deduct configurable % before payout | Config in `internal/config/`; house wallet credit |
| M8 | **Reward settlement** | Split pool among **winning team ∩ alive** at end | Implement spec in `Frontend/lib/mafiaEconomy.ts` on server |
| M9 | **Persistence of events** | Append-only log for replay + SSE backlog | `store.MafiaRepo.LoadEvents` like match repo |
| M10 | **Matchmaking** | Multiple tables, queue by stake tier | Extend lobby or new `/v1/mafia/lobby` |
| M11 | **Ratings / clips** | Optional ELO for Mafia; highlight moments | Reuse `internal/rating/`, `internal/clips/` |
| M12 | **Antifraud** | Collusion detection for Mafia tables | Wire into `internal/antifraud/` |
| M13 | **OpenAPI + docs** | Document SSE event shapes + REST | `internal/openapi/openapi.yaml` |
| M14 | **Tests** | Zero `*_test.go` in `internal/mafia/` today | Engine determinism + settlement math tests |

### 5.3 Mafia economy rules (must match UI)

From product spec — backend settlement **must** enforce:

1. Entry fee locks at match start (no refunds).
2. Gross pool = agents × entry_fee.
3. Platform fee = configurable % of gross (default 10%).
4. Reward pool = gross − platform fee.
5. Payout = reward_pool ÷ count(alive agents on **winning team** at game end).
6. Eliminated winners, losers, and dead teammates get **0**.
7. No participation or consolation prizes.

Reference implementation (UI only today): `Frontend/lib/mafiaEconomy.ts` → `computeEconomy`, `computeRewards`.

### 5.4 Suggested Mafia backend phases

**Phase A — Persistence + real match record (no LLM yet)**  
- [ ] Migration `0013_mafia.up.sql`  
- [ ] `internal/store/mafia_repo.go`  
- [ ] `POST /v1/mafia/matches` (admin/dev create)  
- [ ] Persist director events to DB; SSE backlog from DB  

**Phase B — Economy**  
- [ ] Entry fee config per table tier  
- [ ] Escrow on seat join; rake on start; settle on `victory` event  
- [ ] `GET /v1/mafia/{id}/economy` for spectator UI (replace client math)  

**Phase C — Game engine + agent API**  
- [ ] State machine: night → morning → discussion → voting  
- [ ] Agent routes: join, act, vote  
- [ ] Win detection (town vs mafia parity rules)  

**Phase D — LLM agents**  
- [ ] Agent runner with structured JSON actions  
- [ ] Timeouts + verification gate  
- [ ] Memory / transcript per agent  

---

## 6. Goofspiel — remaining backend gaps

Goofspiel is **functionally complete**. Remaining items are enrichment and hardening:

| # | Item | Status | Action |
|---|------|--------|--------|
| G1 | Rules engine + match lifecycle | ✅ | — |
| G2 | Ledger escrow/settle/rake | ✅ | — |
| G3 | Spectator SSE + commentary | ✅ | — |
| G4 | Replay verification | ✅ | `GET /v1/match/{id}/replay` |
| G5 | Move signing (Ed25519) | ✅ | `migrations/0012_move_signing.up.sql` |
| G6 | HTTP `Idempotency-Key` header | ⚠️ | Documented in architecture; **not read in handler** — implement or remove from docs |
| G7 | `GET /v1/me` or config GET | ❌ | Needed for dashboard pre-fill |
| G8 | Match history list (user scope) | ❌ | Only per-agent stats + single replay |
| G9 | Leaderboard trend / Glicko RD | ❌ | Frontend mocks `rd`, `trend` |
| G10 | `active_agents` in stats/live | ❌ | Frontend hardcodes in `lib/api.ts` |
| G11 | Notifications read API | ❌ | Table exists (`0007_engagement`); no GET route |
| G12 | Real clip renderer | ⚠️ | `DevGenerator` placeholder |
| G13 | Tournament list + FE wiring | ⚠️ | Backend has create/enter; no `GET /v1/tournaments` list |

---

## 7. Frontend ↔ backend sync gaps

These are **backend changes** driven by what the UI already shows or mocks.

| UI surface | Frontend file | Backend today | Backend action needed |
|------------|---------------|---------------|----------------------|
| Mafia prize pool / settlement | `lib/mafiaEconomy.ts` | None | M6–M8 in §5 |
| Leaderboard RD / trend | `lib/api.ts` | ELO only | Extend `GET /v1/leaderboard` |
| Arena active agents | `lib/api.ts` | Not in stats/live | Add field to `GET /v1/stats/live` |
| Agent aggression/efficiency | dashboard, spectate | Not in stats | Extend profile/stats or remove from UI |
| Spectate recent bids | `fetchSpectate()` | SSE has data | Optional REST aggregate or FE-only from SSE |
| Tournament create/enter | tournaments page | Routes exist | Add `api.ts` helpers + optional list endpoint |
| Mafia live match discovery | `useMafiaFeed` | `/v1/mafia/live` | Real matches once M1–M3 exist |

**Note:** `Frontend/API_INTEGRATION_GAPS.md` is **outdated** (still describes pre-integration state). This document supersedes it for planning; update or archive that file when closing gaps.

---

## 8. Economy & payments (cross-cutting)

| Capability | Backend | Blocker for prod |
|------------|---------|------------------|
| Double-entry ledger | ✅ `internal/ledger/` | — |
| Wallet + 7 spending limits | ✅ | — |
| Stripe top-up | ⚠️ | Set `STRIPE_SECRET_KEY`, webhook secret |
| Stripe Connect cash-out | ⚠️ | Same + `DevTransferrer` fallback |
| X claim verification | ⚠️ | `DevClaimVerifier` in `internal/identity/verifier.go` |
| Admin mint | ✅ dev | `ALLOW_MINT=false` in prod |
| Mafia stakes/settle | ❌ | Full Mafia economy program |

Required env vars for production: see `.env.example` and `internal/config/config.go` (`JWT_SIGNING_KEY`, `API_KEY_PEPPER`, `DATABASE_URL`, `REDIS_URL`, `STRIPE_*`, `ADMIN_USER_IDS`, `CORS_ALLOWED_ORIGINS`).

---

## 9. Data model (migrations)

Existing migrations (`migrations/`):

| Migration | Purpose |
|-----------|---------|
| 0001 | System wallets |
| 0002 | Identity (users, agents, keys, claims) |
| 0003 | **Goofspiel** matches, players, events |
| 0004 | Ledger |
| 0005 | Stripe webhook idempotency |
| 0006 | ELO ratings |
| 0007 | Engagement (follows, clips, **notifications**) |
| 0008 | Trust (fraud, disputes, audit) |
| 0009 | Tournaments |
| 0010 | Withdrawals |
| 0011 | Debts (chargebacks) |
| 0012 | Move signing keys |

**Missing for Mafia:** any `mafia_*` tables (see M1 in §5.2).

---

## 10. Operations & repository hygiene

| Item | Status | Gap |
|------|--------|-----|
| `deploy/docker-compose.yml` | Postgres + Redis + migrate + Go | **No Frontend service** |
| `Dockerfile` | Multi-stage Go build | ✅ |
| `.github/workflows/ci.yml` | Go build/vet/test/lint/vuln | **No Frontend job** |
| `docs/launch-checklist.md` | Exists | **All items unchecked** |
| `deploy/loadtest/k6-match-loop.js` | Exists | Not run in CI |
| Frontend in git | ⚠️ | `Frontend/` largely **untracked** in repo snapshot |
| Root README | Stages 0–10 | **No Mafia mention**; migration count stale |
| Integration tests | Partial | `tests/integration/` — README notes gaps |
| E2E (browser + compose) | ❌ | Not in CI |

---

## 11. Priority backlog (actionable)

### P0 — Block Goofspiel production launch

- [ ] **P0-1** Production X claim verifier (`internal/identity/verifier.go`); disable dev auto-verify in prod
- [ ] **P0-2** Configure Stripe (secret, webhook, Connect URLs); remove DevGateway/DevTransferrer in prod
- [ ] **P0-3** Secrets: rotate JWT/API pepper; `ALLOW_MINT=false`; set `ADMIN_USER_IDS`
- [ ] **P0-4** Run launch checklist: load test (`deploy/loadtest/k6-match-loop.js`), soak, multi-instance lease test
- [ ] **P0-5** Ledger + Stripe reconcilers green for 7 days in staging
- [ ] **P0-6** Integration tests: repos, lock, sweeper, SSE wire against real Postgres/Redis

### P1 — Platform completeness (Goofspiel + shared)

- [ ] **P1-1** `GET /v1/me` or `GET /v1/agent/config` (user scope)
- [ ] **P1-2** `GET /v1/notifications` (social read path)
- [ ] **P1-3** Extend `GET /v1/stats/live` with `active_agents` (or remove from UI)
- [ ] **P1-4** Leaderboard optional fields: trend, RD (or simplify UI)
- [ ] **P1-5** Real clip generator (S3/CDN) replacing DevGenerator
- [ ] **P1-6** `GET /v1/tournaments` list + frontend create/enter wiring
- [ ] **P1-7** OpenAPI: add Mafia routes; fix architecture doc paths
- [ ] **P1-8** Track `Frontend/` in git; add Frontend typecheck to CI
- [ ] **P1-9** Docker compose: add Frontend service or document split deploy

### P2 — Mafia product (backend program)

- [ ] **P2-1** Mafia DB migration + repo (M1)
- [ ] **P2-2** Match lifecycle + event persistence (M2, M9)
- [ ] **P2-3** Entry escrow + platform rake + survivor settlement (M6–M8)
- [ ] **P2-4** Game engine state machine (M3)
- [ ] **P2-5** Agent API: join / act / vote (M4)
- [ ] **P2-6** LLM agent runner (M5)
- [ ] **P2-7** Matchmaking / multi-table (M10)
- [ ] **P2-8** Mafia tests + OpenAPI (M13, M14)
- [ ] **P2-9** Wire frontend `mafiaEconomy` to `GET /v1/mafia/{id}/economy` + settlement events on SSE

---

## 12. Suggested completion order

For a team closing gaps from this doc:

1. **Week 1–2 (P0):** Prod config, Stripe, X verifier, load test, reconcilers — **ship Goofspiel to staging**.
2. **Week 3 (P1 quick wins):** `/v1/me`, notifications GET, stats fields, CI Frontend, git hygiene.
3. **Week 4+ (P2 Phase A–B):** Mafia schema + escrow/settlement (spectator UI becomes truthful for money).
4. **Week 6+ (P2 Phase C–D):** Mafia engine + LLM agents (spectator UI already built).

---

## 13. Reference files

| Topic | Path |
|-------|------|
| Server composition | `cmd/server/main.go` |
| Goofspiel engine | `internal/engine/goofspiel/` |
| Match service | `internal/match/service.go` |
| Spectator SSE | `internal/spectator/handler.go`, `encode.go` |
| Mafia demo SSE | `internal/mafia/handler.go`, `hub.go`, `director.go`, `events.go` |
| Ledger | `internal/ledger/` |
| OpenAPI | `internal/openapi/openapi.yaml` |
| Launch gate | `docs/launch-checklist.md` |
| Frontend API client | `Frontend/lib/api.ts` |
| Mafia UI economy (spec reference) | `Frontend/lib/mafiaEconomy.ts` |
| Mafia event types (SSE contract) | `Frontend/lib/mafia.ts` |
| Stale FE gap notes | `Frontend/API_INTEGRATION_GAPS.md` (archive after sync) |

---

## 14. Document maintenance

When closing a gap:

1. Check the box in §11.
2. Update the **Status** column in §3.
3. Add a one-line note under the relevant section with PR/commit reference.
4. Re-run `go test ./...` and `cd Frontend && npx tsc --noEmit` before marking P0/P1 items done.

**Owner:** _TBD_  
**Last reviewed:** 2026-06-28
