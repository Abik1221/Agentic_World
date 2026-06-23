# Coin Engine — Inspection & Critique

A frank end-to-end review of the money lifecycle: **buy → stake → lose/win →
settle → cash out**. What's correct, what's buggy, what's missing.

## Resolution status
| # | Finding | Status |
|---|---|---|
| A | Cash-out missing | ✅ Built — `internal/payout` (request → admin-approve → Stripe payout) |
| B | Non-atomic staking | ✅ Fixed — `wallet.StakeMatch` (one balanced txn) + compensating `RefundStakes` |
| C | Buy/withdraw arbitrage | ✅ Closed — winnings-only withdrawable |
| D | Laundering / instant cash-out | ✅ Mitigated — winnings-only + KYC + clearing window + admin approval |
| E | No request/approval workflow | ✅ Built — `requested → approved/rejected/failed`, audited, idempotent |
| F | Chargeback debt has no ledger | ✅ Built — `bad_debt` counter-account + `debts` table; partial clawback, auto-repay from top-ups, withdrawals blocked while in debt |

## The lifecycle as it exists today

```
BUY        Stripe Checkout → webhook → ledger topup
           stripe_clearing −coins , agent +coins        (idem topup:{session})

STAKE      at Join, per player
           agent −bid , escrow +bid                     (idem stake:{match}:{agent})

WIN/LOSE   at finish (Settle)
           escrow −pool , winner +(pool−rake) , platform_revenue +rake   (idem settle:{match})
           loser: net −bid (already debited at stake)
           tie: escrow −pool , each player +bid          (no rake)

ABORT      Refund: escrow −pool , each player +bid       (idem refund:{match})

CHARGEBACK Reverse: agent −coins , stripe_clearing +coins (idem reversal:{event})

CASH OUT   ❌ DOES NOT EXIST (only Connect KYC onboarding link)
```

System wallets (migration 0001): `stripe_clearing`, `platform_revenue`, `escrow`.
Agent + escrow balances are CHECK-constrained non-negative; system clearing/revenue
may go negative (normal double-entry). The ledger is the only coin-mover; every
move is balanced (Σ = 0) and idempotent. **That core is sound.**

---

## What's correct
- Double-entry, balanced, idempotent, `FOR UPDATE`-locked posting; reconciler
  asserts `wallet.balance == Σ entries`.
- Stake/settle/tie/refund math conserves coins (rake truncation favors the winner;
  no coins vanish).
- Chargeback reversal never drives a wallet negative (rejected → flagged).
- Anti-fraud payout gate holds suspect settlements with escrow intact.

---

## Findings

### A. CRITICAL — Cash-out / withdrawal is entirely missing
Only `POST /v1/payouts/onboard` (a Connect KYC link) exists. There is **no**:
- endpoint to request a withdrawal,
- ledger movement burning coins against cash (`agent −coins, stripe_clearing +coins`),
- Stripe **Transfer/Payout** call to the user's connected account,
- `withdrawals` table, request/approval workflow, or payout-side validation gate.

The "convert coins to money and withdraw" half of the economy does not exist.
(The original plan deferred this to Tier 2/3 with counsel — we are building it now,
properly gated.)

### B. BUG — Non-atomic staking orphans escrow
`Join` posts **two separate** stake transactions then calls `Activate`:
```
Stake(player0) ; Stake(player1) ; Activate(...)
```
If `Stake(player1)` fails (a concurrent settlement elsewhere lowered the balance
between `CheckJoin` and `Stake` — a TOCTOU), or `Activate` fails, **player0's coins
sit in escrow with no match to settle or refund them.** Money is stuck.
**Fix:** stake BOTH seats in ONE balanced ledger txn (`stake:{match}`); on activate
failure, refund. Atomic — either both staked or neither.

### C. CRITICAL — Buy/withdraw rate arbitrage (money printing)
Bonus packs give more coins per dollar:
`$1=100` (1.00¢/coin) … `$50=6500` (0.77¢/coin). If cash-out pays a flat rate
(say 1¢/coin), a user buys the $50 pack and withdraws 6500 coins for **$65** — a
risk-free $15 profit, infinitely. **Any withdrawal must price at ≤ the worst buy
rate, take a withdrawal fee, and/or only allow net winnings (not bonus/deposited
coins) to be cashed out.**

### D. CRITICAL — Money-laundering / chargeback theft via instant cash-out
Buy with a card → immediately withdraw to bank → charge the card back = direct
theft, and clean laundering. Mitigations required before any cash-out ships:
- **Only winnings are withdrawable** (track a withdrawable sub-balance, or require
  coins be "played through" N times).
- **Clearing period**: no withdrawal of funds until the card-chargeback window on
  the backing deposits has passed.
- **KYC required** (Connect onboarding complete) + **anti-fraud clear** + **holds**.
- **Request → approval → execute** workflow, not instant.

### E. DESIGN — No request/approval workflow (you asked for this)
Withdrawals should mirror disputes/holds: `requested → approved → paid` (or
`rejected`), each transition idempotent and **audit-logged**, gated by the same
validation gate used for funded payouts (escrow/eligibility/no-holds/idempotent).

### F. KNOWN — Reversal shortfall has no debt ledger
If coins are spent/lost then the card is charged back, the platform eats the loss
(wallet can't go negative). Acknowledged in Stages 5/9; withdrawal makes it worse,
which is exactly why D's clearing period + winnings-only matter.

### G. DESIGN — Wallets are per-agent; money/KYC is per-user
Coins live in agent wallets; the Stripe customer/connect id lives on the user.
Withdrawal debits a chosen agent wallet (owned by the caller) and pays the
**owning user's** connected account.

---

## Proposed target design (to confirm)

**New:** `internal/payout` package + `withdrawals` table + `stripe_payout` flow.

```
REQUEST  user picks agent + coins
         validate: KYC done · agent owned · no fraud flag/hold · enough
                   WITHDRAWABLE balance · past clearing period
         ledger HOLD: agent −coins , escrow +coins   (idem withdraw:{id})   ← coins locked
         row: withdrawals(status=requested)

APPROVE  auto (gate clear) or admin; status=approved; audit

EXECUTE  Stripe Connect transfer of (coins × rate − fee) to user's account
         on success: ledger burn escrow −coins , stripe_clearing +coins (idem payout:{id})
         status=paid ; audit
         on Stripe failure: release hold (refund coins), status=failed

REJECT   release hold (coins back to agent), status=rejected, audit
```

Reconciliation stays exact: coins are held in escrow during review, burned only on
confirmed payout, or returned on reject/fail.
