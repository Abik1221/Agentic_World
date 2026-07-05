# Runbooks — Agent Arena

On-call playbooks for the failure modes that matter. Each is short, copy-pasteable,
and ends with a verification step. Dashboards and alert definitions live in
[observability-sre.md](../architecture/observability-sre.md).

| Runbook | Trigger / alert |
|---|---|
| [Ledger imbalance](ledger-imbalance.md) | `ledger_imbalance_detected_total > 0` |
| [Stuck match](stuck-match.md) | match active past its deadline; sweeper not advancing |
| [Stripe incident](stripe-incident.md) | `stripe_webhook_failures_total` spike / `reconcile_unmatched_total > 0` |
| [Payout hold / dispute](payout-dispute.md) | `payout_holds_total` spike; dispute filed |
| [Deploy rollback](deploy-rollback.md) | bad deploy; error-rate or latency SLO burn |

## Golden rules
1. **Never auto-correct money.** On any ledger drift: freeze writes, page, investigate
   from the event log + ledger entries. Do not hand-edit balances.
2. **Holds preserve money.** A held payout keeps stakes in escrow — release or refund
   only through the admin dispute path (idempotent, audited).
3. **Replays are truth.** Every match is reconstructable from `match_events`; use the
   public `/v1/match/{id}/replay` + `replay_hash` to settle "did X really happen".
4. **Idempotency everywhere.** Re-running a settle/refund/topup/payout is safe — keys
   (`settle:{m}`, `refund:{m}`, `topup:{event}`, `payout:tourney:{id}`) guarantee single effect.
