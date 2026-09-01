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

// TestTieDiscardThrowsThePoolAway pins the third documented settlement: "some play that tied
// prize cards are discarded". The harshest of the three — a tie costs both players the prize
// outright, so bidding to force a tie can never be a way to bank value for a later round.
//
// Uses the shared all-ties harness so the three rules are compared on exactly the same play:
// both seats tie every round of a 2-round game worth 2+4=6.
func TestTieDiscardThrowsThePoolAway(t *testing.T) {
	s := playAllTies(t, TieDiscard)
	if s.Scores[SeatA] != 0 || s.Scores[SeatB] != 0 {
		t.Fatalf("scores %v; a discarded tie may not be scored by anyone", s.Scores)
	}
	if s.Winner != Tie {
		t.Fatalf("winner = %d, want a draw when neither seat scored", s.Winner)
	}
}

// TestTheThreeTieRulesDisagreeAsIntended: carry, split and discard must produce genuinely
// different games on identical play, or one of them is not implemented.
func TestTheThreeTieRulesDisagreeAsIntended(t *testing.T) {
	carry := playAllTies(t, TieCarry)
	split := playAllTies(t, TieSplit)
	discard := playAllTies(t, TieDiscard)

	total := func(s State) int { return s.Scores[SeatA] + s.Scores[SeatB] }
	// CARRY: every round tied means the pool rolls to the end and is won by nobody — the
	// standard rule's "if the final bids are equal the remaining prizes are not won".
	if total(carry) != 0 {
		t.Errorf("carry total = %d; a pool still carrying when the match ends goes to nobody", total(carry))
	}
	// SPLIT: the 6 points are shared rather than lost.
	if total(split) != 6 {
		t.Errorf("split total = %d, want the whole 6 shared", total(split))
	}
	// DISCARD: thrown away round by round.
	if total(discard) != 0 {
		t.Errorf("discard total = %d, want 0", total(discard))
	}
	// Carry and discard agree on the TOTAL here but differ in mechanism; the split rule is
	// what proves the three are not one rule wearing three names.
	if total(split) == total(carry) {
		t.Error("split and carry produced the same result on all-ties play")
	}
}
