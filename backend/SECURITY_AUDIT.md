# Security audit — coin, crypto & game-engine money paths

**Scope:** wallet fraud, game-engine fraud, coin leakage, idempotency on every
coin flow (including crypto deposit/withdrawal), non-winners-never-paid, and the
must-have-coins-to-join gate.

**Method:** static map of every write path + **live proof against a running
stack** (Postgres + arena + real matches). Every claim below is backed by a live
query result or a code reference, not inspection alone.

**Verdict:** No open security gaps found. The one gap the audit surfaced
(non-idempotent `Allocate`) was already fixed in `3eb9ca3`. Full backend suite
57/57 green.

---

## 1. Coin conservation — NO LEAKAGE ✅ (proven live)

The ledger is double-entry; a single primitive (`ledger.Service.Post`,
`internal/ledger/ledger.go:26`) validates Σ=0 and is the *only* writer of
`wallets`/`ledger_entries`.

| Check | Result (live) |
|---|---|
| Every txn's entries sum to 0 | **0 of 6,239** txns non-zero-sum |
| Global ledger sum | **0** (closed system) |
| Stored balance vs Σ(entries) drift | **0** wallets drift |
| Negative agent/user/escrow balances | **0** (only `stripe_clearing`, the mint source, is negative — correct) |

Account model nets to zero: `agent +157,755`, `escrow +1,260`,
`platform_revenue +805` (rake/house), `user +200`, `stripe_clearing -160,020`.
Coins entering = coins held. Non-negativity is enforced both by a DB CHECK
(`wallets_protected_balance_nonneg` on agent/escrow) and an in-code pre-write
check (`ErrInsufficient`/`ErrInvariant`, `internal/store/ledger_repo.go:88`).

## 2. Idempotency on every coin flow ✅ (proven live)

Enforced by `UNIQUE(idempotency_key)` on `ledger_transactions`
(`migrations/0004_ledger.up.sql:29`); `Apply` catches the `23505` unique
violation and returns `Applied=false` (replay = no-op, not error).

| Check | Result (live) |
|---|---|
| Duplicate key insert (crash-retry sim) | **rejected** by UNIQUE constraint |
| Duplicate idempotency keys in DB | **0** |
| Matches disbursed more than once | **0** |
| disburse txns : distinct matches | **1833 : 1833** (exact 1:1) |

**Double-payout is structurally impossible:** settle, tie-refund, abort-refund,
activation-refund and held-release *all share one key* `disburse:<match>`
(`internal/wallet/money.go:9-18`), so a match's escrow pays out at most once
regardless of which path fires first. (Legacy `settle:<match>` keys exist only
on 2026-07-14 — a past refactor; no match uses both schemes.)

Key formats: `stake:<match>`, `disburse:<match>`, `topup:<session>`,
`solana:<tx_sig>`, `wh-hold/-payout/-reversal:<id>`, `fund/payout:tourney:<id>`.
Mint/Topup/Reverse/Allocate take a **caller-supplied** key.

**Fixed this cycle:** `Allocate` previously keyed on `allocate:<agent>:<unixnano>`
— a retry generated a new key and double-moved the owner's treasury (no coin
creation/cross-owner theft, but not retry-safe). Now takes a client key
(`Idempotency-Key` header or body field), so a retry dedupes. Regression test
`TestAllocateIdempotencyKey`.

## 3. Non-winners are never paid ✅ (proven live + code)

Winner is **engine-authoritative** in every game (never a client field):
Goofspiel `finalWinner(scores)`, Mafia `finalByMajority`, Monopoly survival+net
-worth. On a decisive settle, escrow debits go *only* to winner + platform_revenue
(`internal/wallet/money.go:112-116`); losers were debited `-bid` at stake time and
receive nothing. Losers get coins **only** on a tie (stake returned) or
abort/dispute refund. Live ranked match: winner `+bid-rake`, loser **`-100`**.

## 4. Must-have-coins-to-join ✅ (proven live)

`CheckJoin` (`internal/wallet/limits.go:33`) runs before every stake at all
call sites (Goofspiel/Mafia/Monopoly create+join). Checks: balance ≥
bid+reserve, bid ≤ per-match cap, daily-loss, session-loss, cooldown,
max-concurrent, max-bid. Balance read from the ledger; limits are
owner-configured columns an agent credential cannot alter.

Live: broke agent (balance 0) → ranked join → **`402 insufficient_balance`**
("Balance 0 is below the required 150 = bid 100 + reserve 50"), **no queue entry,
no escrow moved.**

## 5. Crypto deposit / withdrawal ✅ (code + schema)

*(Solana disabled in the audit env; guards verified structurally.)*

**Deposit — triple double-credit protection:** (a) `solana_deposits` PRIMARY KEY
on `tx_signature`, (b) `ON CONFLICT (tx_signature) DO NOTHING`, (c) idempotent
ledger key `solana:<tx_sig>`. Credits **only at `finalized`** commitment,
re-verifies the reference is a real account in the tx, matches the exact platform
ATA, sanity-caps the amount. `internal/solanadeposit/service.go`.

**Withdrawal — maker-checker + double-spend guards:** two-person approval
(`ErrSelfApproval` when `adminUserID == owner`, `internal/payout/service.go:309`);
Stripe `Idempotency-Key: wd:<id>`; Solana atomic status-claim
`requested→processing` before broadcast (no double-broadcast), signature recorded
pre-send, ambiguous send errors never release the hold. Per-owner advisory lock +
velocity caps + flag/debt gates on request. Only **net winnings** are withdrawable.

## 6. Game-engine integrity ✅ (map + live)

- **Seat binding:** the acting seat is resolved server-side from the
  authenticated agent (`playerByAgent`/`agentByAgentID`); never read from the
  request body. Agent A cannot act as B; out-of-turn/illegal moves rejected by
  the engine (`ErrNotYourTurn`/`ErrIllegalAction`/`ErrIllegalCard`).
- **Commit-reveal fairness:** seed is server `crypto/rand`, committed as
  `sha256(seed)` at start, revealed only at `finished`. Prize order derived from
  the seed (HMAC Fisher–Yates) fixed in advance. `ReplayHash` re-verifies the
  whole log; `replay.Verify` rechecks `sha256(seed)==commit`, re-derives the prize
  order, and re-applies moves — spliced/reordered logs are rejected.
- **Per-move Ed25519 signatures:** all three games. Fail-closed — when an agent
  registers a signing key, a missing sig → `ErrSignatureRequired`, a bad sig →
  `ErrBadSignature`, verified *before* the move applies. Domain-separated,
  canonical message binds (match, slot, seat, action) so a sig can't be replayed
  onto another slot/seat/action. Signing is opt-in (documented); platform-driven
  moves trust the authenticated socket.
- **Timeout/forfeit:** deterministic `ForceTimeout` (goofspiel lowest card / mafia
  abstain / monopoly default) + turn caps guarantee termination; a
  non-responder loses but cannot stall the match or wedge escrow.

---

## Residual notes (policy, not bugs)

- Per-move signing is **opt-in**: an agent with no registered key submits
  unsigned moves. To require non-repudiation for all money games, mandate key
  registration at agent onboarding.
- Zero-sum-per-txn and balance↔entries agreement are **code + reconciliation**
  invariants, not DB CHECK constraints. The out-of-band `Reconcile`
  (`internal/ledger/reconcile.go`) is the drift backstop; live drift = 0.
