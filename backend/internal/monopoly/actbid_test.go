package monopoly

import (
	"context"
	"errors"
	"testing"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/httpx"
)

// The composition, through the real Act path.
//
// The two halves are covered separately — the engine returns ErrBidAmountMissing, and
// mapEngineErr turns it into bid_amount_missing — but neither proves they are WIRED. The
// original defect lived exactly in the join: Act discarded a correct engine diagnosis one line
// after receiving it. A test of either half alone would have passed throughout.
func TestActBidWithoutAmountReachesTheAgentAsBidAmountMissing(t *testing.T) {
	seed := make([]byte, 32)
	state, _ := mono.New(matchConfig(2)).Init(seed)
	// Park the match mid-auction with the acting seat on the block.
	state.Phase = mono.PhaseAuction
	state.Auction = &mono.AuctionState{
		Property:   1,
		HighBid:    0,
		HighBidder: mono.Bank,
		InAuction:  []bool{true, true},
		Current:    state.Current,
	}
	deadline := time.Unix(9_000, 0)
	tmpl := &Match{
		Status: StatusActive, EntryFee: 0, Players: 2, Seed: seed, State: state,
		RoundDeadline: &deadline,
		Agents:        []Player{{Seat: state.Current, AgentPublicID: "agent-bidder"}},
	}
	svc := NewService(&fakeRepo{activeTemplate: tmpl}, fakeLock{}, nil, fakeBcast{}, nil,
		fakeClock{t: time.Unix(2_000, 0)}, Config{})

	_, err := svc.Act(context.Background(), "agent-bidder", "match-1",
		mono.Action{Kind: mono.ActBid}, "", false)

	var he *httpx.APIError
	if !errors.As(err, &he) {
		t.Fatalf("Act returned %v (%T), want an API error", err, err)
	}
	if he.Code != "bid_amount_missing" {
		t.Fatalf("code = %q, want bid_amount_missing — an agent whose amount field never "+
			"arrived is being sent to check something else", he.Code)
	}
}
