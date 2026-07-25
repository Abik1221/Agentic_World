# Monopoly Cash-Game Mode — design & plan

Goal: a real-coin "cash game" Monopoly where players buy in for a variable amount,
build (or lose) real wealth on a live table, top up (rebuy), and leave whenever they
want — cashing out their share of the pot. "Rich or broke," poker-cash-game style —
**without** ever minting redeemable currency or getting us shut down.

Contrast with today's **tournament** table (already shipped): one fixed entry fee →
play with fake chips → single winner takes the pool. That stays as-is. Cash-game is a
second, opt-in mode.

---

## 0. Reality check on the engine (audited 2026-07-25)

The pure engine (`internal/engine/monopoly`) is far more complete than an earlier note
implied. Of the five "official rules we might be missing," **four are already fully
implemented** — verified in code:

| Rule | Status | Evidence |
|---|---|---|
| Auction on decline | ✅ done | `engine.go:419-496` (`PhaseAuction`, `startAuction`/`closeAuction`) |
| Even-build & even-sell | ✅ done | `engine.go:1183,1221` (`minHousesInGroup`/`maxHousesInGroup`) |
| Finite house/hotel supply (32/12) | ✅ done | `state.go:102-103`, `engine.go:122-123,1186-1192` |
| No trading property with buildings in group | ✅ done | `engine.go:604-622` (`checkTradeSide`) |
| Mortgage + bankruptcy handoff | ⚠️ partial | `engine.go:1090-1161` |

So the engine-rules work is **small**, not a big pass:
- **R5a — mortgage interest on transfer.** On bankruptcy handoff (`engine.go:1132`) and
  player trades (`engine.go:781-793`) mortgaged property moves with *no* 10% interest.
  Official rule: the receiver pays 10% immediately. Fix both transfer paths.
- **R5b — bank-creditor estate auction.** When a player goes bankrupt owing the *bank*,
  their properties return unimproved to the bank (`engine.go:1133-1135`) instead of
  being auctioned to the remaining players. Fix: run the returned estate through the
  existing `startAuction` machinery.
- **R3-opt (nice-to-have).** Scarce-house *auction* tie-break (when >1 player wants the
  last houses). Today scarcity just blocks the build. Low priority.

Everything else below is the genuinely new work: the **cash-game money model**.

---

## 1. The money model — a mutual-fund / pari-mutuel share

The board keeps running on **chips** (engine `Cash` + property + houses), so all the
classic rules — including the infinite bank paying GO salary — keep working and stay
fun. Real coins only cross the boundary at **buy-in** and **cash-out**. The trick that
makes this safe is that a player's redeemable coins are always their **share of the
pool**, never a fixed chip↔coin peg:

```
pool        = real coins held in table escrow (Σ buy-ins − cash-outs)
chips_i     = engine net worth of seat i  (State.NetWorth: cash + property + houses)
totalChips  = Σ chips_i over live seats (not bankrupt, not cashed out)
```

**Redeemable value of seat i  =  pool × chips_i / totalChips**  (integer floor; the
rounding remainder stays in the pool, so escrow always balances).

### Why this conserves money (no minting)
- The bank minting chips (GO salary) inflates `totalChips`, which lowers everyone's
  coins-per-chip equally. It does **not** create coins — the pool only grows via buy-ins.
- **Cash-out is ratio-invariant.** If seat A redeems `pool·A/total`, then afterwards
  `remainingPool / remainingChips = pool·(total−A)/total ÷ (total−A) = pool/total` —
  unchanged. Other players' redeemable value is untouched by A leaving. (Proof holds for
  any number of sequential cash-outs.)
- **Fair buy-in.** A top-up of `C` coins credits `C × totalChips / pool` chips (i.e. at
  the *current* rate, not 1:1), which likewise leaves `pool/total` invariant. First
  buy-in on an empty table seeds at 1 coin = 1 chip; that player's engine starting cash
  is their buy-in (we override the fixed 1500 for cash-game tables).
- A player who goes **bankrupt to the bank** destroys their chips but their buy-in coins
  stay in the pool → everyone else's share rises. Exactly the "the broke player's money
  is left on the table" feel — and still conserved.

