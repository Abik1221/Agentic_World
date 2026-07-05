package monopoly

import "testing"

var testSeed = []byte("monopoly-test-seed")

func newGame(t *testing.T, players int) (*Engine, State) {
	t.Helper()
	e := New(Config{Players: players, StartingCash: 1500, MaxTurns: 300})
	s, evs := e.Init(testSeed)
	if len(evs) != 2 || evs[0].Type != EvMatchCreated || evs[1].Type != EvTurnStarted {
		t.Fatalf("unexpected init events: %+v", evs)
	}
	return e, s
}

func TestInit(t *testing.T) {
	e, s := newGame(t, 4)
	if len(s.Players) != 4 {
		t.Fatalf("players = %d, want 4", len(s.Players))
	}
	for i, p := range s.Players {
		if p.Cash != 1500 || p.Position != IdxGo || p.Seat != i {
			t.Fatalf("player %d misconfigured: %+v", i, p)
		}
	}
	if s.HousesRemaining != 32 || s.HotelsRemaining != 12 {
		t.Fatalf("bank supply wrong: %d houses %d hotels", s.HousesRemaining, s.HotelsRemaining)
	}
	if len(s.ChanceOrder) != len(chanceDeck) || len(s.CCOrder) != len(ccDeck) {
		t.Fatal("decks not initialized")
	}
	if s.Phase != PhaseRoll || s.Current != 0 {
		t.Fatal("game should open on player 0's roll")
	}
	if !VerifyCommit(testSeed, Commit(testSeed)) {
		t.Fatal("commit/verify mismatch")
	}
	_ = e
}

func TestMoveBySteps_PassGo(t *testing.T) {
	e, s := newGame(t, 2)
	s.Players[0].Position = 39
	ns := s.clone()
	evs := e.moveBySteps(&ns, nil, 0, 5)
	if ns.Players[0].Position != 4 {
		t.Fatalf("position = %d, want 4", ns.Players[0].Position)
	}
	if ns.Players[0].Cash != 1700 {
		t.Fatalf("cash = %d, want 1700 (GO salary)", ns.Players[0].Cash)
	}
	if evs[len(evs)-1].Type != EvCashChanged {
		t.Fatal("expected a GO-salary cash event")
	}
}

func TestBuyProperty(t *testing.T) {
	e, s := newGame(t, 2)
	s.Players[0].Position = 1 // Mediterranean Avenue, $60
	s.Phase = PhaseAcquire
	ns, evs, err := e.Step(s, 0, Action{Kind: ActBuy}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns.Holdings[1].Owner != 0 {
		t.Fatal("property not owned after buy")
	}
	if ns.Players[0].Cash != 1440 {
		t.Fatalf("cash = %d, want 1440", ns.Players[0].Cash)
	}
	if ns.Phase != PhaseManage {
		t.Fatalf("phase = %s, want manage", ns.Phase)
	}
	if evs[0].Type != EvPropertyPurchased {
		t.Fatal("expected property_purchased")
	}
}

func TestRentDoublesOnFullGroup(t *testing.T) {
	_, s := newGame(t, 2)
	e := New(DefaultConfig())
	s.Holdings[1] = Holding{Owner: 0} // Mediterranean
	// Single ownership of brown: base rent 2.
	if r := e.rentFor(&s, 1, 0); r != 2 {
		t.Fatalf("single-owner rent = %d, want 2", r)
	}
	s.Holdings[3] = Holding{Owner: 0} // Baltic -> full brown set
	if r := e.rentFor(&s, 1, 0); r != 4 {
		t.Fatalf("full-set unimproved rent = %d, want 4 (2x base)", r)
	}
}

func TestRailroadAndUtilityRent(t *testing.T) {
	e := New(DefaultConfig())
	_, s := newGame(t, 2)
	s.Holdings[5] = Holding{Owner: 0}
	if r := e.rentFor(&s, 5, 0); r != 25 {
		t.Fatalf("1 railroad rent = %d, want 25", r)
	}
	s.Holdings[15] = Holding{Owner: 0}
	s.Holdings[25] = Holding{Owner: 0}
	if r := e.rentFor(&s, 5, 0); r != 100 {
		t.Fatalf("3 railroad rent = %d, want 100", r)
	}
	s.Holdings[12] = Holding{Owner: 0}
	if r := e.rentFor(&s, 12, 7); r != 28 {
		t.Fatalf("1 utility rent with dice 7 = %d, want 28", r)
	}
	s.Holdings[28] = Holding{Owner: 0}
	if r := e.rentFor(&s, 12, 7); r != 70 {
		t.Fatalf("2 utility rent with dice 7 = %d, want 70", r)
	}
}

func TestBuildEvenRuleAndSupply(t *testing.T) {
	e := New(DefaultConfig())
	_, s := newGame(t, 2)
	s.Holdings[1] = Holding{Owner: 0} // brown set
	s.Holdings[3] = Holding{Owner: 0}
	s.Current = 0
	s.Phase = PhaseManage

	// First house on Mediterranean: OK.
	ns, _, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed)
	if err != nil {
		t.Fatalf("first build failed: %v", err)
	}
	if ns.Holdings[1].Houses != 1 || ns.HousesRemaining != 31 {
		t.Fatalf("after build: houses=%d remaining=%d", ns.Holdings[1].Houses, ns.HousesRemaining)
	}
	// Second house on Mediterranean before Baltic violates even-build.
	if _, _, err := e.Step(ns, 0, Action{Kind: ActBuild, Property: 1}, testSeed); err == nil {
		t.Fatal("even-build rule not enforced")
	}
	// Building on Baltic to level the set: OK.
	if _, _, err := e.Step(ns, 0, Action{Kind: ActBuild, Property: 3}, testSeed); err != nil {
		t.Fatalf("levelling build failed: %v", err)
	}
}

