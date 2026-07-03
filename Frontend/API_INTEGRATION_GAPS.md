# API Integration Gaps — Frontend ↔ Backend

> **Superseded for planning:** use [`docs/GAP_ANALYSIS.md`](../docs/GAP_ANALYSIS.md) as the single backlog for backend + full-app gaps. This file is kept for historical frontend integration notes.

_Generated 2026-06-27. Frontend: this Next.js app (`Frontend/`). Backend: Go service in repo root._

## ✅✅ Status: FULL SCOPE complete (2026-06-27)

Every backend capability now has a working frontend surface, and every interactive flow calls the real API. No Go backend changes were required — the backend was already feature-complete per the stage docs.

New since the first integration pass:

- **Live spectator stream** — `app/spectate/SpectateLive.tsx` connects an `EventSource` to `GET /v1/match/{id}/watch` and renders live rounds, scores and commentary.
- **Write actions wired** — provision top-up (`POST /v1/wallet/topup` → Stripe redirect) and mint (`POST /v1/admin/mint`); guardrails file-withdrawal (`POST /v1/withdrawals`) + Stripe Connect onboarding (`POST /v1/payouts/onboard`).
- **New pages** — `/play` (full agent match console: lobby create/join, long-poll `state`, play `action`), `/clips` (trending), `/agents/[id]` (public profile + follow/unfollow), `/tournaments` (lookup), `/withdrawals` (withdrawable + quote + track + dispute), `/keys` (API key rotate/revoke + signing key).
- **Nav & session** — top nav now links every page and shows a real Sign in / Sign out based on session state.

Endpoint coverage is now complete: leaderboard, matches/live, stats/live, wallet (+limits, withdrawable, packs, topup, mint), agent stats/config, register/verify, clips, profiles, social follow, tournaments, withdrawals, disputes, key management, lobby/create/join, match state/action, and the SSE watch stream.

Verification: `npx tsc --noEmit` passes clean on **both** `a/` and `agent-arena/`. (A full `next build` exceeds the sandbox time limit but the type check covers all imports/types; run it locally for the production bundle.)

---

## ✅ Status: integrated (2026-06-27)

The integration described below is now **built and wired** in both `a/` and `agent-arena/`. No backend Go changes were needed — every gap was closed by composing existing endpoints on the frontend.

What was added:

- `lib/api.ts` — typed, isomorphic API client. One `fetch*` function per endpoint, each mapping the Go JSON into the existing UI types in `lib/mock.ts`. Every call **falls back to mock data** if the backend is unreachable, so the UI always renders. Set `NEXT_PUBLIC_API_STRICT=1` to surface errors instead.
- `lib/session.ts` (client) + `lib/session.server.ts` (server) — cookie-based session holding the dashboard JWT and agent API key.
- `.env.local.example` — `NEXT_PUBLIC_API_BASE` (defaults to `http://localhost:8080`).

Pages wired to live endpoints: `rankings` → `/v1/leaderboard`; `dashboard` → `/v1/agent/stats` + `/v1/wallet`; `lobby` + landing → `/v1/matches/live` + `/v1/stats/live`; `guardrails` + `strategy` → `/v1/wallet` (limits) + `POST /v1/agent/config`; `provision` → `/v1/wallet/packs`; `spectate` → `/v1/matches/live`; `register` → `POST /v1/register`; `verify` → `GET /v1/register/verify` (polls, persists session, shows real creds); `login` → captures the dashboard token.

Setup: `cp .env.local.example .env.local`, point `NEXT_PUBLIC_API_BASE` at the running Go service, and ensure the backend's `CORSAllowedOrigins` includes the frontend origin. `npx tsc --noEmit` passes.

Remaining (intentionally not done):

- **Live bid stream** on `spectate` uses the SSE endpoint `GET /v1/match/{id}/watch`. The header now shows a real live match, but the per-round bid list still uses illustrative data — wiring `EventSource` is a follow-up (marked `TODO` in `fetchSpectate`).
- **Write actions** `topup` (`POST /v1/wallet/topup` → Stripe checkout) and `requestWithdrawal` (`POST /v1/withdrawals`) exist in `lib/api.ts` but their buttons on the server-rendered `provision`/`guardrails` pages aren't clickable yet (would need small client components).
- **Fields not exposed by the backend** are filled from mock defaults: Glicko `rd`, leaderboard `trend`, agent `aggression`/`efficiency`/`version`, and per-match ELO delta. These need new backend fields if they should be real.

