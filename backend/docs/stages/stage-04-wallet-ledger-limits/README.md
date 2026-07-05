# Stage 4 — Wallet, Double-Entry Ledger & Spending Limits

> **Goal:** coins become real and safe. A double-entry ledger moves every coin,
> escrow locks stakes, winners get paid (minus rake), and **seven server-enforced
> limits** make a runaway/compromised agent unable to drain its owner.

**Maps to:** Plan Phase 0, Week 3; Coin Economy §6; Ledger §7.
**Depends on:** Stage 3 (matches to stake/settle), Stage 1 (agents/limits).
**Unblocks:** Stage 5 (top-ups credit the ledger), funded play (Stage 5+).

## Scope
**In:** `wallets`, `ledger_transactions`/`ledger_entries`, the ledger service (the
only coin-mover), escrow at match start, settlement at match end (winner+rake),
refunds/aborts, the 7 limits enforced at join, cooldown, reconciliation job.
**Out:** Stripe/real money in (Stage 5) — coins here are granted by a test/admin
mint until Stage 5 wires payments.

## Design references
- [data-model.md](../../architecture/data-model.md) (ledger tables, invariants, system wallets)
- [security.md](../../architecture/security.md) (money controls, validation gate, limit firewall)
- [api-surface.md](../../architecture/api-surface.md) (`/v1/wallet`, history)

## The seven server-enforced limits (all checked at join, in order)
1. balance ≥ bid + `min_wallet_balance`
2. bid ≤ `coin_limit_per_match`
3. today's losses < `daily_loss_limit`
4. session losses < `session_loss_limit`
5. not in `cooldown` (after `cooldown_losses` losses → `cooldown_seconds`)
6. active matches < `max_concurrent_matches`
7. bid ≤ `max_bid`
Any failure ⇒ a specific `409`/`402` error; no coins move.

## Tasks
- [ ] Migrations: `wallets`, `ledger_transactions`, `ledger_entries`; seed system
  wallets (`stripe_clearing`, `platform_revenue`, `escrow`); create an agent wallet on agent creation (backfill).
- [ ] `internal/ledger`: `Post(txn)` = atomic, balanced, idempotent (`idempotency_key` unique), `SELECT … FOR UPDATE` on affected wallets, `CHECK(balance>=0)`; rejects unbalanced/negative.
- [ ] `internal/wallet`: `Stake(agent,bid,matchID)` → debits agent, credits escrow (`idem stake:{match}:{agent}`); `Settle(matchID,winner,rake)` → escrow→winner(+) & platform_revenue(rake) (`idem settle:{match}`); `Refund(matchID)` for aborts/ties (50/50) (`idem refund:{match}`).
- [ ] Implement the **money hook interfaces** Stage 3 stubbed: stake at match start, settle at finalize, refund on abort.
- [ ] Limit engine: implement the 7 checks at `lobby/join` (replaces Stage 3 stub); daily/session loss accounting from ledger; cooldown state in Redis/DB.
- [ ] **Validation gate** before any settlement (escrow present, result signed + replay_hash, no dispute, idempotency unused, anti-collusion stub→Stage 9).
- [ ] `/v1/wallet` (agent: read-only balance + limit usage; user: full headroom + history); `/v1/wallet/history` from ledger.
- [ ] Admin/test mint endpoint (gated, non-prod) to grant coins until Stage 5.
- [ ] Nightly reconciliation job: assert `wallet.balance == Σ entries`; alert on drift.

## Data model delta
`wallets`, `ledger_transactions`, `ledger_entries` (+ system wallets seed).

## API delta
`GET /v1/wallet`, `GET /v1/wallet/history`; (admin mint, non-prod).

## Acceptance criteria
- Staking 50 coins moves exactly 50 agent→escrow; settling a 100-coin pool pays
  the winner 95 and platform 5; **all entries sum to zero**; no wallet negative.
- Re-posting any settlement/stake with the same idempotency key is a **no-op**
  (single effect), proven by a replay test.
- Each of the 7 limits **blocks the offending join** with the correct error and
  moves zero coins; an agent **cannot** change any limit (user-only).
- A tie match refunds both stakes (50/50) idempotently.
- Nightly reconciliation reports **zero drift** on a seeded dataset; injected drift
  is detected and alerts.
- Concurrent stakes on the same wallet never oversell (race test with `-race`).

## Test plan
- Ledger unit: balanced/unbalanced rejection, negative rejection, idempotency.
- Integration: full stake→play→settle on real PG; assert balances + entries.
- Limits: table test all 7, boundary values, cooldown timing.
- Concurrency: parallel stakes/settles under `-race`; `FOR UPDATE` correctness.
- Reconciliation: seed drift → detected.

## Observability
- `coins_staked_total`, `rake_total`, `wallet_negative_attempts_total` (=0),
  `ledger_imbalance_detected_total` (=0, pages on any increment),
  `limit_block_total{limit}`, `match_settlement_seconds`.

## Security
- Ledger is the only coin-mover; idempotency on every txn; `FOR UPDATE`; limit +
  scope firewalls; validation gate; reconciliation as a safety net.

## Risks
- Money bug = trust death → invariants enforced in DB *and* code, idempotency
  everywhere, reconciliation alerting, never auto-correct (freeze + page).

## Definition of Done
Full stake/settle/refund path correct + idempotent + reconciled; 7 limits enforced
server-side and unmodifiable by agents; race-clean; acceptance criteria pass.
