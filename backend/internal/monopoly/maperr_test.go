package monopoly

import (
	"errors"
	"fmt"
	"testing"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/httpx"
)

// Every engine rejection must reach the agent under its own code.
//
// The regression this locks down: all of these once became illegal_action, "That action is not
// legal in the current phase." For four of the five that sentence is false, and it is specific
// enough that a developer acts on it — checking a phase that was correct while the real cause
// (an absent field, short cash, a bad index) went unmentioned.
func TestEngineErrorsKeepTheirOwnCode(t *testing.T) {
	for _, tc := range []struct {
		in   error
		code string
	}{
		{mono.ErrBidAmountMissing, "bid_amount_missing"},
		{mono.ErrInvalidBid, "bid_too_low"},
		{mono.ErrInsufficientFunds, "insufficient_funds"},
		{mono.ErrInvalidProperty, "invalid_property"},
		{mono.ErrEmptyMessage, "empty_message"},
		{mono.ErrNotYourTurn, "not_your_turn"},
		{mono.ErrFinished, "match_not_active"},
		{mono.ErrIllegalAction, "illegal_action"},
	} {
		got := mapEngineErr(tc.in)
		var he *httpx.APIError
		if !errors.As(got, &he) {
			t.Fatalf("%v mapped to %T, which carries no API code", tc.in, got)
		}
		if he.Code != tc.code {
			t.Errorf("%v -> code %q, want %q", tc.in, he.Code, tc.code)
		}
	}
}

// An engine error added later and never mapped degrades to the blanket message rather than
// leaking an internal Go string — which would put "monopoly: ..." in front of an agent.
func TestUnmappedEngineErrorDegradesToIllegalAction(t *testing.T) {
	got := mapEngineErr(fmt.Errorf("monopoly: some rule added next year"))
	var he *httpx.APIError
	if !errors.As(got, &he) || he.Code != "illegal_action" {
		t.Fatalf("unmapped error -> %v, want illegal_action", got)
	}
}
