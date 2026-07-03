package goofspiel

import "testing"

// playAllTies runs a 2-round game (prize cards {2,4}) where every round is tied,
// under the given tie rule, and returns the final state. Both pools ({2,4}) are
// even, so the outcome is independent of the seed-shuffled prize order.
func playAllTies(t *testing.T, tieRule string) State {
	t.Helper()
	e := New(Config{Cards: []int{2, 4}, Rounds: 2, FairnessMode: FairnessShuffled, TieRule: tieRule})
	s, _ := e.Init([]byte("tie-seed"))

	seal := func(card int) {
		var err error
		if s, _, err = e.Seal(s, SeatA, card); err != nil {
			t.Fatalf("seal A %d: %v", card, err)
		}
		if s, _, err = e.Seal(s, SeatB, card); err != nil {
			t.Fatalf("seal B %d: %v", card, err)
		}
		if s, _, err = e.Resolve(s); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	seal(4) // round 1: both play 4 → tie
	seal(2) // round 2: both play 2 → tie
	if !s.Finished {
		t.Fatal("game did not finish")
	}
	return s
}

// TestTieSplitAwardsHalf: under TieSplit each seat is awarded half of every tied
// pool, so the whole 2+4=6 points are shared 3/3.
func TestTieSplitAwardsHalf(t *testing.T) {
	s := playAllTies(t, TieSplit)
	if s.Scores[SeatA] != 3 || s.Scores[SeatB] != 3 {
		t.Fatalf("split scores = %v, want [3 3]", s.Scores)
	}
}

// TestTieCarryDefault: under TieCarry a tie awards nothing and stacks the pool;
// an all-tie game therefore ends 0/0 (the final tied pool is never awarded). The
// empty tie rule must behave identically (default).
func TestTieCarryDefault(t *testing.T) {
	carry := playAllTies(t, TieCarry)
	if carry.Scores[SeatA] != 0 || carry.Scores[SeatB] != 0 {
		t.Fatalf("carry scores = %v, want [0 0]", carry.Scores)
	}
	def := playAllTies(t, "")
	if def.Scores != carry.Scores {
		t.Fatalf("empty tie rule = %v, want carry default %v", def.Scores, carry.Scores)
	}
}
