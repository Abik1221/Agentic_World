# Beta end-to-end verification checklist

Everything below is **build- and unit-test-verified** (backend 57 pkgs, SDK 46,
tracing, client + web tsc all green). This checklist covers the last mile that
needs a **live stack** (Postgres + ClickHouse + the agent socket + Super_Admin),
which can't be exercised in CI-only. Run top to bottom.

## 0. Bring up + migrate
- [ ] Postgres + Redis up; `alembic`/golang-migrate applies **through 0053**
      (0050 autoplay, 0051 pindex-intelligence, 0052 autoplay-schedule/stops,
      0053 benchmark tokens). Confirm `agent_autoplay`, `agent_match_benchmark`,
      `developer_pindex.intelligence_c`, and the new autoplay columns exist.
- [ ] ClickHouse (`tracing/backend`) applies **through 004** — `agent_benchmarks`
      has `prompt/completion/reasoning/total_tokens`.
- [ ] Env: `AUTOPLAY_ENABLED=true`, `PYYOL_LENS_ENABLED=true` + `_ENDPOINT`/`_API_KEY`/`_ORG`,
      and (for live-vs-live) `RANKED_AUTODRIVE=true`.

## 1. Owner stop-loss + amounts (coins)
- [ ] `/strategy` page: edit `coin_limit_per_match`, `max_bid`, `min_wallet_balance`,
      `daily_loss_limit`, `session_loss_limit` → save → `POST /v1/agent/config` 200.
- [ ] `max_bid > coin_limit_per_match` is rejected (409).
- [ ] Reload → values persisted (`GET /v1/wallet` reflects them).
- [ ] Play a ranked match at a stake > `daily_loss_limit` remaining → join is blocked (409, `daily_loss_limit`).

## 2. Deployed-agent "when to play / when to stop"
- [ ] Auto-play card: set mode=ranked, a bid, `active_from/until` window, `daily_match_cap`,
      `daily_token_budget`, `daily_loss_stop`, `take_profit` → save → `PUT /v1/agent/autoplay` 200.
- [ ] Deploy the agent: `pyyol serve` (worker) **or** `pyyol publish` a hosted endpoint, then `pyyol autoplay on`.
- [ ] Inside the active-hours window → the reconciler enqueues/starts matches; **outside it → nothing** (check logs: "autoplay: paused reason=outside active hours").
- [ ] Hit `daily_match_cap` → stops ("daily match cap reached"). Same for token budget, loss-stop, take-profit.
- [ ] Restart the server mid-day → settings + today's counts persist (no double-play).

## 3. Tracing (Pyyol Lens) end-to-end
- [ ] After matches, Lens receives `benchmark_recorded` per seat; `agent_benchmarks` rows carry token columns (`total_tokens > 0`).
- [ ] Web UI is the **black theme**; the leaderboard shows token usage for arena agents.
- [ ] Open a match's trace → the **"agent decision trail"** link → `/matches/{id}` shows per-move action/outcome/latency/**reasoning**.

## 4. P-Index intelligence (deliberate go-live)
- [ ] Run several ranked matches; confirm `agent_match_benchmark` fills (decisions/legal/fallbacks/latency/tokens).
- [ ] Sanity-check computed intelligence sub-scores look sane (query `Inputs` for a dev, or read the recompute log).
- [ ] **Only then** flip v2 live:
      `UPDATE pindex_config SET active=false WHERE version=1; UPDATE pindex_config SET active=true WHERE version=2;`
      Watch the next recompute; confirm reputations shift sensibly. Reversible (flip back).

## 5. Ranked live-vs-live (money path)
- [ ] Two agents (one socket `pyyol serve`, one hosted endpoint) auto-queue; a paired match is driven end-to-end; escrow + settle correct; both transports work (`match/drive.go` seatDriver).

## 6. Super-admin (comprehensive + integrated)
- [ ] Super_Admin logs in; each page (Dashboard, Agents, Users, Coins, Payments, Fraud, Games, Season, Tournaments, Analytics, System) shows **live** arena/Lens data (not placeholder).
- [ ] Suspend an agent via the signed **config bus** → arena `IsSuspended` blocks its ranked play.
- [ ] A pending withdrawal: approve requires a **different** admin (maker-checker); reject returns escrow.
- [ ] Open a dispute → resolve (refund/release/reject) → ledger reflects it.

## Known follow-ups
- Take-profit reads net coins today (wired); confirm the ledger `coins_delta` sign matches expectation on a real match.
- Tracing black-theme: eyeball the inline-styled sub-pages (cost-analytics, flame graph) for any residual light spots.
