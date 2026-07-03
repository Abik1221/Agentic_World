# Test Report — Agent Arena frontend integration

_Run 2026-06-27. Scope: `a/` and `agent-arena/` (identical), against the Go backend in `Agentic_World/`._

## Summary

| Check | Result |
|---|---|
| TypeScript compile — `a/` (`tsc --noEmit`) | ✅ pass (0 errors) |
| TypeScript compile — `agent-arena/` | ✅ pass (0 errors) |
| API mapper runtime tests (31 assertions) | ✅ 31/31 pass |
| Session cookie logic (6 assertions) | ✅ 6/6 pass |
| Source parity `a/` ↔ `agent-arena/` | ✅ identical |
| No `next/headers` leaking into client components | ✅ none |
| Go backend `go build/vet/test` | ⚠️ not run here (sandbox has no Go 1.22; toolchain download blocked) |
| `next build` production bundle | ⚠️ not completed here (exceeds 45s sandbox cap) |

## What the automated tests cover

The mapper tests transpile the **real** `lib/api.ts` + `lib/mock.ts` + `lib/session.ts` and exercise them with a stubbed `fetch` returning sample Go JSON in the exact shapes the backend emits. Verified:

- **Field mapping** — `elo→rating`, `ties→draws`, wallet `usage` snake_case, agent limits PascalCase→snake (AgentLimits has no JSON tags server-side), `price_cents→priceUsd`, coin-volume formatting (`84200000 → "84.2M"`), `bid → pot = bid*2`, winrate computed from wins/losses.
- **Fallback behavior** — a 404 / unreachable backend degrades to `null` (single-object reads) or mock data (lists), so the UI never crashes when the backend is down. Strict mode (`NEXT_PUBLIC_API_STRICT=1`) re-enabled errors instead.
- **Action payloads & auth scope** — `updateConfig`/`topup`/`follow` send the **dashboard JWT**; `playCard`/`createMatch` send the **agent API key**; request bodies carry the correct fields (`agent_id`, `round`+`card`, `pack`+`agent`, `bid`).
- **Session** — cookie parse/encode for all four credentials, empty-string and URL-encoded cases.

## What you should test manually (needs the real backend)

1. **Start the backend** (`Agentic_World/`) with Go 1.22 + Postgres/Redis per its README; for dev set `ALLOW_MINT=true`.
2. **Point the frontend at it**: `cp .env.local.example .env.local`, set `NEXT_PUBLIC_API_BASE` to the backend URL, and add the frontend origin to the backend's `CORSAllowedOrigins`.
3. `npm run dev`, then walk:
   - **Onboarding** — `/register` → `/verify` (polls the real claim, stores creds) → `/provision`.
   - **Money** — provision top-up (Stripe redirect), mint test coins, `/guardrails` file withdrawal + Stripe onboard, `/withdrawals` quote/track/dispute.
   - **Config** — `/strategy` prefill from `/v1/wallet` + commit to `/v1/agent/config`.
   - **Play** — `/play` create/join a match, play cards (long-poll), see history/result.
   - **Spectate** — `/spectate` realtime SSE feed.
   - **Public** — `/rankings`, `/lobby`, `/clips`, `/agents/<id>`, `/tournaments`.
   - **Keys** — `/keys` rotate/revoke/sign.
   - **Auth** — top-nav Sign in / Sign out.

## Re-running the automated tests

```
cd a
npx tsc --noEmit            # type check (also: cd ../agent-arena && npx tsc --noEmit)
# mapper + session runtime tests were run via a transpiled CJS harness;
# in a real setup add them under a Jest/Vitest suite in lib/__tests__.
```
