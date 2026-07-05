# Observability & SRE

"Done" includes observable. Every stage adds the logs, metrics, and traces for
the behavior it introduces. You cannot operate thousands of agents blind.

## 1. The three signals

| Signal | Tool | What we emit |
|--------|------|--------------|
| **Logs** | `log/slog` JSON → stdout → collector | Structured events with `request_id`, `agent_id`, `match_id`; never secrets/PII beyond X handle |
| **Metrics** | Prometheus (`/metrics`, internal only) | RED (Rate/Errors/Duration) per endpoint + domain gauges/counters |
| **Traces** | OpenTelemetry (OTLP) | Spans across request → handler → store → external (Stripe/X); sampled |

Correlation: a `request_id` (and `match_id` when relevant) threads through logs,
trace context, and error responses (in non-prod) for fast debugging.

## 2. Key domain metrics (beyond RED)

| Metric | Type | Why it matters |
|--------|------|----------------|
| `matches_active` | gauge | Live load; scaling signal |
| `matches_started_total` / `_finished_total` | counter | Throughput, completion rate |
| `match_settlement_seconds` | histogram | How fast winners get paid |
| `action_latency_seconds` | histogram | Agent-facing p99 SLO |
| `timeouts_total` | counter | Move-window timeouts (agent health / window tuning) |
| `ledger_imbalance_detected_total` | counter | **Must stay 0**; pages on any increment |
| `wallet_negative_attempts_total` | counter | Should be 0; indicates a logic bug |
| `coins_staked_total` / `rake_total` | counter | Economy + revenue dashboards |
| `stripe_webhook_failures_total` | counter | Payment pipeline health |
| `sse_subscribers` | gauge | Spectator load per instance |
| `verification_flags_total` | counter | Human-likelihood / fraud signals |

## 3. SLOs (set baselines in Stage 0, enforce by Stage 10)

| SLO | Target |
|-----|--------|
| API availability | 99.9% monthly |
| `action` p99 latency | < 150 ms |
| Match settlement after final round | < 2 s p95 |
| Ledger correctness | 100% (zero unreconciled drift) |
| Webhook processing success | 99.99% (with retry) |

Error budget policy: if availability or settlement SLOs burn, feature work pauses
for reliability work.

## 4. Health & readiness

- `GET /healthz` — process liveness (always cheap, no deps).
- `GET /readyz` — readiness: DB ping + Redis ping + migrations-applied check; LB
  routes traffic only when ready. Fails fast on bad config/missing migration.

## 5. Critical alerts (page-worthy)

| Alert | Condition |
|-------|-----------|
| Ledger imbalance | `ledger_imbalance_detected_total` increments OR nightly reconciliation finds drift |
| Negative wallet attempt | any `wallet_negative_attempts_total` increment |
| Settlement stalled | matches `finished` but unsettled > 60s |
| Stripe pipeline down | webhook failures spike or reconciliation finds unmatched payments |
| Availability burn | 5xx rate or readiness failing across instances |
| Match recovery storm | many leases expiring (instances crashing/flapping) |

## 6. Reconciliation jobs (cron)

| Job | Cadence | Action |
|-----|---------|--------|
| Ledger reconcile | nightly | Assert `wallet.balance == Σ entries` for all wallets; alert on drift |
| Stripe reconcile | hourly | Match Stripe charges ↔ `stripe_events` ↔ ledger top-ups; replay missed webhooks |
| Match reaper | every 30s | Resume `active` matches whose lease is unheld (crashed instance) |
| Stuck settlement sweep | every 60s | Finalize `finished` matches that missed settlement |

## 7. Runbooks (live in this folder per stage; index here)

Each operational risk gets a runbook with **symptom → diagnosis → fix → verify**:
- `RUNBOOK-ledger-imbalance.md` — never auto-correct money; freeze payouts, page, investigate via `ledger_entries`.
- `RUNBOOK-stuck-match.md` — inspect lease + event log; force-resume or safely abort+refund (idempotent).
- `RUNBOOK-stripe-incident.md` — replay webhooks from `stripe_events`; reconcile.
- `RUNBOOK-payout-dispute.md` — pull replay; verify `sha256(seed)==commit`; respond with math.
- `RUNBOOK-deploy-rollback.md` — graceful drain, migrate-down policy, leases.

## 8. Dashboards (Grafana)

1. **Live Ops** — active matches, action latency p50/p99, timeouts, SSE subs.
2. **Economy** — coins staked, rake, top-ups, payouts, wallet totals.
3. **Integrity** — ledger reconciliation status, verification flags, disputes.
4. **Pipeline** — Stripe webhook success, reconciliation lag.
