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

func TestTradeCounter(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0} // Mediterranean — seat 0
	s.Holdings[3] = Holding{Owner: 1} // Baltic — seat 1
	s.Current = 0
	s.Phase = PhaseManage

	// Seat 0 offers Mediterranean for Baltic.
	s, _, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: &Trade{Target: 1, GiveProps: []int{1}, WantProps: []int{3}}}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	// counter_trade must be legal for the responder.
	if got := e.LegalActions(s, 1); !containsString(got, ActCounterTrade) {
		t.Fatalf("counter_trade should be legal for the responder, got %v", got)
	}

	// Seat 1 counters: wants Mediterranean AND $100 for Baltic.
	s, cevs, err := e.Step(s, 1, Action{Kind: ActCounterTrade, Trade: &Trade{GiveProps: []int{3}, WantProps: []int{1}, WantCash: 100}}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if s.Phase != PhaseTradeResponse || s.PendingTrade == nil {
		t.Fatal("counter should keep the negotiation open")
	}
	if cevs[0].Type != EvTradeProposed {
		t.Fatal("a counter is emitted as a new trade_proposed")
	}
	// Roles swapped: seat 1 is now the proposer, seat 0 must respond.
	if s.PendingTrade.Proposer != 1 || s.PendingTrade.Target != 0 {
		t.Fatalf("counter roles not swapped: proposer=%d target=%d", s.PendingTrade.Proposer, s.PendingTrade.Target)
	}
	if e.pendingActor(s) != 0 {
		t.Fatal("original proposer should now be the pending actor")
	}
	if s.TradeCounters != 1 {
		t.Fatalf("counter depth should be 1, got %d", s.TradeCounters)
	}

	// Seat 0 accepts the counter: seat 0 gives Mediterranean + $100, gets Baltic.
	s, aevs, err := e.Step(s, 0, Action{Kind: ActAcceptTrade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if aevs[0].Type != EvTradeExecuted {
		t.Fatal("expected trade_executed")
	}
	if s.Holdings[1].Owner != 1 || s.Holdings[3].Owner != 0 {
		t.Fatalf("counter not executed: med=%d baltic=%d", s.Holdings[1].Owner, s.Holdings[3].Owner)
	}
	if s.Players[0].Cash != 1400 || s.Players[1].Cash != 1600 {
		t.Fatalf("counter cash wrong: %d %d", s.Players[0].Cash, s.Players[1].Cash)
	}
	// Turn returns to the original turn owner (seat 0), negotiation cleared.
	if s.Phase != PhaseManage || s.PendingTrade != nil || s.Current != 0 || s.TradeCounters != 0 {
		t.Fatalf("should return to turn owner's manage: phase=%s current=%d", s.Phase, s.Current)
	}
}

func TestTradeCounterCapEndsAtAcceptReject(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 5000, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0}
	s.Holdings[3] = Holding{Owner: 1}
	s.Current = 0
	s.Phase = PhaseManage
	s, _, _ = e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: &Trade{Target: 1, GiveProps: []int{1}, WantProps: []int{3}}}, testSeed)

	// Alternate counters up to the cap; each side just re-counters the same shape.
	for i := 0; i < maxTradeCounters; i++ {
		responder := e.pendingActor(s)
		give, want := 3, 1
		if responder == 0 {
			give, want = 1, 3
		}
		var err error
		s, _, err = e.Step(s, responder, Action{Kind: ActCounterTrade, Trade: &Trade{GiveProps: []int{give}, WantProps: []int{want}}}, testSeed)
		if err != nil {
			t.Fatalf("counter %d failed: %v", i, err)
		}
	}
	// At the cap, counter is no longer legal — only accept/reject.
	if got := e.LegalActions(s, e.pendingActor(s)); containsString(got, ActCounterTrade) {
		t.Fatalf("counter should be capped out, got %v", got)
	}
	if _, _, err := e.Step(s, e.pendingActor(s), Action{Kind: ActCounterTrade, Trade: &Trade{GiveProps: []int{1}, WantProps: []int{3}}}, testSeed); err == nil {
		t.Fatal("counter past the cap should be rejected")
	}
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestTradeJailCards(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Players[1].JailCards = 1 // seat 1 holds a get-out-of-jail card
	s.Current = 0
	s.Phase = PhaseManage

	// Seat 0 offers $75 for seat 1's jail card.
	s, _, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: &Trade{Target: 1, GiveCash: 75, WantCards: 1}}, testSeed)
	if err != nil {
		t.Fatalf("propose jail-card trade: %v", err)
	}
	s, _, err = e.Step(s, 1, Action{Kind: ActAcceptTrade}, testSeed)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if s.Players[0].JailCards != 1 || s.Players[1].JailCards != 0 {
		t.Fatalf("jail card did not move: p0=%d p1=%d", s.Players[0].JailCards, s.Players[1].JailCards)
	}
	if s.Players[0].Cash != 1425 || s.Players[1].Cash != 1575 {
		t.Fatalf("cash wrong: %d %d", s.Players[0].Cash, s.Players[1].Cash)
	}

	// Offering a card you don't have is illegal.
	s2, _ := e.Init(testSeed)
	s2.Current = 0
	s2.Phase = PhaseManage
	if _, _, err := e.Step(s2, 0, Action{Kind: ActProposeTrade, Trade: &Trade{Target: 1, GiveCards: 1}}, testSeed); err == nil {
		t.Fatal("offering a jail card you don't hold should be rejected")
	}
}

