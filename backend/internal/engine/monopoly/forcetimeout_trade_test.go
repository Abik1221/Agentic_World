package monopoly

import (
	"encoding/json"
	"os"
	"testing"
)

// A staked table that reaches the trade phase and then times out must not hang forever.
//
// Five live tables were found wedged exactly here — all five in phase=trade, the oldest for
// two days, with 2,500 coins sitting in escrow that nothing would ever release. The sweeper
// selected them correctly on every tick and then dropped them, because ForceTimeout returned
// no events and the service reads "no events" as "nothing to do" rather than as a failure.
//
// The state loaded here is the real one, taken from a wedged production-shaped match rather
// than constructed, so the test cannot pass by modelling the bug away.
func TestForceTimeoutAdvancesAWedgedTradePhase(t *testing.T) {
	raw, err := os.ReadFile("testdata/stuck-trade-phase.json")
	if err != nil {
		t.Skipf("no captured state: %v", err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parse captured state: %v", err)
	}
	if s.Phase != PhaseTrade {
		t.Fatalf("captured state is phase %q, expected the trade phase this test exists for", s.Phase)
	}

	eng := New(Config{Players: len(s.Players), StartingCash: 1500, MaxTurns: DefaultMaxTurns, GoSalary: 200})
	next, events, err := eng.ForceTimeout(s, []byte("seed-for-a-stuck-table-000000000"))
	if err != nil {
		t.Fatalf("ForceTimeout errored: %v", err)
	}
	// The contract that matters is ADVANCEMENT, not events. stepTradeWindow legitimately
	// pops the queue and enters play while emitting nothing observable, so asserting on
	// events would pin an implementation detail — and it was the SERVICE reading "no events"
	// as "nothing happened" that wedged the tables, not the engine's silence.
	advanced := next.Phase != s.Phase || next.Current != s.Current ||
		len(next.TradeQueue) != len(s.TradeQueue)
	if !advanced {
		t.Fatalf("ForceTimeout left a wedged trade phase unchanged (phase=%v seat=%d queue=%d). "+
			"Nothing downstream can unstick the table, so a staked match hangs with its escrow "+
			"locked.", next.Phase, next.Current, len(next.TradeQueue))
	}
	t.Logf("advanced: phase %v -> %v, seat %d -> %d, events=%d",
		s.Phase, next.Phase, s.Current, next.Current, len(events))
}