### Rake (never by minting)
Default: **rake on net winnings at cash-out.** `gross = pool·chips_i/total`;
`profit = max(0, gross − costBasis_i)`; `rake = profit·rakePct`; the player receives
`gross − rake`; `rake → platform_revenue`. Losers pay no rake; only realized profit is
raked. (Config alt: flat % on buy-in.) Escrow debit `gross` = player credit + rake, so
the ledger always balances.

### Lifecycle
- **Buy-in / rebuy:** allowed on join and any time while seated, up to a max-stack cap
  (poker-style). Mints chips at the current rate; coins → escrow.
- **Cash-out (leave anytime):** compute share, rake the profit, pay, remove the seat.
- **Broke = out:** net worth 0 → nothing to redeem → eliminated.
- **Stop-loss (per seat, owner-set):** auto-cash-out when redeemable value ≤ a floor.
  Nothing illiquid to force-sell — the share is computed from net worth directly.
- **Table close (bounds a cash game):** max wall-clock + max turns, or ≤1 solvent seat.
  On close every remaining seat auto-cashes-out at the current rate; since
  `Σ(pool·chips_i/total) = pool`, the whole remaining pool is distributed exactly.

### Anti-collusion (required — chip-dumping is worse with real coins + trades)
- Every **trade** emits a fraud signal; repeated lopsided transfers between the same
  agent pair → flag/void (chip-dumping pattern).
- **Cash-out routes through `gate.Allow`** (same hold gate as tournament settle); a held
  cash-out is queued for admin review, not paid instantly.
- Per-seat **cost basis + realized profit** are recorded so laundering (big profit, no
  legitimate play) is visible to the fraud module.

---

## 2. Phases

- **P1 — pure cash-game economy math** ✅ done (`internal/monopoly/cashgame.go`).
  Integer-exact `RedeemableCoins`, `ChipsForBuyin`, `CashOut` (gross/rake/net),
  `ShouldStopLoss`, `CloseDistribution`; conservation/ratio-invariance/overflow tests.
- **P2 — engine seating for cash tables** ✅ done. `Engine.InitWithStacks` gives each
  seat its buy-in as opening chips; `Init` unchanged for tournament tables. (The
  `Mode: cash|tournament` flag + max-stack cap move to the service model in P4, wired
  with the money so no dead fields land early.)
- **P3 — money wiring:** ⏳ blocked on contested files. Extend the monopoly `Wallet`
  interface with `BuyIn` / `CashOut` (new `internal/wallet/monopoly_cashgame.go`, reusing
  `KindStake`/`KindSettle` + metadata to stay off the parallel session's contested
  `ledger/types.go`); cash-out through `gate.Allow`; migration for per-table pool +
  per-seat cost-basis. **Do after the parallel session's wallet/ledger WIP is committed.**
- **P4 — service lifecycle:** join/rebuy/leave endpoints, `Mode` flag + max-stack cap,
  stop-loss watcher, table-close bounds, auto-cash-out on close.
- **P5 — anti-collusion:** trade fraud signals + lopsided-pair detection.
- **R5 — engine rule fixes:** R5a (mortgage interest on transfer) ✅ done. R5b
  (bank-estate auction) ✅ done — `State.EstateQueue` + `AuctionState.Estate`;
  `declareBankrupt` auctions a bank creditor's estate property-by-property; `closeAuction`
  drains the queue then ends the debtor's turn. Engine `Version` bumped to
  `monopoly-1.2.0`. Full determinism + replay suites green.

**With R5a + R5b done, the engine now implements all five official rules.** Remaining
cash-game work is purely the money model (P3/P4/P5), which is why P3 is the gating item.

Each phase ships with unit + (where it touches the DB) real-Postgres integration tests,
mirroring the tournament path.

---

## 3. Non-goals / guardrails
- Real coins never move on the board — only buy-in and cash-out cross the boundary.
- No engine rule rewrites for the money model (the infinite bank stays).
- Cash-game tables are **flagged off** until P1-P5 + anti-collusion land and an
  admin enables the mode; KYC/AML posture is a launch decision, not a code default.
