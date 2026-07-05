# Runbook — Deploy rollback & rolling deploy

**Goal:** ship with **zero aborted matches**. The design makes this routine:
instances are stateless, match state lives in Postgres (snapshot + event log), and
any instance can act on any match under the per-match Redis lock.

## Rolling deploy (normal)
1. Deploy new instances alongside old (N+1), wait for readiness (`/health`).
2. Send SIGTERM to old instances one at a time. Each:
   - stops accepting new connections; the HTTP server drains in-flight requests
     within `SHUTDOWN_GRACE`;
   - cancels the root context → sweeper, reconcilers, clip/notification workers,
     and the anti-fraud detector stop cleanly.
   - **In-flight matches are NOT aborted** — they are persisted; a surviving (or
     new) instance's sweeper progresses them on the next tick.
3. Proceed once the old instance exits and live traffic is on the new set.

## Rollback (bad deploy)
- Symptom: error-rate or latency SLO burn, or a correctness alert after a deploy.
- Roll the previous image back the same way (it's just another rolling deploy).
- Schema: migrations are forward-only in spirit; a rollback to the prior binary
  must remain compatible with the new schema. If a migration is incompatible,
  prefer a **fix-forward** patch over a down-migration in prod.
- Money safety holds across rollback: idempotency keys mean any retried
  settle/refund/topup/payout is a single effect.

## Verify
- New (or rolled-back) version healthy; `action` p99 and settle p95 within SLO;
  no increase in aborted matches; reconciler zero-drift.
