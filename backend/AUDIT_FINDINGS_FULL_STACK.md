# Full-stack audit — admin, user, tracing, arena (backend + frontend)

Deep audit across all four surfaces + browser/session security + backend↔UI
contract coverage + dead/broken logic + cross-service compatibility. Findings
verified against a **live running stack** where possible.

**Fixed + proven live this pass:** H1 (rating.updated Elo zeroing). Also
confirmed live: the admin↔arena signed config bus works (earlier rejection was a
local split-Redis-DB env issue, not code).

Legend: ✅ fixed · 🔴 HIGH · 🟠 MED · ⚪ LOW

---

## ✅ FIXED — H1: admin leaderboard Elo permanently 0 (cross-service field drift)
Arena emits `rating.updated` as `{agents:[{agent, rating_after, …}]}`
(`backend/internal/store/rating_repo.go:182`); the admin mirror decoded the field
as `after` (`Super_Admin/.../mirror/mirror.go:345`) → always 0 → wrote `elo=0` for
every agent on every rated match. The 30s backfill never touches `elo`, so it was
**permanent, no self-heal**. Fixed the mirror to read `rating_after`; proven live
(admin Elo 0→1527 on a signed event; forged event correctly rejected). Commit
`aae14a6` (Super_Admin repo).

---

## 🔴 HIGH — recommend next

1. **Admin JWTs (access + refresh) in `localStorage`** — `Super_Admin/frontend/src/app/api/client.ts:5-23`.
   Any XSS on the console steals a 168h refresh token = ~7 days of full admin. Move
   to HttpOnly+Secure+SameSite cookies (esp. refresh).
2. **User client downgrades to JS-readable token cookies on BFF failure** —
   `Pyyol_client/lib/session.ts:97-114`. The `catch` writes `aa_dash` (user JWT) and
   `aa_key` (**agent API secret**) via `document.cookie` (non-HttpOnly). Silent XSS
   exfil of both. Remove the fallback; keep HttpOnly-only.
3. **Admin fail-open in prod** — `Super_Admin/server/internal/platsign/platsign.go:73-75`
   (`Verify` returns true when the engine key is absent) + `config.go:60` `IsProd()`
   defined but **never called**. If `PLATFORM_ENGINE_PUBLIC_KEY`/`JWT_SECRET` are
   unset in prod: forged `platform:events` accepted, admin JWTs forgeable (default
   secret), config published unsigned. Hard-fail on boot when prod + missing keys.
4. **Tracing ClickHouse migrations never auto-apply on existing volumes** —
   `tracing/docker-compose.yml:44` mounts SQL into `/docker-entrypoint-initdb.d`
   (runs only on first init); no migration runner. `004_benchmark_tokens.sql`
   (token columns) silently missing on an existing deploy → token rollups error
   and are swallowed to 0. (Hit live this session — had to apply 004 by hand.)
5. **Tracing query/control APIs have no auth; `x-organization-id` trusted verbatim** —
   `tracing/backend/internal/query/handler.go:1525` only checks the header is
   non-empty. Anyone who can reach `:8082` reads ANY org's telemetry. `/v1/projections*`
   needs no header at all. Fine only if the port is strictly proxy-internal.
6. **Magic-link login is a silent dead path in prod** — `backend/internal/identity/handler.go:73`
   registered + used by the player UI, always returns `{sent:true}`, but there is
   **no email sender anywhere in the backend**. Prod users never get the link. Wire
   a mailer or gate/remove the route in prod.

## 🟠 MED

- **User BFF is an unrestricted credential-injecting proxy** — `Pyyol_client/app/api/be/[...path]/route.ts`. Same-origin script can drive any authenticated endpoint (`/v1/admin/*`, `/v1/withdrawals`). Add a path allow-list.
- **Ingest APIKey not fail-closed** — `tracing/backend/internal/auth/key.go:5` accepts everything if `INGEST_API_KEY==""`. Reject empty.
- **Cross-service phantom/dropped events** — arena publishes `season.rolled`/`pindex.updated` the mirror drops (`mirror.go:362`); `topup.succeeded`/`badge.awarded` handled but never emitted; `dispute.opened` UserName differs live vs backfill. Add a **schema-version field** to `platform:events` (none today) and reconcile the set.
- **Admin cosmetic actions** — notification "send" only flips a DB row (no delivery); fraud "review/suspend" only change status (no arena enforcement). `Super_Admin/server/internal/modules/{notifications,fraud}`.
- **Tracing control-api is all stubs** — Alerts/Settings pages render permanently empty (`tracing/backend/internal/control/handler.go`).
- **Decision-trail telemetry undiscoverable** — rich per-move reasoning/tokens only reachable via one conditional workbench link; no nav entry, no link from the leaderboard/agent pages.
- **Black-theme light islands** — `.warning-state`, `.status-chip.*`, `.status-*` render light on the black UI (`tracing/web/src/app/globals.css:383,561,1096`).
- **User backend-ready flows with no UI** — ranked queue, tournaments, agent manifest/push-play config, API-key list/revoke, subscriptions (Stripe surface abandoned). Backend endpoints live; no user UI.
- **No tournament list endpoint** — only fetch-by-id; undiscoverable (`backend/internal/tournament/handler.go`).
- **Admin refresh token: no rotation/reuse detection** — a stolen refresh stays valid to its 168h expiry (`Super_Admin/server/internal/auth/service.go:143`).
- **Admin dead capability** — 4/5 `/tracking/*` endpoints (provider intel, KPIs, per-agent, pipeline health) fetched server-side but never surfaced.

## ⚪ LOW
- Dead user modules: `useMafiaFeed/useMonopolyFeed/useGoofFeed` (~350 lines), demo-identity fabrication, scripted-demo fallback reachable when `NEXT_PUBLIC_API_STRICT` unset.
- Arena game-name casing (`gameLive("mafia")` vs `"Mafia"`) can zero a dashboard KPI.
- WS token in URL query (admin + user); WS upgrade accepts any Origin.
- Non-standard error envelope on `/v1/agent/status`; `/v1/config` capability flags incomplete.
- Assorted unused arena endpoints (`/v1/arenas`, `/v1/wallet/history` duplicate).

---

## Recommended order to tackle (all end-to-end)
1. **Frontend token hardening** (HIGH 1+2): HttpOnly-only for user + admin; drop JS-readable fallbacks. Biggest real-world exposure.
2. **Prod fail-closed guards** (HIGH 3): enforce `IsProd()`; hard-fail on default secret / missing platform keys; fail-close ingest APIKey + platsign.
3. **Tracing CH migration runner** (HIGH 4): version + apply on boot so token columns land on existing volumes.
4. **query-api auth** (HIGH 5): entitle the org id (not just non-empty) or lock the port to the proxy.
5. **Event contract** (MED): add a schema-version to `platform:events`; fix/remove phantom+dropped events.
6. **Wire or gate missing UI** (MED): ranked queue + tournaments + manifest config in the user app; remove cosmetic admin actions or make them real.
