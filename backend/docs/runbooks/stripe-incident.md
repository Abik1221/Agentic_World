# Runbook — Stripe incident

**Alerts:** `stripe_webhook_failures_total` spike, or `reconcile_unmatched_total > 0`
(hourly payments reconciler found a paid session with no credit).

**Severity:** P2 (money in; eventual correctness is guaranteed by reconciliation).

## Webhook failures spiking
- **Bad signature (400s):** verify `STRIPE_WEBHOOK_SECRET` matches the endpoint's
  signing secret in the Stripe dashboard. A rotated secret is the usual cause.
- **5xx (we asked Stripe to retry):** check DB/ledger health — processing failed,
  not verification. Stripe retries with backoff; the event is already persisted
  (persist-first), so recovery is automatic once the downstream is healthy.
- Replay manually if needed: `stripe events resend <evt_id>` (Stripe CLI).

## Unmatched charges (reconcile_unmatched > 0)
- A completed Checkout never produced a (received) credit. The hourly reconciler
  calls `coiner.Topup` with the **session-id** key (`topup:{session}`), the same
  key the webhook uses, so it self-heals on the next run.
- If it persists: confirm the session metadata carries `agent` + `coins` (set in
  `CreateCheckout`); a missing/blank metadata session can't be auto-credited →
  credit via admin mint with a note, and fix the checkout creation path.

## Refund / chargeback
- `charge.refunded` / `charge.dispute.created` reverse the top-up
  (`reversal:{event}`). If the agent already spent the coins, the wallet can't go
  negative → `stripe_reversal_shortfall_total` increments and the account is
  flagged. Follow up via the disputes/anti-fraud path.

## Verify
- `stripe_webhook_failures_total` flat; `reconcile_unmatched_total` back to 0;
  affected agents' balances correct; ledger zero-drift.
