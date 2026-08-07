package monopoly

import (
	"errors"
	"testing"
)

// A bid that names no amount must say SO, rather than be reported as a bid that failed to
// beat the high bid.
//
// The two are different mistakes with different fixes. "Your bid did not exceed the current
// high bid" sends a developer to read the auction state and raise their number; the actual
// problem is that the `amount` field never reached the server, so no number they choose will
// work until they fix the serialization. An accurate error is the difference between a
// one-minute fix and an afternoon.
//
// Amount is a plain int, so an absent field and an explicit 0 both arrive as 0 and cannot be
// told apart. That is fine here and the message is worded for it: a bid of 0 is never legal
// either (the high bid starts at 0 and a bid must exceed it), so "no usable amount" is a true
// statement about both cases rather than a guess about which one happened.
func TestBidWithoutAmountIsNotReportedAsALowBid(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Auction = &AuctionState{
		Property:   1,
		HighBid:    0,
		HighBidder: Bank,
		InAuction:  []bool{true, true},
		Current:    s.Current,
	}
	s.Phase = PhaseAuction

	_, _, err := e.Step(s, s.Current, Action{Kind: ActBid}, testSeed)
	if !errors.Is(err, ErrBidAmountMissing) {
		t.Fatalf("bid with no amount: got %v, want ErrBidAmountMissing", err)
	}
	// The blanket phase error is the specific misdiagnosis this test exists to prevent: the
	// phase was correct, the seat was correct, and only the field was absent.
	if errors.Is(err, ErrIllegalAction) {
		t.Fatal("a missing amount was reported as an illegal action for the phase")
	}
}

// The genuinely-too-low bid keeps its own error. Collapsing the two in the other direction
// would be the same defect mirrored.
func TestBidBelowHighBidStillReportsTheHighBid(t *testing.T) {
	e := New(Config{Players: 2, StartingCash: 1500, MaxTurns: 300})
	s, _ := e.Init(testSeed)
	s.Auction = &AuctionState{
		Property:   1,
		HighBid:    50,
		HighBidder: 1,
		InAuction:  []bool{true, true},
		Current:    s.Current,
	}
	s.Phase = PhaseAuction

	_, _, err := e.Step(s, s.Current, Action{Kind: ActBid, Amount: 20}, testSeed)
	if !errors.Is(err, ErrInvalidBid) {
		t.Fatalf("bid of 20 under a high bid of 50: got %v, want ErrInvalidBid", err)
	}
	if errors.Is(err, ErrBidAmountMissing) {
		t.Fatal("a real but too-low bid was reported as a missing amount")
	}
}