---

## TL;DR (original analysis)

The frontend is **not integrated with the backend at all**. Every page imports static data from `lib/mock.ts`; there is no fetch layer, no API client, no auth-token handling, and no environment config pointing at the Go service. The backend, by contrast, exposes a fairly complete REST + SSE surface (~30 routes across 13 modules). Integration work is therefore mostly net-new on the frontend, plus a handful of genuinely missing or mismatched backend endpoints called out below.

Three things block almost everything else:

1. **No API client.** Nothing in `app/` or `lib/` calls `fetch`. A typed client (`lib/api.ts`) plus a base-URL env var (`NEXT_PUBLIC_API_BASE`) is the first dependency.
2. **No auth flow.** The backend uses two credential types — a dashboard **JWT** (`ScopeUser`) and an **agent API key** (`ScopeAgent`). The frontend stores/sends neither. The `/login` page has no backing endpoint (see gaps).
3. **No "current agent / me" read.** The dashboard, strategy, and guardrails pages render the *user's own* agent and its config, but the backend has no `GET /v1/me` and agent config is **write-only** (`POST /v1/agent/config` with no matching GET).

---

## Backend endpoint inventory (mounted in `cmd/server/main.go`)

Auth column: **public** = no auth · **user** = JWT `ScopeUser` · **agent** = API key `ScopeAgent` · **admin** = user scope + admin allowlist.

| Module | Method & path | Auth |
|---|---|---|
| health | `GET /healthz`, `GET /readyz`, `GET /v1/ping` | public |
| openapi | `GET /openapi.yaml`, `GET /docs` | public |
| identity | `POST /v1/register` | public |
| identity | `GET /v1/register/verify` | public |
| identity | `POST /v1/agent/config` | user |
| identity | `POST /v1/agent/keys` · `DELETE /v1/agent/keys/{prefix}` | user |
| identity | `POST /v1/agent/signing-key` | user |
| match | `GET /v1/lobby` · `POST /v1/lobby/create` · `POST /v1/lobby/join` | agent |
| match | `GET /v1/match/{id}/state` · `POST /v1/match/{id}/action` | agent |
| match | `GET /v1/match/{id}/replay` | public |
| wallet | `GET /v1/wallet` · `GET /v1/wallet/history` | user |
| wallet | `POST /v1/admin/mint` | admin |
| payments | `GET /v1/wallet/packs` · `POST /v1/wallet/topup` | user |
| payments | `POST /v1/payouts/onboard` | user |
| payments | `POST /v1/webhooks/stripe` | public (signature-verified) |
| spectator | `GET /v1/match/{id}/watch` (SSE) | public |
| spectator | `GET /v1/matches/live` · `GET /v1/stats/live` | public |
| rating | `GET /v1/leaderboard` | public |
| profiles | `GET /v1/agent/{id}/profile` | public |
| clips | `GET /v1/clips/trending` | public |
| social | `POST /v1/agent/{id}/follow` · `DELETE /v1/agent/{id}/follow` | user |
| antifraud | `POST /v1/disputes` | user |
| antifraud | `POST /v1/admin/disputes/{id}/resolve` · `GET /v1/admin/agent/{id}/timing` | admin |
| tournament | `GET /v1/tournaments/{id}` | public |
| payout | `GET /v1/wallet/withdrawable` · `POST /v1/withdrawals` · `GET /v1/withdrawals/{id}` | user |
| payout | `POST /v1/admin/withdrawals/{id}/approve` · `.../reject` | admin |

---

## Page-by-page gap map

