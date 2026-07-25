package monopoly

import "math/big"

// cashgame.go — pure, integer-exact economics for CASH-GAME Monopoly tables.
//
// A cash-game table runs on chips (the engine's per-seat net worth) but pays out real
// coins as a mutual-fund SHARE of the table pool, never a fixed chip↔coin peg:
//
//	redeemable(seat) = pool × chips(seat) / totalChips      (integer floor)
//
// This conserves money by construction — the pool only grows via buy-ins, the bank
// minting chips (GO salary) merely dilutes everyone's coins-per-chip equally, and a
// cash-out is ratio-invariant (see MONOPOLY_CASH_GAME_DESIGN.md §1). Every function
// here is pure and overflow-safe (128-bit intermediate via math/big); the money wiring
// (ledger postings, gate.Allow hold) lives in the wallet layer and calls into these.
//
// All amounts are in coins (the platform's smallest unit); chips are the engine's
// net-worth integers. Rounding always floors, and the floor remainder stays in the
// pool, so escrow can never be over-drawn.

// mulDiv returns floor(a×b / d) for non-negative a, b and positive d, computed in
// 128-bit so pool×chips can't overflow int64. d <= 0 returns 0 (defensive).
func mulDiv(a, b, d int64) int64 {
	if a <= 0 || b <= 0 || d <= 0 {
		return 0
	}
	x := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	x.Quo(x, big.NewInt(d))
	return x.Int64()
}

// RedeemableCoins is seat's cash-out value: its share of the pool. The sole remaining
// live seat (chips >= totalChips) takes the whole pool so no coins are stranded by
// rounding; otherwise floor(pool × chips / totalChips), remainder left in the pool.
func RedeemableCoins(pool, chips, totalChips int64) int64 {
	if pool <= 0 || chips <= 0 || totalChips <= 0 {
		return 0
	}
	if chips >= totalChips {
		return pool
	}
	return mulDiv(pool, chips, totalChips)
}

// ChipsForBuyin is how many chips a buy-in of `coins` mints at the table's CURRENT
// redemption rate. On an empty table (no pool or no chips yet) it seeds 1 coin = 1 chip;
// otherwise chips = floor(coins × totalChips / pool), i.e. the current rate, so the
// pool/chips ratio — and thus every seated player's redeemable value — is unchanged at
// the instant of buy-in. (The buyer's own coins then join the pool.)
func ChipsForBuyin(coins, pool, totalChips int64) int64 {
	if coins <= 0 {
		return 0
	}
	if pool <= 0 || totalChips <= 0 {
		return coins // seed rate 1:1 on a fresh table
	}
	return mulDiv(coins, totalChips, pool)
}

// CashOutResult is the coin breakdown of a seat leaving the table. Gross is the escrow
// debit (the seat's pool share); Rake is taken only on realized profit and goes to
// platform revenue; Net is paid to the agent. Gross == Rake + Net always, so the ledger
// balances.
type CashOutResult struct {
	Gross  int64 // escrow → (agent + platform); the seat's share of the pool
	Rake   int64 // → platform revenue (only on profit above cost basis)
	Net    int64 // → agent wallet
	Profit int64 // gross − costBasis, floored at 0 (what the rake is charged on)
}

// CashOut computes a seat's disbursement. rakePct (0..100) is applied only to realized
// profit (gross above the coins the seat bought in with), so a break-even or losing
// seat pays no rake. clamp keeps rakePct in range.
func CashOut(pool, chips, totalChips, costBasis int64, rakePct int) CashOutResult {
	gross := RedeemableCoins(pool, chips, totalChips)
	if gross <= 0 {
		return CashOutResult{}
	}
	if rakePct < 0 {
		rakePct = 0
	}
	if rakePct > 100 {
		rakePct = 100
	}
	profit := gross - costBasis
	if profit < 0 {
		profit = 0
	}
	rake := mulDiv(profit, int64(rakePct), 100)
	return CashOutResult{Gross: gross, Rake: rake, Net: gross - rake, Profit: profit}
}

// ShouldStopLoss reports whether a seat's live redeemable value has fallen to or below
// its owner-set floor (in coins) — the trigger to auto-cash-out. floorCoins <= 0
// disables the stop-loss.
func ShouldStopLoss(pool, chips, totalChips, floorCoins int64) bool {
	if floorCoins <= 0 {
		return false
	}
	return RedeemableCoins(pool, chips, totalChips) <= floorCoins
}

// TotalChips sums the net worth of the live seats (the denominator of every share).
func TotalChips(chipsBySeat map[int]int64) int64 {
	var total int64
	for _, c := range chipsBySeat {
		if c > 0 {
			total += c
		}
	}
	return total
}

// CloseDistribution cashes out every live seat when a table closes: each seat gets its
// share (floored), so the sum of Gross is at most the pool. Any floor remainder (< number
// of seats) is returned separately and, by convention, stays with platform revenue so
// escrow zeroes out exactly. Seats with non-positive chips are skipped.
func CloseDistribution(pool int64, chipsBySeat, costBasisBySeat map[int]int64, rakePct int) (map[int]CashOutResult, int64) {
	total := TotalChips(chipsBySeat)
	out := make(map[int]CashOutResult, len(chipsBySeat))
	var distributed int64
	for seat, chips := range chipsBySeat {
		if chips <= 0 {
			continue
		}
		res := CashOut(pool, chips, total, costBasisBySeat[seat], rakePct)
		if res.Gross <= 0 {
			continue
		}
		out[seat] = res
		distributed += res.Gross
	}
	remainder := pool - distributed
	if remainder < 0 {
		remainder = 0 // defensive; floors can only under-distribute
	}
	return out, remainder
}
