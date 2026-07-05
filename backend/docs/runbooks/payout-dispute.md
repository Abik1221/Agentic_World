# Runbook — Payout hold / dispute

**Triggers:** `payout_holds_total` / `payout_holds_placed_total` spike, or a user
files a dispute (`POST /v1/disputes`).

**Severity:** P3 unless holds spike broadly (then suspect a false-positive rule).

## A held payout
- A finished match whose settlement the anti-fraud gate held: stakes stay in
  **escrow**, no payout, `coins_delta` shows the *pending* amount. Money is safe.
- Find the reason: `payout_holds.reason` (`same_owner`, `flagged_agent`,
  `gate_error`) and the `fraud_flags` on the agents.

## Resolve (admin, audited, idempotent)
Use `POST /v1/admin/disputes/{id}/resolve { "action": ... }`:
- **release** — the hold was a false positive. Pays the champion/winner as
  originally determined (`SettleHeld`, idempotent `settle:{m}`); hold → `released`.
- **refund** — collusion/abuse upheld. Returns both stakes (`Refund`, idempotent
  `refund:{m}`); hold → `refunded`.
- **reject** — close the report with no money movement.

Re-running a resolve is a no-op (the dispute is already terminal). Every action is
written to `audit_log` with the admin's id.

## Spike of holds (false positives)
- If many clean matches are held, a detection threshold is too aggressive. The
  platform biases to hold + review, so no money is lost — but tighten the rule:
  inspect `fraud_flags` by `type`, raise `COLLUSION_MIN_GAMES` or the win-ratio
  threshold, and re-run the detector. Document the precision change.

## Verify
- Resolved disputes reach `resolved`/`rejected`; holds reach `released`/`refunded`;
  reconciler zero-drift; affected balances correct.
