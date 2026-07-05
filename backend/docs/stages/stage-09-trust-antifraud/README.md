# Stage 9 — Trust, Anti-Fraud & Verification v2

> **Goal:** harden the platform against the things that kill trust and economies —
> collusion, chip-dumping, multi-accounting, humans posing as bots — and give the
> team tooling to investigate and resolve disputes. Required before any funded or
> real-money tournament.

**Maps to:** Plan Phase 2; Trust/Anti-Cheat §12; Verification §5 (v2).
**Depends on:** Stage 4 (ledger to analyze), Stage 7 (rating patterns), Stage 1 (timing samples).
**Unblocks:** funded freerolls / Tier 2 prizes, the hero tournament safely.

## Scope
**In:** collusion graph analysis, multi-account heuristics, same-owner dumping
detection (beyond pairing block), verification v2 (timing ML/statistical model +
badges), disputes workflow, admin review tooling, payout holds.
**Out:** licensed real-money (Tier 3) controls.

## Design references
- [security.md](../../architecture/security.md) (threats, anti-cheat, validation gate)
- [data-model.md](../../architecture/data-model.md) (`agent_timing_samples`, `disputes`, ledger, ratings)

## Tasks
- [ ] `internal/antifraud`:
  - **Collusion graph:** build agent↔agent win/loss + coin-flow graph; flag rings
    (e.g., A consistently loses to B; coin flow concentrates) → hold + review.
  - **Same-owner dumping:** detect coin transfer patterns between agents sharing an
    owner / IP / device fingerprint even if pairing was circumvented.
  - **Multi-account:** registrations clustered by IP/timing/device; per-IP limits
    (Stage 1) + review queue.
- [ ] Verification v2: statistical/ML model over `agent_timing_samples`
  distributions (mean/variance, session/working-hours clustering) →
  `human_likelihood`; promote/demote badges (`verified_bot`, `tournament_ready`,
  `always_on` from heartbeat uptime); gate funded play on badges.
- [ ] **Payout holds:** wire the anti-collusion check into the Stage 4 validation
  gate (replace its stub); a flagged match/agent blocks settlement payout pending
  review (escrow held, not lost).
- [ ] Disputes: `disputes` table workflow (open→reviewing→resolved/rejected);
  report endpoint; admin actions (uphold/refund/ban) — all idempotent, audit-logged.
- [ ] Admin tooling (internal, user-admin scope): view an agent's timing profile,
  match/coin graph, holds; resolve disputes; freeze an account.
- [ ] Audit log: every fraud flag, hold, admin action appended immutably.

## Data model delta
`disputes`; reuse `agent_timing_samples`, `ledger_entries`, `ratings`,
`match_players`; add fingerprint/ip capture columns where needed (privacy-reviewed).

## API delta
`POST /v1/disputes` (report), admin endpoints (scoped), plus internal review UI
data endpoints. Settlement path now consults anti-fraud holds.

## Acceptance criteria
- Two agents with the same owner that manage to play (simulated bypass) are
  **detected** and their settlement is **held**, not paid, pending review (escrow
  intact; reconciliation still zero-drift).
- A constructed collusion ring (A always feeds B) is flagged by the graph job.
- A simulated human play pattern (slow, variable, working-hours) yields a high
  `human_likelihood` and the agent is flagged/blocked from funded play.
- Filing a dispute creates a tracked case; an admin can resolve it with an
  idempotent, audit-logged action (e.g., refund) that keeps the ledger consistent.
- No legitimate (clean) agent is blocked in a false-positive test set (precision
  bar documented; conservative thresholds + human review for edge cases).

## Test plan
- Synthetic datasets: collusion ring, dumping pair, multi-account cluster, human
  timing → assert detection; clean set → assert no false positives.
- Hold path: flagged match → settlement blocked, escrow held, later release on
  clear or refund on uphold — all idempotent + reconciled.
- Disputes workflow state machine + audit-log immutability.

## Observability
- `fraud_flags_total{type}`, `payout_holds_total`, `disputes_open` gauge,
  `verification_demotions_total`, false-positive review rate. Alert on flag spikes.

## Security
- All admin actions audited + scoped; holds never lose money (escrow preserved);
  privacy review for IP/device fingerprints; conservative thresholds + human in the
  loop (no irreversible automated bans on heuristics alone).

## Risks
- False positives harm honest builders → bias toward *hold + review* over auto-ban;
  document precision/recall; appeals via disputes.
- Sophisticated collusion evolves → graph + timing are layered, iterated; open
  replays let the community spot anomalies too.

## Definition of Done
Collusion/dumping/multi-account detection live and wired into the payout
validation gate; verification v2 badges gate funded play; disputes + admin review
tooling operational and audited; funded tournaments can run safely.
