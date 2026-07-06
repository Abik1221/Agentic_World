# Game UI ↔ Backend Integration Plan

_Audit + plan produced 2026-07-06. Scope: `Frontend/` (Next.js) ↔ `backend/` (Go arena)._

## 1. Current state (audited, verified against source)

### Backend (Go, chi router — routes registered in `cmd/server/main.go`)
- **3 game engines fully implemented, no stubs:** `internal/engine/{goofspiel,mafia,monopoly}`.
- **Served agent-play (pull/long-poll):**
  - Goofspiel: `/v1/lobby*`, `/v1/match/{id}/state|action|replay`, matchmaking `/v1/queue`.
  - Mafia: `/v1/mafia/lobby*`, `/v1/mafia/{id}/state|action|watch|economy|replay`.
  - Monopoly: `/v1/monopoly/lobby/create`, `/v1/monopoly/{id}/state|action|watch|economy|replay` (creator + server bots).
- **Sandbox (Goofspiel house-bot practice):** `GET /v1/sandbox/opponents`, `POST /v1/sandbox/match`; play then reuses the standard `/v1/match/{id}/*` loop. `match.Service.CreateSandbox` = mode=sandbox, bid 0, no limits/rating/cert gate.
- **Realtime = SSE only** (no WebSocket): `/v1/match|mafia|monopoly/{id}/watch`, resumable via `Last-Event-ID`, 25s keepalive.
- **Push-play (built, NOT wired):** `internal/remoteplay/goofspiel.go` + `internal/agentclient.Play` — platform drives the developer's hosted endpoint. Currently only used for manifest endpoint *verification*, never as a live match driver.
- **Manifest pipeline:** `/v1/agents/{id}/manifest*` (submit → endpoint-secret → verify).
- **Platform bus (Admin↔Arena):** internal outbox → Redis stream, Ed25519-signed. No HTTP endpoint.

### Frontend (Next.js app router, single API layer `lib/api.ts`)
- Full agent-play client for all 3 games (lobby/create/join/state/action + SSE watch + economy + replay).
- Per-game **spectator viewers** (`components/{goofspiel,mafia,monopoly}/*Viewer.tsx`) — **scripted demos**, not live.
- Only real live-SSE consumer: `app/spectate/SpectateLive.tsx` (Goofspiel-shaped).
- Nav in `components/console/nav.ts` — parent groups with children; "Games" group already lists the 3 games.
- Every call falls back to mock unless `NEXT_PUBLIC_API_STRICT=1`.

## 2. Gaps (the work)
1. **Sandbox: no frontend at all** — add API client fns + per-game sandbox play UI + "Sandbox" nav group.
2. **Live viewers are demos** — wire the 3 viewers to live SSE (reuse/extend `useGoofFeed`/`useMafiaFeed`, add `useMonopolyFeed`).
3. **Push-play unwired** — seat `remoteplay` deciders into the live/sandbox match pipeline (Go); extend to Mafia/Monopoly; add manifest endpoint-registration UI.
4. **4 FE-called endpoints missing in BE** — `agent/profile`, `agent/game-config`, `notifications`, `magic-link[/verify]`.
5. **Matchmaking queue** unused by FE (optional; backend prefers it over lobby).

## 3. Phased plan
- **Phase 0 — Make it real:** run backend (Postgres+Redis) + frontend; set `NEXT_PUBLIC_API_STRICT=1`; align CORS/ports; confirm what's genuinely connected vs mock.
- **Phase 1 — Sandbox UI + nav:** `fetchSandboxOpponents`/`createSandboxMatch` in `lib/api.ts`; "Sandbox" nav group with Goofspiel/Mafia/Monopoly + Sandbox; per-game sandbox play views (browser plays vs house bot via existing endpoints).
- **Phase 2 — Live spectator wiring:** connect the 3 viewers to live SSE feeds; add Monopoly feed hook.
- **Phase 3 — Agent-vs-engine (push-play):** wire `remoteplay` into the served pipeline (Goofspiel first, then Mafia/Monopoly); manifest endpoint UI.
- **Phase 4 — Backend gap endpoints + polish:** implement the 4 missing endpoints; disable mock fallbacks; end-to-end verification.

_(Decisions pending from product owner before execution — see chat.)_
