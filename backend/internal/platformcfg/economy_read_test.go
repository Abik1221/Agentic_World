package platformcfg

import "testing"

// Every field in the economy block used to be write-only: the Super Admin published
// it, the bus carried it, and no code read it. These pin the read side, and in
// particular the bounds — the values cross a service boundary, so a corrupt or
// compromised publisher must not be able to set a confiscatory fee or remove a floor
// that exists to stop the platform being drained.

func TestWithdrawFeeIsBounded(t *testing.T) {
	const fallback = 5
	for _, bad := range []int{-1, 51, 100, 1000} {
		s := &Snapshot{Economy: Economy{WithdrawFeePct: bad}}
		if got := s.WithdrawFeePct(fallback); got != fallback {
			t.Fatalf("published %d%% accepted as %d%%; a fee above half the balance is confiscation", bad, got)
		}
	}
	s := &Snapshot{Economy: Economy{WithdrawFeePct: 8}}
	if got := s.WithdrawFeePct(fallback); got != 8 {
		t.Fatalf("in-band 8%% not adopted; got %d%%", got)
	}
	// A fee-free promotion is a real setting.
	zero := &Snapshot{Economy: Economy{WithdrawFeePct: 0}}
	if got := zero.WithdrawFeePct(fallback); got != 0 {
		t.Fatalf("a deliberate 0%% fee became %d%%", got)
	}
}

// The admin sets a dollar minimum; the ledger withdraws coins. The conversion must
// honour the peg, and an unset minimum must not become zero — a zero minimum lets
// someone spam dust withdrawals that each cost a real on-chain fee.
func TestMinWithdrawalConvertsAtThePeg(t *testing.T) {
	s := &Snapshot{Economy: Economy{MinWithdrawalCents: 2000}} // $20
	if got := s.MinWithdrawalCoins(500, 1); got != 2000 {
		t.Fatalf("got %d coins, want 2000 ($20 at a 1c peg)", got)
	}
	if got := s.MinWithdrawalCoins(500, 5); got != 400 {
		t.Fatalf("got %d coins, want 400 ($20 at a 5c peg)", got)
	}
	unset := &Snapshot{}
	if got := unset.MinWithdrawalCoins(500, 1); got != 500 {
		t.Fatalf("unset minimum became %d; dust withdrawals would be free to spam", got)
	}
	if got := s.MinWithdrawalCoins(500, 0); got != 500 {
		t.Fatal("a zero peg must fall back rather than divide by zero")
	}
}

// A maximum below the minimum would reject every deposit, so it is treated as
// corrupt rather than obeyed.
func TestDepositBoundsRejectAnInvertedRange(t *testing.T) {
	s := &Snapshot{Economy: Economy{MinPurchaseCents: 500, MaxPurchaseCents: 100}}
	if got := s.MaxDepositCents(200_000); got != 200_000 {
		t.Fatalf("max below min was accepted as %d; every deposit would be refused", got)
	}
	ok := &Snapshot{Economy: Economy{MinPurchaseCents: 500, MaxPurchaseCents: 200_000}}
	if got := ok.MinDepositCents(100); got != 500 {
		t.Fatalf("min deposit = %d, want 500", got)
	}
	if got := ok.MaxDepositCents(100); got != 200_000 {
		t.Fatalf("max deposit = %d, want 200000", got)
	}
}

// The floor is admin policy. Zero is a deliberate floorless sandbox and must be
// expressible; only a negative value is meaningless.
func TestMinStakeFloorHonoursZeroButRejectsNegative(t *testing.T) {
	zero := &Snapshot{Economy: Economy{MinStakeUSDCents: 0}}
	if got := zero.MinStakeUSDCents(500); got != 0 {
		t.Fatalf("a deliberate floorless sandbox became %d cents", got)
	}
	neg := &Snapshot{Economy: Economy{MinStakeUSDCents: -1}}
	if got := neg.MinStakeUSDCents(500); got != 500 {
		t.Fatalf("negative floor accepted as %d", got)
	}
	set := &Snapshot{Economy: Economy{MinStakeUSDCents: 1000}}
	if got := set.MinStakeUSDCents(500); got != 1000 {
		t.Fatalf("published $10 floor not adopted; got %d cents", got)
	}
}

// A nil snapshot is the cold start before the first bus refresh: fall back, never
// panic, and never treat "we have not heard yet" as "the operator chose zero".
func TestNilSnapshotFallsBackEverywhere(t *testing.T) {
	var s *Snapshot
	if s.WithdrawFeePct(7) != 7 || s.MinWithdrawalCoins(500, 1) != 500 ||
		s.MinDepositCents(100) != 100 || s.MaxDepositCents(9) != 9 || s.MinStakeUSDCents(500) != 500 {
		t.Fatal("a nil snapshot did not fall back cleanly on every accessor")
	}
}

// The entry fee is now admin-owned like the exit fee. Same bounds, same reasoning:
// this value crosses a service boundary, and a rate above half is confiscation.
func TestDepositFeeIsBounded(t *testing.T) {
	const fallback = 5
	for _, bad := range []int{-1, 51, 100} {
		s := &Snapshot{Economy: Economy{DepositFeePct: bad}}
		if got := s.DepositFeePct(fallback); got != fallback {
			t.Fatalf("published %d%% entry fee accepted as %d%%", bad, got)
		}
	}
	if got := (&Snapshot{Economy: Economy{DepositFeePct: 3}}).DepositFeePct(fallback); got != 3 {
		t.Fatalf("in-band 3%% not adopted; got %d%%", got)
	}
	// A fee-free deposit promotion is a real setting, not "unset".
	if got := (&Snapshot{Economy: Economy{DepositFeePct: 0}}).DepositFeePct(fallback); got != 0 {
		t.Fatalf("a deliberate 0%% entry fee became %d%%", got)
	}
	var nilSnap *Snapshot
	if got := nilSnap.DepositFeePct(7); got != 7 {
		t.Fatalf("nil snapshot did not fall back; got %d%%", got)
	}
}
