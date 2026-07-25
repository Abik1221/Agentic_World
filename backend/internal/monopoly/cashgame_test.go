package monopoly

import "testing"

func TestRedeemableCoins_ShareAndEdges(t *testing.T) {
	// even 4-way split of a 400 pool with equal chips
	if got := RedeemableCoins(400, 100, 400); got != 100 {
		t.Fatalf("even share: got %d want 100", got)
	}
	// proportional: 25% of chips → 25% of pool
	if got := RedeemableCoins(1000, 250, 1000); got != 250 {
		t.Fatalf("proportional: got %d want 250", got)
	}
	// sole live seat takes the whole pool (no rounding loss)
	if got := RedeemableCoins(999, 500, 500); got != 999 {
		t.Fatalf("sole seat: got %d want 999", got)
	}
	// floor: 1/3 of 100 = 33 (remainder stays in pool)
	if got := RedeemableCoins(100, 1, 3); got != 33 {
		t.Fatalf("floor: got %d want 33", got)
	}
	// degenerate inputs → 0
	for _, tc := range [][3]int64{{0, 1, 1}, {100, 0, 1}, {100, 1, 0}, {100, -5, 10}} {
		if got := RedeemableCoins(tc[0], tc[1], tc[2]); got != 0 {
			t.Fatalf("degenerate %v: got %d want 0", tc, got)
		}
	}
}

func TestCashOut_IsConserved_NoOverDraw(t *testing.T) {
	// three seats, unequal chips, cash out sequentially; total paid must never exceed
	// the pool and the pool must never go negative.
	pool := int64(1000)
	chips := map[int]int64{0: 500, 1: 300, 2: 200} // total 1000
	total := TotalChips(chips)
	var paidOut int64
	for seat := 0; seat < 3; seat++ {
		res := CashOut(pool, chips[seat], total, 0, 0)
		if res.Gross < 0 || res.Gross > pool {
			t.Fatalf("seat %d gross %d out of range (pool %d)", seat, res.Gross, pool)
		}
		if res.Net+res.Rake != res.Gross {
			t.Fatalf("seat %d net+rake=%d != gross %d", seat, res.Net+res.Rake, res.Gross)
		}
		pool -= res.Gross
		total -= chips[seat]
		paidOut += res.Gross
	}
	if pool < 0 {
		t.Fatalf("pool went negative: %d", pool)
	}
	if paidOut != 1000 {
		t.Fatalf("equal-basis conservation: paid %d want 1000 (pool left %d)", paidOut, pool)
	}
}

func TestCashOut_RatioInvariance(t *testing.T) {
	// After seat 0 leaves, seat 1's redeemable value must be unchanged (its share of the
	// remaining pool equals its share of the original pool).
	pool, total := int64(1000), int64(1000)
	before := RedeemableCoins(pool, 300, total) // seat 1 = 300 chips
	res0 := CashOut(pool, 500, total, 0, 0)     // seat 0 leaves
	after := RedeemableCoins(pool-res0.Gross, 300, total-500)
	if before != after {
		t.Fatalf("ratio not invariant: before %d after %d", before, after)
	}
}

func TestChipsForBuyin_SeedAndRatePreserving(t *testing.T) {
	// fresh table: 1 coin = 1 chip
	if got := ChipsForBuyin(150, 0, 0); got != 150 {
		t.Fatalf("seed rate: got %d want 150", got)
	}
	// buying in at the current rate must not change any seated player's redeemable value.
	pool, total := int64(1000), int64(800)
	seatChips := int64(200)
	before := RedeemableCoins(pool, seatChips, total)
	buy := int64(500)
	minted := ChipsForBuyin(buy, pool, total) // chips = 500 * 800 / 1000 = 400
	if minted != 400 {
		t.Fatalf("mint: got %d want 400", minted)
	}
	after := RedeemableCoins(pool+buy, seatChips, total+minted)
	if before != after {
		t.Fatalf("buy-in disturbed a seated share: before %d after %d", before, after)
	}
}

func TestCashOut_RakeOnlyOnProfit(t *testing.T) {
	// winner: gross 300 on a 100 cost basis → 200 profit, 10% rake = 20.
	win := CashOut(600, 300, 600, 100, 10)
	if win.Gross != 300 || win.Profit != 200 || win.Rake != 20 || win.Net != 280 {
		t.Fatalf("winner rake: %+v", win)
	}
	// loser: gross 50 on a 100 cost basis → no profit, no rake.
	lose := CashOut(600, 50, 600, 100, 10)
	if lose.Rake != 0 || lose.Net != lose.Gross {
		t.Fatalf("loser should pay no rake: %+v", lose)
	}
	// rakePct is clamped
	if r := CashOut(600, 300, 600, 100, 500).Rake; r != 200 {
		t.Fatalf("rake clamp: got %d want 200 (100%% of 200 profit)", r)
	}
}

func TestShouldStopLoss(t *testing.T) {
	// floor 100: redeemable 90 trips it, 110 does not, 0 floor disables.
	if !ShouldStopLoss(1000, 90, 1000, 100) {
		t.Fatal("should trip at/below floor")
	}
	if ShouldStopLoss(1000, 110, 1000, 100) {
		t.Fatal("should not trip above floor")
	}
	if ShouldStopLoss(1000, 10, 1000, 0) {
		t.Fatal("floor 0 disables stop-loss")
	}
}

func TestCloseDistribution_ConservesPool(t *testing.T) {
	pool := int64(1001) // odd → forces a floor remainder
	chips := map[int]int64{0: 334, 1: 333, 2: 334}
	basis := map[int]int64{0: 300, 1: 300, 2: 300}
	dist, remainder := CloseDistribution(pool, chips, basis, 0)
	var sum int64
	for _, r := range dist {
		sum += r.Gross
	}
	if sum+remainder != pool {
		t.Fatalf("close not conserved: distributed %d + remainder %d != pool %d", sum, remainder, pool)
	}
	if remainder < 0 || remainder >= int64(len(chips)) {
		t.Fatalf("remainder %d should be a small floor leftover (< seats)", remainder)
	}
}

func TestMulDiv_OverflowSafe(t *testing.T) {
	// pool × chips would overflow int64 (both ~1e18) if multiplied natively.
	big := int64(1_000_000_000_000_000_000)
	if got := RedeemableCoins(big, big/2, big); got != big/2 {
		t.Fatalf("overflow-safe share: got %d want %d", got, big/2)
	}
}
