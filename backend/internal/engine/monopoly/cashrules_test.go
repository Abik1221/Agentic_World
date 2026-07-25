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

// P2: cash-game tables seat each player on their own buy-in stack.

func TestInitWithStacks_PerSeatOpeningCash(t *testing.T) {
	e := New(Config{Players: 3, StartingCash: 1500})
	s, _ := e.InitWithStacks([]byte("seed-abc"), []int{500, 0, 900})
	// seat 0 → its stack, seat 1 → fallback (0 entry), seat 2 → its stack.
	if s.Players[0].Cash != 500 || s.Players[1].Cash != 1500 || s.Players[2].Cash != 900 {
		t.Fatalf("per-seat cash wrong: %d/%d/%d", s.Players[0].Cash, s.Players[1].Cash, s.Players[2].Cash)
	}
}

func TestInit_UnchangedUniformCash(t *testing.T) {
	e := New(Config{Players: 4, StartingCash: 1500})
	s, _ := e.Init([]byte("seed-xyz"))
	for i := range s.Players {
		if s.Players[i].Cash != 1500 {
			t.Fatalf("Init seat %d cash %d want 1500 (tournament tables unchanged)", i, s.Players[i].Cash)
		}
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

// R5b: a player who goes bankrupt owing the BANK has their estate auctioned to the
// surviving players, one property at a time, before their turn ends.

func TestBankruptcyToBank_QueuesEstateAuction(t *testing.T) {
	e, s := newGame(t, 3)
	// Seat 0 owns Mediterranean (1) and Baltic (3); it owes the bank an unpayable debt.
	s.Holdings[1] = Holding{Owner: 0}
	s.Holdings[3] = Holding{Owner: 0}
	s.Current = 0
	s.Debt = &Debt{Debtor: 0, Creditor: Bank, Amount: 999999, Property: 12, Reason: "rent"}

	e.declareBankrupt(&s)

	if !s.Players[0].Bankrupt {
		t.Fatal("debtor should be bankrupt")
	}
	if s.Phase != PhaseAuction || s.Auction == nil || !s.Auction.Estate {
		t.Fatalf("expected an estate auction, got phase=%q auction=%+v", s.Phase, s.Auction)
	}
	if s.Auction.Property != 1 { // first in ascending board order
		t.Fatalf("first estate property: got %d want 1", s.Auction.Property)
	}
	if len(s.EstateQueue) != 1 || s.EstateQueue[0] != 3 {
		t.Fatalf("remaining estate queue: got %v want [3]", s.EstateQueue)
	}
	if s.Auction.Current != 1 { // the bankrupt debtor (seat 0) can't bid; next active seat
		t.Fatalf("first bidder: got seat %d want 1", s.Auction.Current)
	}
}

func TestBankruptcyToBank_EstateDrainsThenTurnEnds(t *testing.T) {
	e, s := newGame(t, 3)
	s.Holdings[1] = Holding{Owner: 0}
	s.Holdings[3] = Holding{Owner: 0}
	s.Current = 0
	s.Debt = &Debt{Debtor: 0, Creditor: Bank, Amount: 999999, Property: 12, Reason: "rent"}
	e.declareBankrupt(&s)

	// Everyone passes on both estate properties; each auction closes unsold and the
	// next starts, then the debtor's turn ends once the queue drains.
	for i := 0; i < 12 && s.Phase == PhaseAuction; i++ {
		if _, err := e.stepAuction(&s, Action{Kind: ActPass}); err != nil {
			t.Fatalf("pass %d errored: %v", i, err)
		}
	}
	if s.Phase == PhaseAuction {
		t.Fatal("estate auctions never drained")
	}
	if len(s.EstateQueue) != 0 {
		t.Fatalf("estate queue not drained: %v", s.EstateQueue)
	}
	if s.Holdings[1].Owner != Bank || s.Holdings[3].Owner != Bank {
		t.Fatalf("unsold estate should stay with the bank: owners %d/%d", s.Holdings[1].Owner, s.Holdings[3].Owner)
	}
	if s.Current == 0 {
		t.Fatal("turn should have advanced past the bankrupt debtor")
	}
}