func TestBuildRequiresFullGroup(t *testing.T) {
	e := New(DefaultConfig())
	_, s := newGame(t, 2)
	s.Holdings[1] = Holding{Owner: 0} // only one brown
	s.Phase = PhaseManage
	if _, _, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed); err == nil {
		t.Fatal("building without a monopoly should be illegal")
	}
}

func TestMortgageUnmortgage(t *testing.T) {
	e := New(DefaultConfig())
	_, s := newGame(t, 2)
	s.Holdings[1] = Holding{Owner: 0}
	s.Phase = PhaseManage
	start := s.Players[0].Cash

	ns, _, err := e.Step(s, 0, Action{Kind: ActMortgage, Property: 1}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if !ns.Holdings[1].Mortgaged || ns.Players[0].Cash != start+30 {
		t.Fatalf("mortgage wrong: mortgaged=%v cash=%d", ns.Holdings[1].Mortgaged, ns.Players[0].Cash)
	}
	ns2, _, err := e.Step(ns, 0, Action{Kind: ActUnmortgage, Property: 1}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns2.Holdings[1].Mortgaged || ns2.Players[0].Cash != start+30-33 {
		t.Fatalf("unmortgage wrong: mortgaged=%v cash=%d (want %d)", ns2.Holdings[1].Mortgaged, ns2.Players[0].Cash, start-3)
	}
}

func TestMortgageBlockedByHouses(t *testing.T) {
	e := New(DefaultConfig())
	_, s := newGame(t, 2)
	s.Holdings[1] = Holding{Owner: 0, Houses: 1}
	s.Holdings[3] = Holding{Owner: 0}
	s.Phase = PhaseManage
	if _, _, err := e.Step(s, 0, Action{Kind: ActMortgage, Property: 1}, testSeed); err == nil {
		t.Fatal("mortgaging a group with houses should be illegal")
	}
}

func TestGoToJailSpace(t *testing.T) {
	e := New(DefaultConfig())
	_, s := newGame(t, 2)
	s.Players[0].Position = IdxGoToJail
	ns := s.clone()
	e.resolveLanding(&ns, nil, testSeed)
	if !ns.Players[0].InJail || ns.Players[0].Position != IdxJail {
		t.Fatalf("go-to-jail failed: inJail=%v pos=%d", ns.Players[0].InJail, ns.Players[0].Position)
	}
}

func TestChanceCollectAndJailCards(t *testing.T) {
	e := New(DefaultConfig())
	_, s := newGame(t, 2)

	// Force a "Bank pays dividend $50" card (chance id 6).
	s.ChanceOrder = []int{6}
	s.ChanceIdx = 0
	ns := s.clone()
	before := ns.Players[0].Cash
	e.drawChance(&ns, nil, testSeed)
	if ns.Players[0].Cash != before+50 {
		t.Fatalf("dividend not paid: %d -> %d", before, ns.Players[0].Cash)
	}

	// Force "Get Out of Jail Free" (chance id 7).
	s.ChanceOrder = []int{7}
	ns = s.clone()
	e.drawChance(&ns, nil, testSeed)
	if ns.Players[0].JailCards != 1 {
		t.Fatalf("jail card not kept: %d", ns.Players[0].JailCards)
	}

	// Force "Go to Jail" (chance id 9).
	s.ChanceOrder = []int{9}
	ns = s.clone()
	e.drawChance(&ns, nil, testSeed)
	if !ns.Players[0].InJail {
		t.Fatal("go-to-jail card did not jail the player")
	}
}

func TestAuctionFlow(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Players[0].Position = 1 // Mediterranean
	s.Phase = PhaseAcquire

	// Player 0 declines -> auction opens with player 0 to act.
	s, _, err := e.Step(s, 0, Action{Kind: ActDecline}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if s.Phase != PhaseAuction || s.Auction == nil || s.Auction.Current != 0 {
		t.Fatalf("auction did not open correctly: %+v", s.Auction)
	}
	// Player 0 bids 10.
	s, _, err = e.Step(s, 0, Action{Kind: ActBid, Amount: 10}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if s.Auction.Current != 1 {
		t.Fatalf("turn did not pass to player 1: current=%d", s.Auction.Current)
	}
	// Player 1 passes -> auction closes, player 0 wins.
	s, _, err = e.Step(s, 1, Action{Kind: ActPass}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if s.Holdings[1].Owner != 0 || s.Players[0].Cash != 1490 || s.Phase != PhaseManage {
		t.Fatalf("auction result wrong: owner=%d cash=%d phase=%s", s.Holdings[1].Owner, s.Players[0].Cash, s.Phase)
	}
}

func TestInvalidBidRejected(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Players[0].Position = 1
	s.Phase = PhaseAcquire
	s, _, _ = e.Step(s, 0, Action{Kind: ActDecline}, testSeed)
	s, _, _ = e.Step(s, 0, Action{Kind: ActBid, Amount: 50}, testSeed) // high bid now 50, player 1 to act
	if _, _, err := e.Step(s, 1, Action{Kind: ActBid, Amount: 50}, testSeed); err == nil {
		t.Fatal("bid not exceeding high bid should be rejected")
	}
}

func TestBankruptcyEndsTwoPlayerGame(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Players[0].Cash = 10
	s.Current = 0
	s.Debt = &Debt{Debtor: 0, Creditor: 1, Amount: 500, Property: 6, Reason: "rent"}
	s.Phase = PhaseResolveDebt

	ns, _, err := e.Step(s, 0, Action{Kind: ActBankrupt}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if !ns.Players[0].Bankrupt {
		t.Fatal("debtor not marked bankrupt")
	}
	if !ns.Finished || ns.Winner != 1 {
		t.Fatalf("game should end with player 1 winning: finished=%v winner=%d", ns.Finished, ns.Winner)
	}
	// Creditor inherited the debtor's remaining cash.
	if ns.Players[1].Cash != 1510 {
		t.Fatalf("creditor cash = %d, want 1510", ns.Players[1].Cash)
	}
}

func TestDebtAutoSettlesWhenFundsRaised(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Players[0].Cash = 20
	s.Holdings[1] = Holding{Owner: 0} // can mortgage for $30
	s.Current = 0
	s.Debt = &Debt{Debtor: 0, Creditor: 1, Amount: 40, Property: 6, Reason: "rent"}
	s.Phase = PhaseResolveDebt

	// Mortgaging raises cash to 50, which auto-settles the $40 rent debt.
	ns, _, err := e.Step(s, 0, Action{Kind: ActMortgage, Property: 1}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns.Debt != nil {
		t.Fatal("debt should have auto-settled")
	}
	if ns.Players[0].Cash != 10 { // 20 + 30 mortgage - 40 rent
		t.Fatalf("debtor cash = %d, want 10", ns.Players[0].Cash)
	}
	if ns.Players[1].Cash != 1540 {
		t.Fatalf("creditor cash = %d, want 1540", ns.Players[1].Cash)
	}
	if ns.Phase != PhaseManage {
		t.Fatalf("phase = %s, want manage", ns.Phase)
	}
}

func TestStepRejectsOutOfTurn(t *testing.T) {
	e, s := newGame(t, 3)
	if _, _, err := e.Step(s, 1, Action{Kind: ActRoll}, testSeed); err != ErrNotYourTurn {
		t.Fatalf("expected ErrNotYourTurn, got %v", err)
	}
	if _, _, err := e.Step(s, 0, Action{Kind: ActBuy}, testSeed); err != ErrIllegalAction {
		t.Fatalf("expected ErrIllegalAction for buy during roll, got %v", err)
	}
}

func TestLegalActions(t *testing.T) {
	e, s := newGame(t, 2)
	if got := e.LegalActions(s, 0); len(got) != 1 || got[0] != ActRoll {
		t.Fatalf("roll-phase legal actions = %v", got)
	}
	if e.LegalActions(s, 1) != nil {
		t.Fatal("non-current player should have no legal actions during roll")
	}
}

func TestTradeAccept(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0} // Mediterranean
	s.Holdings[3] = Holding{Owner: 1} // Baltic
	s.Current = 0
	s.Phase = PhaseManage

	// Seat 0 offers Mediterranean + $50 for Baltic.
	trade := &Trade{Target: 1, GiveProps: []int{1}, GiveCash: 50, WantProps: []int{3}}
	ns, evs, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: trade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns.Phase != PhaseTradeResponse || ns.PendingTrade == nil {
		t.Fatalf("trade did not open: phase=%s", ns.Phase)
	}
	if evs[0].Type != EvTradeProposed {
		t.Fatal("expected trade_proposed event")
	}
	if e.pendingActor(ns) != 1 {
		t.Fatal("target should be the pending actor")
	}
	// The proposer cannot accept their own trade.
	if _, _, err := e.Step(ns, 0, Action{Kind: ActAcceptTrade}, testSeed); err != ErrNotYourTurn {
		t.Fatalf("proposer accepting should be ErrNotYourTurn, got %v", err)
	}

	ns2, evs2, err := e.Step(ns, 1, Action{Kind: ActAcceptTrade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns2.Holdings[1].Owner != 1 || ns2.Holdings[3].Owner != 0 {
		t.Fatalf("properties not swapped: %d %d", ns2.Holdings[1].Owner, ns2.Holdings[3].Owner)
	}
	if ns2.Players[0].Cash != 1450 || ns2.Players[1].Cash != 1550 {
		t.Fatalf("cash wrong: %d %d", ns2.Players[0].Cash, ns2.Players[1].Cash)
	}
	if ns2.Phase != PhaseManage || ns2.PendingTrade != nil {
		t.Fatal("should return to manage with no pending trade")
	}
	if evs2[0].Type != EvTradeExecuted {
		t.Fatal("expected trade_executed event")
	}
}

func TestTradeReject(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0}
	s.Holdings[3] = Holding{Owner: 1}
	s.Current = 0
	s.Phase = PhaseManage
	s, _, _ = e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: &Trade{Target: 1, GiveProps: []int{1}, WantProps: []int{3}}}, testSeed)
	s, _, err := e.Step(s, 1, Action{Kind: ActRejectTrade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if s.Phase != PhaseManage || s.PendingTrade != nil {
		t.Fatal("reject should clear the trade and return to manage")
	}
	if s.Holdings[1].Owner != 0 || s.Holdings[3].Owner != 1 {
		t.Fatal("reject must not move any property")
	}
}

func TestTradeValidation(t *testing.T) {
	e := New(DefaultConfig())
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0}
	s.Current = 0
	s.Phase = PhaseManage

	// Wanting a property the target doesn't own is illegal.
	if _, _, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: &Trade{Target: 1, GiveProps: []int{1}, WantProps: []int{3}}}, testSeed); err == nil {
		t.Fatal("trade wanting an unowned property should fail")
	}
	// A property whose color group has buildings cannot be traded.
	s.Holdings[1] = Holding{Owner: 0, Houses: 1}
	s.Holdings[3] = Holding{Owner: 0}
	if _, _, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: &Trade{Target: 1, GiveProps: []int{1}, GiveCash: 1}}, testSeed); err == nil {
		t.Fatal("trading a property whose group has houses should fail")
	}
	// An empty trade is illegal.
	s.Holdings[1] = Holding{Owner: 0}
	if _, _, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: &Trade{Target: 1}}, testSeed); err == nil {
		t.Fatal("an empty trade should fail")
	}
}