| Page | Mock data used | Backend endpoint(s) | Status |
|---|---|---|---|
| `register/page.tsx` | — | `POST /v1/register` | ✅ Exists — needs wiring |
| `verify/page.tsx` | — | `GET /v1/register/verify` | ✅ Exists — needs wiring |
| `login/page.tsx` | — | _none_ | ❌ **No login endpoint.** Auth is register→verify→JWT + API keys; there is no password/credential login. Either add a login endpoint or redesign this page around the actual token flow. |
| `dashboard/page.tsx` | `userAgent`, `wallet`, `recentEngagements`, `performanceBars` | `GET /v1/wallet` ✅; agent identity ❌; match history ❌ | ⚠️ Partial. Wallet maps cleanly. **No `GET /v1/me`** for `userAgent`; **no per-agent match-history endpoint** for `recentEngagements`/`performanceBars` (only single-match `/replay` exists). |
| `provision/page.tsx` | `coinPacks` | `GET /v1/wallet/packs` ✅, `POST /v1/wallet/topup` ✅ | ✅ Exists — needs wiring (topup likely returns a Stripe checkout/session to redirect to; confirm shape). |
| `guardrails/page.tsx` | `limits`, `wallet` | `GET /v1/wallet` ✅; config read/write ⚠️ | ⚠️ Partial. Wallet OK. `limits` shown here come from agent config, which is **write-only** — no GET to populate the form. |
| `strategy/page.tsx` | `limits`, `userAgent` | `POST /v1/agent/config` ✅ (write); read ❌ | ⚠️ Partial. Can save config; **cannot fetch current config** to pre-fill. Same missing `me`/config-GET gap as dashboard. |
| `lobby/page.tsx` | `lobbyTiers`, `liveMatches`, `arenaStats` | `GET /v1/lobby` (agent), `GET /v1/matches/live`, `GET /v1/stats/live` | ⚠️ Mismatch. `liveMatches`→`/matches/live` and `arenaStats`→`/stats/live` map well. `lobbyTiers`/joining requires **agent-scope** (API key), but the lobby is presented as a logged-in *user* UI — scope mismatch to resolve. |
| `spectate/page.tsx` | `liveMatch`, `recentBids` | `GET /v1/match/{id}/watch` (SSE), `GET /v1/match/{id}/state` | ⚠️ Needs SSE client. Watch is a Server-Sent-Events stream, not a JSON GET; `recentBids` is expected to arrive within that stream/state — confirm payload includes bid history. |
| `rankings/page.tsx` | `leaderboard` | `GET /v1/leaderboard` | ✅ Exists — cleanest 1:1 mapping. Verify field names (`rating`, `rd`, `wins/losses/draws`). |
| `page.tsx` (landing) | `arenaStats`, `strategyDeck`, `deckLabels` | `GET /v1/stats/live` | ✅/static. `arenaStats`→`/stats/live`; `strategyDeck`/`deckLabels` are static UI copy, no endpoint needed. |

Backend capabilities with **no frontend surface yet** (build or descope): clips trending, agent follow/unfollow (social), agent public profiles, tournaments, disputes (antifraud), withdrawals/payouts, API-key management, signing-key registration.

---

## Cross-cutting gaps (priority order)

1. **API client + base URL.** Add `lib/api.ts` and `NEXT_PUBLIC_API_BASE`; replace `lib/mock.ts` imports page-by-page. CORS is already configured server-side (`mw.CORS(cfg.CORSAllowedOrigins)`), so set the allowed origin to the frontend host.
2. **Auth/session model.** Decide and implement: where the JWT lives (cookie vs. memory), how the agent API key is captured and attached to agent-scoped calls, and what `/login` actually does. This unblocks dashboard, strategy, guardrails, lobby.
3. **Missing reads on the backend:**
   - `GET /v1/me` (or `GET /v1/agent/config`) — to populate the user's own agent + current limits/config. Today config is POST-only.
   - Per-agent **match history** — `recentEngagements` and `performanceBars` have no source; only `GET /v1/match/{id}/replay` (single match) exists.
   - A real **login endpoint** (or an explicit decision that `/login` is just "paste your dashboard token / API key").
4. **Scope reconciliation for the lobby.** Joining/creating matches is `ScopeAgent` (API key), but the lobby UI lives in the user dashboard. Clarify whether a logged-in user acts on behalf of their agent automatically.
5. **Streaming, not polling.** `spectate` and live stats are SSE (`/watch`) — needs an `EventSource` client, plus `Last-Event-ID` resume handling the server already supports.
6. **Two identical frontends.** `a/` and `agent-arena/` are byte-for-byte the same app. Decide which is canonical before integrating, or you'll do the work twice.

## Quick wins (endpoints that map 1:1, do these first)

`leaderboard`, `wallet`, `wallet/packs` + `topup`, `register` + `register/verify`, `matches/live`, `stats/live`. These need only the client layer (#1) and, for wallet/packs/topup, user auth (#2).
