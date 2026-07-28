package mafia

import "testing"

// An ABANDONED table must still terminate.
//
// ForceTimeout is a pure abstain — it eliminates nobody — so with MaxDays unbounded a
// table where every agent went silent looped forever: phase timed out, nobody died,
// day advanced, repeat. On a staked table that meant the match never finished,
// settlement never ran, and the escrowed stakes were locked permanently. The day cap
// is what guarantees an operator never has to hand-unwind escrow.
func TestAbandonedTableTerminatesUnderDayCap(t *testing.T) {
	const maxDays = 14
	e := NewWithMaxDays(maxDays)
	s, _ := e.Init(seed, StandardSeats())

	// Nobody ever acts: drive only forced phase timeouts, exactly as the sweeper does.
	for i := 0; i < 5000 && !s.Finished; i++ {
		ns, _, err := e.ForceTimeout(s, seed)
		if err != nil {
			t.Fatalf("ForceTimeout: %v", err)
		}
		s = ns
		if s.Day > maxDays+1 {
			t.Fatalf("table ran past the day cap (day %d) — escrow would be stranded", s.Day)
		}
	}

	if !s.Finished {
		t.Fatal("an all-silent table never finished; its stakes would be locked in escrow forever")
	}
	// A capped game is decided by surviving majority, so it settles on the state of
	// play rather than being voided — there must be a winning team to pay out.
	if s.Winner == "" {
		t.Fatal("capped game finished with no winning team; settlement would have no payee")
	}
}

// The unbounded default is what made the strand possible; assert the constructor we
// now use in the service is genuinely bounded.
func TestNewWithMaxDaysIsBounded(t *testing.T) {
	if got := NewWithMaxDays(14).MaxDays; got != 14 {
		t.Fatalf("MaxDays = %d, want 14", got)
	}
	if got := New().MaxDays; got != 0 {
		t.Fatalf("New() should stay unbounded (0) for callers that opt in; got %d", got)
	}
}
