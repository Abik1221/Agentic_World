# Runbook — Ledger imbalance

**Alert:** `ledger_imbalance_detected_total` increments (nightly reconciler or the
`ErrInvariant` path). This means a wallet's cached balance disagrees with the sum
of its entries, OR a protected wallet (agent/escrow) would have gone negative.

**Severity:** P1. Money correctness is the product's trust anchor.

## 1. Freeze
- Flip the kill switch for new value-moving actions (block `lobby/create`,
  `lobby/join`, `wallet/topup`). In-flight matches may finish; no new stakes.
- Do **not** restart instances hoping it clears — the drift is in the data.

## 2. Locate
- Find the drifted wallet(s): the reconciler logs `wallet_id`, `kind`,
  `cached_balance`, `entry_sum`, `delta`.
- Pull that wallet's entries newest-first; find where `running_sum(entries)`
  diverges from the balance updates. The offending `txn` is the seam.

## 3. Diagnose (common causes)
- A non-idempotent write slipped a second effect → look for two txns with the same
  intent but different `idempotency_key`.
- A manual/admin edit (should never happen) → check `audit_log`.
- A bug in a new ledger caller → the `kind` + metadata identifies the module.

## 4. Correct
- Post a **compensating, balanced** ledger transaction (new idempotency key,
  `kind=adjustment`, metadata explaining the incident + ticket). Never UPDATE a
  balance directly.
- Re-run the reconciler; confirm zero drift.

## 5. Verify & unfreeze
- `ledger_imbalance_detected_total` stops incrementing; reconcile reports clean.
- Lift the freeze. File a post-incident note; add a regression test for the seam.
