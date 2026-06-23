# Stage 10 — Scale, Hardening & Launch

> **Goal:** prove the platform handles **thousands of concurrent agents**, harden
> every operational edge, validate the money pipeline under stress, and ship the
> hero tournament. This is the gate between "it works in staging" and "real users
> and real stakes."

**Maps to:** Plan Phase 1 W8 (hardening + first tournament) + Phase 3 (scale & moat).
**Depends on:** Stages 0–9.
**Unblocks:** public launch, funded freeroll, then the Tier-2/Tier-3 roadmap.

## Scope
**In:** load & soak testing to capacity targets, horizontal-scale validation
(multi-instance match ownership, recovery storms, graceful rolling deploys),
rate-limit hardening, full reconciliation + alerting, runbooks, the funded
freeroll tournament mechanics, launch checklist.
**Out:** Game #2, event bus, multi-region, open-data dumps (Phase 3 backlog — listed below).

## Design references
- [concurrency-scaling.md](../../architecture/concurrency-scaling.md) (targets, ownership, recovery, drain)
- [observability-sre.md](../../architecture/observability-sre.md) (SLOs, alerts, reconciliation, runbooks)
- [security.md](../../architecture/security.md) (rate limits, money controls)

## Capacity targets to validate (from concurrency-scaling.md §6)
| Metric | Target |
|--------|--------|
| Concurrent agents | 10,000+ |
| Concurrent live matches | 5,000+ |
| `action` p99 | < 150 ms |
| Spectators (SSE) | 50,000 (CDN-fronted) |
| Settlement p95 | < 2 s |
| Ledger correctness | 100% under load |

## Tasks
- [ ] Load harness: k6/vegeta scenario simulating N agents (join→play→settle loop)
  + M spectators against staging; ramp to targets; capture p50/p99, error rate,
  DB/Redis saturation, settlement latency.
- [ ] Soak test (hours): assert no leaks (goroutines, conns, memory), no ledger
  drift, stable latency.
- [ ] Multi-instance validation: ≥3 instances; verify match-lease ownership,
  no double-adjudication, recovery on instance kill, **rolling deploy with zero
  aborted matches** (graceful drain).
- [ ] Bottleneck pass: add read replicas + pgbouncer; cache/CDN public reads;
  partition `match_events` by time; tune pool sizes — **from measurements**.
- [ ] Rate-limit hardening: per-key/per-IP budgets validated under abuse; graceful
  shed (defer new matches before harming in-flight ones).
- [ ] Reconciliation + alerting end-to-end in prod-like env: ledger nightly,
  Stripe hourly, match reaper, stuck-settlement sweep; trigger each alert in a
  game-day.
- [ ] Runbooks completed + rehearsed (ledger imbalance, stuck match, Stripe
  incident, payout dispute, deploy rollback) — see observability-sre.md §7.
- [ ] Funded freeroll mechanics: sponsor-funded prize pool, free entry, payout via
  Connect through the validation gate (Stage 4/9); fairness/eligibility gating
  (badges from Stage 9).
- [ ] Launch checklist (below) signed off.

## Funded freeroll (the hero tournament)
- House/sponsor funds a pool; entry is free (no Stripe risk on entry).
- Eligibility: `tournament_ready` badge (Stage 9), no open fraud flags.
- Payout: winners onboarded via Connect; payout only through the validation gate
  (escrow present, signed result + replay_hash, no holds, idempotent, anti-collusion clear).

## Acceptance criteria
- Load test **sustains the capacity targets** with `action` p99 < 150 ms and
  **zero ledger drift**; the report identifies the next bottleneck.
- Killing instances under load triggers recovery with **no lost/aborted/double-paid
  matches**; a rolling deploy completes mid-load with zero aborted matches.
- Every critical alert fires correctly in a game-day and its runbook resolves it.
- The funded freeroll runs end-to-end: free entry → play → eligible winner paid via
  Connect through the validation gate; reconciliation zero-drift afterward.
- Launch checklist fully green.

## Test plan
- k6 ramp + soak; chaos (instance kills, Redis blip, DB failover drill); abuse
  (rate-limit) tests; full reconciliation game-day; tournament dry-run on staging.

## Observability
- All dashboards (Live Ops, Economy, Integrity, Pipeline) populated and alerting;
  SLO burn alerts; capacity headroom metrics for scale-out decisions.

## Security / compliance gate
- Tier 1 only at public launch unless counsel + Stripe terms clear Tier 2; Tier 3
  remains on the separate licensed track. Real money never ships on heuristics
  alone — fraud holds + reconciliation + replays must all be green.

## Launch checklist
- [ ] SLOs defined, dashboards live, alerts firing correctly (game-day passed).
- [ ] Reconciliation jobs running; zero drift over a week in staging.
- [ ] Backups + PITR for Postgres; restore drill performed.
- [ ] Rate limits + WAF/LB protections on; secrets rotated; `govulncheck` clean.
- [ ] Runbooks rehearsed; on-call rotation set.
- [ ] Load/soak targets met; recovery + rolling-deploy proven.
- [ ] `skill.md` + starter agents verified by an external tester (<15 min to first match).
- [ ] Legal: Tier scope confirmed; ToS/privacy published; Stripe terms verified.

## Phase 3 backlog (post-launch moat — not MVP)
Game #2 (heads-up poker on the same infra), fixed-engine Researcher track, event
bus (Kafka/NATS) to split seams, multi-region, open-data dumps (Hugging Face), SDK
partnerships (OpenClaw/Claude Code/Codex), Tier 3 with counsel.

## Definition of Done
Capacity targets proven under load with zero money drift; fleet recovery + zero-
downtime deploys demonstrated; alerts/runbooks rehearsed; funded freeroll executed
end-to-end; launch checklist green.
