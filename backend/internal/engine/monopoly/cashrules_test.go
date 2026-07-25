package monopoly

import "testing"

// R5a: assuming a mortgaged property (via trade or bankruptcy) costs the receiver 10%
// bank interest, rounded up — matching the unmortgage cost basis.

func TestMortgageInterest_TenPercentCeil(t *testing.T) {
	// Mediterranean Ave (idx 1): price 60 → mortgage value 30 → interest ceil(3.0) = 3.
	if got := mortgageInterest(1); got != 3 {
		t.Fatalf("interest: got %d want 3", got)
	}
}

func TestExecuteTrade_ChargesTransferInterestToReceiver(t *testing.T) {
	e, s := newGame(t, 2)
	// Seat 0 owns a MORTGAGED Mediterranean Ave; seat 1 will receive it.
	s.Holdings[1] = Holding{Owner: 0, Mortgaged: true}
	c0, c1 := s.Players[0].Cash, s.Players[1].Cash

	// A pure property gift: seat 0 (proposer) gives idx 1 to seat 1 (target), no cash.
	evs := e.executeTrade(&s, Trade{Proposer: 0, Target: 1, GiveProps: []int{1}})

	if s.Holdings[1].Owner != 1 {
		t.Fatalf("ownership not transferred: owner %d", s.Holdings[1].Owner)
	}
	if s.Players[1].Cash != c1-3 {
		t.Fatalf("receiver not billed interest: got %d want %d", s.Players[1].Cash, c1-3)
	}
	if s.Players[0].Cash != c0 {
		t.Fatalf("giver's cash should be untouched: got %d want %d", s.Players[0].Cash, c0)
	}
	if len(evs) != 1 || evs[0].Type != EvCashChanged {
		t.Fatalf("expected one mortgage-interest cash event, got %+v", evs)
	}
}

func TestExecuteTrade_NoInterestForUnmortgaged(t *testing.T) {
	e, s := newGame(t, 2)
	s.Holdings[1] = Holding{Owner: 0, Mortgaged: false} // NOT mortgaged
	c1 := s.Players[1].Cash

	evs := e.executeTrade(&s, Trade{Proposer: 0, Target: 1, GiveProps: []int{1}})

	if s.Players[1].Cash != c1 {
		t.Fatalf("unmortgaged transfer must be free: got %d want %d", s.Players[1].Cash, c1)
	}
	if len(evs) != 0 {
		t.Fatalf("no interest events expected, got %+v", evs)
	}
}

func TestChargeTransferInterest_FlooredAtAvailableCash(t *testing.T) {
	e, s := newGame(t, 2)
	s.Holdings[1] = Holding{Owner: 1, Mortgaged: true}
	s.Players[1].Cash = 2 // less than the 3 owed

	evs := e.chargeTransferInterest(&s, 1, 1)

	if s.Players[1].Cash != 0 {
		t.Fatalf("interest must floor at available cash (never negative): got %d", s.Players[1].Cash)
	}
	if len(evs) != 1 {
		t.Fatalf("expected a (partial) interest event, got %+v", evs)
	}
}