func TestOpenTradeWindow(t *testing.T) {
	e := New(Config{Players: 3, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0} // Mediterranean — seat 0
	s.Holdings[3] = Holding{Owner: 2} // Baltic — seat 2

	// End seat 0's turn: seat 1's turn now opens with a trade window for the others.
	s.Current = 0
	s.Phase = PhaseManage
	s, _, err := e.Step(s, 0, Action{Kind: ActEndTurn}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if s.Phase != PhaseTrade || s.Current != 1 {
		t.Fatalf("seat 1's turn should open a trade window: phase=%s current=%d", s.Phase, s.Current)
	}
	// The window offers the OTHER active players (0 then 2), not the turn owner.
	if e.pendingActor(s) != 0 {
		t.Fatalf("window should offer seat 0 first, got %d", e.pendingActor(s))
	}
	if got := e.LegalActions(s, 0); !containsString(got, ActProposeTrade) || !containsString(got, ActSkipTrade) {
		t.Fatalf("window legal actions = %v", got)
	}

	// Seat 0 trades with seat 2 DURING seat 1's turn — a trade on someone else's turn.
	s, _, err = e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: &Trade{Target: 2, GiveProps: []int{1}, WantProps: []int{3}}}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if s.Phase != PhaseTradeResponse || e.pendingActor(s) != 2 {
		t.Fatalf("seat 2 should respond: phase=%s pending=%d", s.Phase, e.pendingActor(s))
	}
	s, _, err = e.Step(s, 2, Action{Kind: ActAcceptTrade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if s.Holdings[1].Owner != 2 || s.Holdings[3].Owner != 0 {
		t.Fatal("trade did not execute during the window")
	}
	// The window resumes, pops seat 0, and offers seat 2 next.
	if s.Phase != PhaseTrade || e.pendingActor(s) != 2 {
		t.Fatalf("window should resume at seat 2: phase=%s pending=%d", s.Phase, e.pendingActor(s))
	}
	// Seat 2 skips → window drains → the turn owner (seat 1) finally rolls.
	s, _, err = e.Step(s, 2, Action{Kind: ActSkipTrade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if s.Phase != PhaseRoll || e.pendingActor(s) != 1 {
		t.Fatalf("after the window, seat 1 should roll: phase=%s pending=%d", s.Phase, e.pendingActor(s))
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

// ── open offers: any seat may take them ──────────────────────────────────────
//
// The rule these pin: an offer made to the TABLE is answered by whoever can satisfy it,
// in seat order, and the first yes wins. Before this existed a trade could only ever be
// accepted by the one seat it named, so an agent watching a good deal go by had no way
// to take it.

// openBoard: four seats, one property each in the brown/light-blue range, everyone solvent.
func openBoard(t *testing.T) (*Engine, State) {
	t.Helper()
	e := New(Config{Players: 4, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0} // Mediterranean — the seat 0 offers
	s.Holdings[3] = Holding{Owner: 1} // Baltic
	s.Holdings[6] = Holding{Owner: 2} // Oriental
	s.Holdings[8] = Holding{Owner: 3} // Vermont
	s.Current = 0
	s.Phase = PhaseManage
	return e, s
}

func TestOpenOfferIsAnsweredBySomeoneOtherThanTheTarget(t *testing.T) {
	e, s := openBoard(t)

	// Mediterranean for $200, offered to the whole table — no target named.
	trade := &Trade{Target: OpenToTable, GiveProps: []int{1}, WantCash: 200}
	ns, evs, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: trade}, testSeed)
	if err != nil {
		t.Fatalf("open offer refused: %v", err)
	}
	if evs[0].Type != EvTradeProposed {
		t.Fatalf("expected trade_proposed, got %s", evs[0].Type)
	}
	if ns.Phase != PhaseTradeResponse {
		t.Fatalf("phase = %s, want a response phase", ns.Phase)
	}
	// Every other solvent seat is eligible, in seat order. The proposer never is.
	if got := ns.OpenResponders; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("responders = %v, want [1 2 3]", got)
	}
	if e.pendingActor(ns) != 1 {
		t.Fatalf("pending actor = %d, want the head of the queue", e.pendingActor(ns))
	}

	// Seat 1 passes. The offer MUST survive — this is the whole feature.
	ns, evs, err = e.Step(ns, 1, Action{Kind: ActRejectTrade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if evs[0].Type != EvTradeDeclined {
		t.Fatalf("a pass on a standing offer must be trade_declined, got %s — trade_rejected "+
			"tells a watching agent the offer is gone when it can still be taken", evs[0].Type)
	}
	if ns.PendingTrade == nil {
		t.Fatal("the offer was withdrawn when seat 1 passed; seats 2 and 3 never got their chance")
	}
	if e.pendingActor(ns) != 2 {
		t.Fatalf("pending actor = %d, want seat 2 after seat 1 passed", e.pendingActor(ns))
	}

	// Seat 2 takes it — a seat the proposer never named.
	ns2, evs2, err := e.Step(ns, 2, Action{Kind: ActAcceptTrade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns2.Holdings[1].Owner != 2 {
		t.Fatalf("Mediterranean owner = %d, want seat 2 (the seat that accepted)", ns2.Holdings[1].Owner)
	}
	if ns2.Players[2].Cash != 1300 || ns2.Players[0].Cash != 1700 {
		t.Fatalf("cash wrong: proposer %d, acceptor %d", ns2.Players[0].Cash, ns2.Players[2].Cash)
	}
	if ns2.PendingTrade != nil || len(ns2.OpenResponders) != 0 {
		t.Fatal("the offer must be closed once taken")
	}
	if evs2[len(evs2)-1].Type != EvTradeExecuted {
		t.Fatalf("expected trade_executed, got %s", evs2[len(evs2)-1].Type)
	}
}

func TestOnlyTheHeadOfTheQueueMayAnswerAnOpenOffer(t *testing.T) {
	e, s := openBoard(t)
	trade := &Trade{Target: OpenToTable, GiveProps: []int{1}, WantCash: 200}
	ns, _, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: trade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	// Seat 3 is eligible but LAST. Letting it jump the queue would make the outcome
	// depend on which agent's HTTP response landed first, and the same match would
	// replay differently — the engine could no longer prove its own result.
	if _, _, err := e.Step(ns, 3, Action{Kind: ActAcceptTrade}, testSeed); err != ErrNotYourTurn {
		t.Fatalf("seat 3 jumping the queue = %v, want ErrNotYourTurn", err)
	}
	// The proposer cannot take its own offer either.
	if _, _, err := e.Step(ns, 0, Action{Kind: ActAcceptTrade}, testSeed); err != ErrNotYourTurn {
		t.Fatalf("proposer accepting its own open offer = %v, want ErrNotYourTurn", err)
	}
}

func TestOnlySeatsThatCanSatisfyAnOpenOfferAreAsked(t *testing.T) {
	e, s := openBoard(t)
	s.Players[1].Cash = 10 // cannot pay
	s.Players[2].Cash = 10 // cannot pay
	trade := &Trade{Target: OpenToTable, GiveProps: []int{1}, WantCash: 200}
	ns, _, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: trade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if got := ns.OpenResponders; len(got) != 1 || got[0] != 3 {
		t.Fatalf("responders = %v, want only seat 3 — a seat that cannot pay must not be "+
			"handed a decision it has no legal answer to", got)
	}
}

func TestAnOpenOfferNobodyCanTakeResolvesInsteadOfHanging(t *testing.T) {
	e, s := openBoard(t)
	for i := 1; i < 4; i++ {
		s.Players[i].Cash = 10
	}
	trade := &Trade{Target: OpenToTable, GiveProps: []int{1}, WantCash: 200}
	ns, evs, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: trade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns.PendingTrade != nil {
		t.Fatal("an offer with no eligible taker must not pend — nobody exists to answer it")
	}
	if ns.Phase != PhaseManage {
		t.Fatalf("phase = %s, want the turn to continue at manage", ns.Phase)
	}
	if len(evs) != 2 || evs[1].Type != EvTradeRejected {
		t.Fatalf("events = %v, want proposed then rejected", evs)
	}
}

// TestAnUnsatisfiableOpenOfferInTheWindowDoesNotLoop pins the bug this nearly shipped
// with: from the trade WINDOW, the proposer is only popped off TradeQueue when a
// negotiation resolves. An offer that found no taker originally returned without
// resolving, leaving the same seat at the head of the queue — so a policy that kept
// making the same unsatisfiable offer would never let the turn advance.
func TestAnUnsatisfiableOpenOfferInTheWindowDoesNotLoop(t *testing.T) {
	e, s := openBoard(t)
	s.Phase = PhaseTrade
	s.TradeQueue = []int{1, 2, 3}
	s.TradeReturn = ""
	for i := range s.Players {
		if i != 1 {
			s.Players[i].Cash = 10
		}
	}
	// Seat 1 offers Baltic for $900 — nobody can pay.
	trade := &Trade{Target: OpenToTable, GiveProps: []int{3}, WantCash: 900}
	ns, _, err := e.Step(s, 1, Action{Kind: ActProposeTrade, Trade: trade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns.TradeQueue) != 2 || ns.TradeQueue[0] != 2 {
		t.Fatalf("trade queue = %v, want seat 1 popped and seat 2 next; an unresolved "+
			"offer would ask seat 1 again forever", ns.TradeQueue)
	}
}

func TestAnOpenOfferCannotBeCountered(t *testing.T) {
	e, s := openBoard(t)
	trade := &Trade{Target: OpenToTable, GiveProps: []int{1}, WantCash: 200}
	ns, _, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: trade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	for _, act := range e.LegalActions(ns, 1) {
		if act == ActCounterTrade {
			t.Fatal("counter must not be legal on an open offer: it would turn a standing " +
				"table-wide offer into a private negotiation and cut out the seats behind")
		}
	}
	counter := &Trade{GiveProps: []int{3}, WantProps: []int{1}}
	if _, _, err := e.Step(ns, 1, Action{Kind: ActCounterTrade, Trade: counter}, testSeed); err == nil {
		t.Fatal("countering an open offer was accepted; the guard in stepTradeResponse is not holding")
	}
}

// TestAZeroTargetIsNeverAnOpenOffer is the seat-0 trap, in the one place it would be
// most expensive: an offer whose Target field was left unset must be a concrete offer
// to seat 0 — a real player — and never silently become an offer to the whole table.
func TestAZeroTargetIsNeverAnOpenOffer(t *testing.T) {
	e, s := openBoard(t)
	s.Current = 1 // so seat 0 is a plausible counterparty, not the proposer
	trade := &Trade{GiveProps: []int{3}, WantCash: 100}
	ns, _, err := e.Step(s, 1, Action{Kind: ActProposeTrade, Trade: trade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns.OpenResponders) != 0 {
		t.Fatalf("an unset target opened a table-wide offer (responders %v); it must be a "+
			"targeted offer to seat 0", ns.OpenResponders)
	}
	if ns.PendingTrade == nil || ns.PendingTrade.Target != 0 {
		t.Fatal("expected a concrete pending offer to seat 0")
	}
}

// ── free will: what the rules let a seat do, and when ────────────────────────
//
// Audited against the official rules (Hasbro/Parker Brothers). Each test below names the rule
// it pins and the behaviour that was wrong before it.

// TestADebtorMayTradeItsWayOut is the most characteristic moment in Monopoly and the engine
// did not allow it. Official: a player who cannot pay raises money by selling houses,
// mortgaging, OR TRADING with other players, and declares bankruptcy only when none of that
// is enough. PhaseResolveDebt offered [mortgage, sell_house, bankrupt] — so every squeezed
// agent had to liquidate or die, and could never offer a property for the cash to survive.
func TestADebtorMayTradeItsWayOut(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0} // seat 0 owns Mediterranean
	s.Players[0].Cash = 10            // and is broke
	s.Players[1].Cash = 1000
	s.Current = 0
	s.Phase = PhaseResolveDebt
	s.Debt = &Debt{Debtor: 0, Creditor: 1, Amount: 300, Property: 3, Reason: "rent"}

	legal := e.LegalActions(s, 0)
	var canTrade bool
	for _, a := range legal {
		if a == ActProposeTrade {
			canTrade = true
		}
	}
	if !canTrade {
		t.Fatalf("legal actions in debt = %v; a player who cannot pay must be able to TRADE "+
			"for the money, not only liquidate or go bankrupt", legal)
	}

	// Sell Mediterranean to seat 1 for $400 — enough to clear the $300 debt.
	offer := &Trade{Target: 1, GiveProps: []int{1}, WantCash: 400}
	ns, _, err := e.Step(s, 0, Action{Kind: ActProposeTrade, Trade: offer}, testSeed)
	if err != nil {
		t.Fatalf("a debtor's trade was refused: %v", err)
	}
	if ns.Phase != PhaseTradeResponse {
		t.Fatalf("phase = %s, want the counterparty to be asked", ns.Phase)
	}
	ns2, _, err := e.Step(ns, 1, Action{Kind: ActAcceptTrade}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	// The cash arrived and the debt is gone: afterRaise settles it the moment it is payable.
	if ns2.Debt != nil {
		t.Fatalf("debt still open with $%d in hand — a trade that covers the debt must settle "+
			"it exactly as selling a house would", ns2.Players[0].Cash)
	}
	if ns2.Players[0].Bankrupt {
		t.Fatal("the seat went bankrupt despite raising the money by trading")
	}
	if ns2.Holdings[1].Owner != 1 {
		t.Fatal("the traded property did not change hands")
	}
}

// TestADebtorWithNothingCanStillGoBankrupt: filtering the debt phase must never leave a seat
// with no legal move. Bankruptcy has to survive every filter.
func TestADebtorWithNothingCanStillGoBankrupt(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Players[0].Cash = 0
	s.Players[1].Bankrupt = true // nobody left to trade with either
	s.Current = 0
	s.Phase = PhaseResolveDebt
	s.Debt = &Debt{Debtor: 0, Creditor: Bank, Amount: 200, Property: -1, Reason: "tax"}
	legal := e.LegalActions(s, 0)
	if len(legal) != 1 || legal[0] != ActBankrupt {
		t.Fatalf("legal = %v, want exactly [bankrupt] — a seat with no assets and no partner "+
			"must still have one legal move", legal)
	}
}

// TestASeatMayBuildBetweenOtherPlayersTurns pins the official timing rule: you may buy houses
// on your turn OR between other players' turns. Management used to be reachable only in the
// owner's own manage phase, which deleted the timing plays the real game turns on — putting
// houses up just before an opponent's roll.
func TestASeatMayBuildBetweenOtherPlayersTurns(t *testing.T) {
	e := New(Config{Players: 3, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	// Seat 1 owns the whole brown group; it is seat 0's turn.
	s.Holdings[1] = Holding{Owner: 1}
	s.Holdings[3] = Holding{Owner: 1}
	s.Current = 0
	s.Phase = PhaseTrade
	s.TradeQueue = []int{1, 2}

	legal := e.LegalActions(s, 1)
	var canBuild bool
	for _, a := range legal {
		if a == ActBuild {
			canBuild = true
		}
	}
	if !canBuild {
		t.Fatalf("window actions for seat 1 = %v; the official rules let a player build "+
			"between other players' turns", legal)
	}

	ns, _, err := e.Step(s, 1, Action{Kind: ActBuild, Property: 1}, testSeed)
	if err != nil {
		t.Fatalf("building between turns was refused: %v", err)
	}
	if ns.Holdings[1].Houses != 1 {
		t.Fatalf("houses on Mediterranean = %d, want 1", ns.Holdings[1].Houses)
	}
	// Building must NOT cost the seat its place in the window — a player putting up a street
	// takes several actions, exactly as building does not end their own turn.
	if len(ns.TradeQueue) == 0 || ns.TradeQueue[0] != 1 {
		t.Fatalf("trade queue = %v, want seat 1 to keep the floor after building", ns.TradeQueue)
	}
}

// TestTheWindowAllowanceStopsASeatHoldingTheFloorForever: build and sell_house are both legal
// and both affordable, and neither pops the queue — so a policy that alternates them would
// stall the match. The allowance bounds that without bounding honest play.
func TestTheWindowAllowanceStopsASeatHoldingTheFloorForever(t *testing.T) {
	e := New(Config{Players: 3, StartingCash: 5000, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 1}
	s.Holdings[3] = Holding{Owner: 1}
	s.Current = 0
	s.Phase = PhaseTrade
	s.TradeQueue = []int{1, 2}

	ns := s
	var err error
	for i := 0; i < maxWindowActions; i++ {
		// Alternate build/sell on the same square: always legal, always affordable.
		act := ActBuild
		if i%2 == 1 {
			act = ActSellHouse
		}
		ns, _, err = e.Step(ns, 1, Action{Kind: act, Property: 1}, testSeed)
		if err != nil {
			t.Fatalf("action %d (%s) refused early: %v", i, act, err)
		}
	}
	if _, _, err = e.Step(ns, 1, Action{Kind: ActBuild, Property: 1}, testSeed); err == nil {
		t.Fatalf("a seat took more than %d management actions in one window; a looping policy "+
			"would hold the floor forever and the match would never advance", maxWindowActions)
	}
}

// TestLegalActionsNeverOffersAMoveTheEngineWouldRefuse is the general form of the bug: the
// manage phase answered with a fixed list — [end_turn build sell_house mortgage unmortgage
// propose_trade] — on any board at all. An agent on an empty board was told `build` was legal,
// chose it, and got ErrIllegalAction. For an LLM agent that is a wasted decision AND a wasted
// model call.
func TestLegalActionsNeverOffersAMoveTheEngineWouldRefuse(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Current = 0
	s.Phase = PhaseManage // owns nothing, has built nothing, has mortgaged nothing

	for _, act := range e.LegalActions(s, 0) {
		if act == ActEndTurn || act == ActProposeTrade {
			continue // no property argument; always available
		}
		if _, _, err := e.Step(s, 0, Action{Kind: act}, testSeed); err == ErrIllegalAction {
			t.Errorf("%q was offered as legal on a board where the seat owns nothing, and the "+
				"engine then refused it", act)
		}
	}
	// Concretely: with nothing owned, none of the property verbs may be offered.
	for _, act := range e.LegalActions(s, 0) {
		switch act {
		case ActBuild, ActSellHouse, ActMortgage, ActUnmortgage:
			t.Errorf("%q offered to a seat that owns no property", act)
		}
	}
}

// TestBuildIsOfferedOnlyWhenTheBankHasThePiece: during a housing shortage `build` must stop
// being advertised. The bank's supply is a real constraint and an agent that keeps being
// offered a house the bank does not have burns a decision every turn.
func TestBuildIsOfferedOnlyWhenTheBankHasThePiece(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 5000, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0}
	s.Holdings[3] = Holding{Owner: 0}
	s.Current = 0
	s.Phase = PhaseManage

	if !containsAction(e.LegalActions(s, 0), ActBuild) {
		t.Fatal("build should be offered with a full group, cash, and houses in the bank")
	}
	s.HousesRemaining = 0
	if containsAction(e.LegalActions(s, 0), ActBuild) {
		t.Error("build offered while the bank has no houses left")
	}
}

func containsAction(acts []string, want string) bool {
	for _, a := range acts {
		if a == want {
			return true
		}
	}
	return false
}

// TestABidderMayRaiseCashDuringAnAuction pins the last of the timing rights: officially a
// bidder may sell houses and mortgage to fund a bid. A bid is capped at cash in hand, so
// without this an asset-rich, cash-poor seat was locked out of auctions it should win.
func TestABidderMayRaiseCashDuringAnAuction(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[6] = Holding{Owner: 0} // Oriental — mortgageable, worth $50 mortgaged
	s.Players[0].Cash = 20            // cash-poor, asset-rich
	s.Current = 0
	s.Phase = PhaseAuction
	s.Auction = &AuctionState{Property: 1, HighBid: 0, HighBidder: Bank,
		InAuction: []bool{true, true}, Current: 0}

	legal := e.LegalActions(s, 0)
	if !containsAction(legal, ActMortgage) {
		t.Fatalf("auction actions = %v; a bidder must be able to mortgage to fund a bid", legal)
	}
	// build and unmortgage SPEND money — offering them could not fund a bid and would let a
	// seat alternate build/sell forever without bidding or passing.
	if containsAction(legal, ActBuild) || containsAction(legal, ActUnmortgage) {
		t.Errorf("auction actions = %v; the cash-SPENDING verbs must not be offered", legal)
	}

	ns, _, err := e.Step(s, 0, Action{Kind: ActMortgage, Property: 6}, testSeed)
	if err != nil {
		t.Fatalf("mortgaging during an auction was refused: %v", err)
	}
	if ns.Players[0].Cash <= 20 {
		t.Fatalf("cash = %d, want the mortgage proceeds", ns.Players[0].Cash)
	}
	// The floor stays with the seat: it raised the money in order to bid.
	if ns.Auction == nil || ns.Auction.Current != 0 {
		t.Fatal("raising cash must not pass the bidding turn to the next seat")
	}
	// And it can now actually bid what it raised.
	ns2, _, err := e.Step(ns, 0, Action{Kind: ActBid, Amount: 60}, testSeed)
	if err != nil {
		t.Fatalf("bidding with the raised cash failed: %v", err)
	}
	if ns2.Auction.HighBid != 60 || ns2.Auction.HighBidder != 0 {
		t.Fatalf("high bid = %d by %d, want 60 by seat 0", ns2.Auction.HighBid, ns2.Auction.HighBidder)
	}
}

// ── the housing shortage auction ─────────────────────────────────────────────
//
// Official: "if there are a limited number of houses and hotels available and two or more
// players wish to buy more than the Bank has, the houses or hotels must be sold at auction to
// the highest bidder." The trigger chosen — the bank has at least one piece, and more seats
// could legally buy that piece than the bank has to sell — is documented in shortage.go.

// shortageBoard: seats 0 and 1 each own a complete, unmortgaged colour group, so both could
// legally build. Supply is set per test.
func shortageBoard(t *testing.T) (*Engine, State) {
	t.Helper()
	e := New(Config{Players: 3, StartingCash: 2000, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Holdings[1] = Holding{Owner: 0} // brown group — seat 0
	s.Holdings[3] = Holding{Owner: 0}
	s.Holdings[6] = Holding{Owner: 1} // light blue — seat 1
	s.Holdings[8] = Holding{Owner: 1}
	s.Holdings[9] = Holding{Owner: 1}
	s.Current = 0
	s.Phase = PhaseManage
	return e, s
}

func TestAPlentifulBankNeverTriggersAnAuction(t *testing.T) {
	e, s := shortageBoard(t)
	s.HousesRemaining = 32 // no shortage at all
	ns, _, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns.Auction != nil {
		t.Fatal("an auction opened with 32 houses in the bank; the common case must be untouched")
	}
	if ns.Holdings[1].Houses != 1 {
		t.Fatalf("houses = %d, want the ordinary build to have happened", ns.Holdings[1].Houses)
	}
}

func TestAShortageIsNotContestedWhenOnlyOneSeatCouldBuild(t *testing.T) {
	e, s := shortageBoard(t)
	s.HousesRemaining = 1
	// Take seat 1 out of contention: mortgage one of its group, so it cannot build.
	s.Holdings[6] = Holding{Owner: 1, Mortgaged: true}
	ns, _, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns.Auction != nil {
		t.Fatal("one house and ONE eligible builder is not a contest — the rule needs two or " +
			"more players wanting more than the bank has")
	}
	if ns.Holdings[1].Houses != 1 || ns.HousesRemaining != 0 {
		t.Fatalf("the uncontested build did not happen: houses=%d remaining=%d",
			ns.Holdings[1].Houses, ns.HousesRemaining)
	}
}

func TestAnEmptyBankStillMeansWaitNotAuction(t *testing.T) {
	e, s := shortageBoard(t)
	s.HousesRemaining = 0
	if containsAction(e.LegalActions(s, 0), ActBuild) {
		t.Fatal("build offered with an empty bank")
	}
	if _, _, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed); err == nil {
		t.Fatal("building from an empty bank must fail; officially players WAIT for houses to " +
			"come back, and there is nothing to auction")
	}
}

func TestAContestedHouseGoesToAuctionAndCanBeOutbid(t *testing.T) {
	e, s := shortageBoard(t)
	s.HousesRemaining = 1 // one house, two eligible builders

	ns, evs, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed)
	if err != nil {
		t.Fatalf("contested build errored instead of opening an auction: %v", err)
	}
	if ns.Auction == nil || !ns.Auction.House {
		t.Fatal("no housing-shortage auction opened")
	}
	if evs[0].Type != EvHouseAuctionStarted {
		t.Fatalf("first event = %s, want house_auction_started", evs[0].Type)
	}
	// The initiator's list price is the standing bid, so triggering costs it nothing.
	if ns.Auction.HighBidder != 0 || ns.Auction.HighBid != space(1).HouseCost {
		t.Fatalf("opening bid = %d by seat %d, want the initiator at list price %d",
			ns.Auction.HighBid, ns.Auction.HighBidder, space(1).HouseCost)
	}
	// The house has NOT been taken from the bank yet — the auction decides who gets it.
	if ns.HousesRemaining != 1 {
		t.Fatalf("bank supply = %d before the auction closed, want 1", ns.HousesRemaining)
	}
	// Seat 2 owns nothing, so it cannot bid on a house it could not place.
	if ns.Auction.InAuction[2] {
		t.Error("a seat that could not legally build was put in the auction")
	}
	// Bidding starts with the next eligible seat, not the initiator outbidding itself.
	if ns.Auction.Current != 1 {
		t.Fatalf("first bidder = %d, want seat 1", ns.Auction.Current)
	}

	// Seat 1 outbids, naming ITS square.
	ns2, _, err := e.Step(ns, 1, Action{Kind: ActBid, Amount: 120, Property: 6}, testSeed)
	if err != nil {
		t.Fatalf("outbid refused: %v", err)
	}
	// The auction does NOT close yet: seat 0 is still in and must get the chance to
	// respond to the raise. That is ordinary ascending-auction behaviour and the reason
	// this assertion was wrong the first time it was written.
	if ns2.Auction == nil || ns2.Auction.Current != 0 {
		t.Fatalf("after a raise the previous high bidder must get a chance to respond: %+v", ns2.Auction)
	}
	// Seat 0 declines to go higher, and the auction closes.
	ns2, _, err = e.Step(ns2, 0, Action{Kind: ActPass}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns2.Auction != nil {
		t.Fatalf("auction still open after every rival passed: %+v", ns2.Auction)
	}
	if ns2.Holdings[6].Houses != 1 {
		t.Fatalf("the winner's square has %d houses, want 1 — the piece must land on the "+
			"square the winner NAMED, not on the initiator's", ns2.Holdings[6].Houses)
	}
	if ns2.Holdings[1].Houses != 0 {
		t.Error("the initiator got a house despite being outbid")
	}
	if ns2.Players[1].Cash != 2000-120 {
		t.Fatalf("winner cash = %d, want the bid deducted", ns2.Players[1].Cash)
	}
	if ns2.HousesRemaining != 0 {
		t.Fatalf("bank supply = %d, want the auctioned house gone", ns2.HousesRemaining)
	}
	if ns2.Phase != PhaseManage {
		t.Fatalf("phase = %s, want the interrupted phase resumed", ns2.Phase)
	}
}

func TestAnUnopposedInitiatorPaysListPrice(t *testing.T) {
	e, s := shortageBoard(t)
	s.HousesRemaining = 1
	ns, _, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	// Seat 1 passes: nobody wants to pay more.
	ns2, _, err := e.Step(ns, 1, Action{Kind: ActPass}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns2.Holdings[1].Houses != 1 {
		t.Fatal("the initiator did not get the house it was never outbid for")
	}
	if ns2.Players[0].Cash != 2000-space(1).HouseCost {
		t.Fatalf("initiator paid %d, want list price %d — triggering a contest must never "+
			"cost the initiator anything", 2000-ns2.Players[0].Cash, space(1).HouseCost)
	}
}

func TestABidMustNameASquareTheBidderCanBuildOn(t *testing.T) {
	e, s := shortageBoard(t)
	s.HousesRemaining = 1
	ns, _, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	// Seat 1 bids on seat 0's square. Winning it would leave the piece unplaceable.
	if _, _, err := e.Step(ns, 1, Action{Kind: ActBid, Amount: 200, Property: 1}, testSeed); err == nil {
		t.Fatal("a bid naming a square the bidder cannot build on was accepted")
	}
	// And on a square nobody owns.
	if _, _, err := e.Step(ns, 1, Action{Kind: ActBid, Amount: 200, Property: 11}, testSeed); err == nil {
		t.Fatal("a bid naming an unowned square was accepted")
	}
}

func TestSellingHousesIsWithheldDuringAShortageAuction(t *testing.T) {
	e, s := shortageBoard(t)
	s.HousesRemaining = 1
	s.Holdings[6] = Holding{Owner: 1, Houses: 1} // seat 1 has a house it could sell back
	s.Holdings[8] = Holding{Owner: 1, Houses: 1}
	s.Holdings[9] = Holding{Owner: 1, Houses: 1}
	ns, _, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	if ns.Auction == nil {
		t.Fatal("expected a contested auction")
	}
	legal := e.LegalActions(ns, ns.Auction.Current)
	if containsAction(legal, ActSellHouse) {
		t.Errorf("sell_house offered during a shortage auction (%v): returning pieces to the "+
			"bank mid-contest would move the very supply being fought over", legal)
	}
	if !containsAction(legal, ActMortgage) && anyMortgageable(&ns, ns.Auction.Current) {
		t.Error("mortgage withheld too; it raises cash without touching the supply")
	}
}

// TestARejectedBidLeavesTheAuctionUntouched pins the transactional guarantee against the new
// Targets slice: Step returns the ORIGINAL state on error, and a shared backing array would
// let a refused bid's target survive the rollback.
func TestARejectedBidLeavesTheAuctionUntouched(t *testing.T) {
	e, s := shortageBoard(t)
	s.HousesRemaining = 1
	ns, _, err := e.Step(s, 0, Action{Kind: ActBuild, Property: 1}, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]int(nil), ns.Auction.Targets...)
	if _, _, err := e.Step(ns, 1, Action{Kind: ActBid, Amount: 200, Property: 1}, testSeed); err == nil {
		t.Fatal("expected the illegal bid to be refused")
	}
	for i := range before {
		if ns.Auction.Targets[i] != before[i] {
			t.Fatalf("targets mutated by a REFUSED bid: %v -> %v", before, ns.Auction.Targets)
		}
	}
}
