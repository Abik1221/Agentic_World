package goofspiel

import "testing"

// openEngine builds a small deterministic game (cards 1..3, fixed prize order
// 1,2,3) so scenarios can be hand-computed.
func openEngine(t *testing.T) *Engine {
	t.Helper()
	return New(Config{Cards: []int{1, 2, 3}, Rounds: 3, FairnessMode: FairnessOpen})
}

// playScript drives a full match from per-round card choices for both seats.
func playScript(t *testing.T, eng *Engine, seed []byte, a, b []int) (State, []Event) {
	t.Helper()
	s, evs := eng.Init(seed)
	for i := 0; !s.Finished; i++ {
		if i >= len(a) || i >= len(b) {
			t.Fatalf("ran out of scripted moves at round %d", s.Round)
		}
		var e1, e2, e3 []Event
		var err error
		if s, e1, err = eng.Seal(s, SeatA, a[i]); err != nil {
			t.Fatalf("round %d seal A(%d): %v", i+1, a[i], err)
		}
		if s, e2, err = eng.Seal(s, SeatB, b[i]); err != nil {
			t.Fatalf("round %d seal B(%d): %v", i+1, b[i], err)
		}
		if s, e3, err = eng.Resolve(s); err != nil {
			t.Fatalf("round %d resolve: %v", i+1, err)
		}
		evs = append(evs, e1...)
		evs = append(evs, e2...)
		evs = append(evs, e3...)
	}
	return s, evs
}

func TestGolden_SimpleWin(t *testing.T) {
	eng := openEngine(t)
	// R1 prize1: A3>B1 → A+1. R2 prize2: A1<B3 → B+2. R3 prize3: A2=B2 tie (last round, pool unawarded).
	s, _ := playScript(t, eng, []byte("seed"), []int{3, 1, 2}, []int{1, 3, 2})
	if !s.Finished {
		t.Fatal("match should be finished")
	}
	if s.Scores != [2]int{1, 2} {
		t.Fatalf("scores = %v, want [1 2]", s.Scores)
	}
	if s.Winner != SeatB {
		t.Fatalf("winner = %d, want SeatB", s.Winner)
	}
	if len(s.History) != 3 {
		t.Fatalf("history len = %d, want 3", len(s.History))
	}
	if len(s.Hands[SeatA]) != 0 || len(s.Hands[SeatB]) != 0 {
		t.Fatalf("hands not empty: %v", s.Hands)
	}
}

func TestGolden_TieCarriesAndStacks(t *testing.T) {
	eng := openEngine(t)
	// R1 prize1: A2=B2 tie → pool(1) carries. R2 contested pool = 1+2 = 3: A3>B1 → A+3.
	// R3 prize3 pool3: A1<B3 → B+3. Final 3-3 tie.
	s, _ := playScript(t, eng, []byte("x"), []int{2, 3, 1}, []int{2, 1, 3})
	if s.History[0].Winner != Tie {
		t.Fatalf("round1 winner = %d, want Tie", s.History[0].Winner)
	}
	if s.History[1].PrizePool != 3 {
		t.Fatalf("round2 pool = %d, want 3 (carry+stack)", s.History[1].PrizePool)
	}
	if s.History[1].Scores != [2]int{3, 0} {
		t.Fatalf("after round2 scores = %v, want [3 0]", s.History[1].Scores)
	}
	if s.Scores != [2]int{3, 3} || s.Winner != Tie {
		t.Fatalf("final scores=%v winner=%d, want [3 3] Tie", s.Scores, s.Winner)
	}
}

func TestSealRejections(t *testing.T) {
	eng := openEngine(t)
	s, _ := eng.Init([]byte("s"))

	if _, _, err := eng.Seal(s, SeatA, 99); err != ErrIllegalCard {
		t.Fatalf("illegal card err = %v, want ErrIllegalCard", err)
	}
	if _, _, err := eng.Seal(s, 5, 1); err != ErrInvalidSeat {
		t.Fatalf("invalid seat err = %v, want ErrInvalidSeat", err)
	}
	// Resolve before both sealed.
	s2, _, _ := eng.Seal(s, SeatA, 1)
	if _, _, err := eng.Resolve(s2); err != ErrNotReady {
		t.Fatalf("resolve err = %v, want ErrNotReady", err)
	}
	// Double seal.
	if _, _, err := eng.Seal(s2, SeatA, 2); err != ErrAlreadyActed {
		t.Fatalf("double seal err = %v, want ErrAlreadyActed", err)
	}
}

func TestForceTimeoutPlaysLowestDeterministically(t *testing.T) {
	eng := openEngine(t)
	s, _ := eng.Init([]byte("seed-1"))
	a, _, errA := eng.ForceTimeout(s, SeatA)
	b, _, errB := eng.ForceTimeout(s, SeatA)
	if errA != nil || errB != nil {
		t.Fatalf("timeout errors: %v %v", errA, errB)
	}
	if *a.Sealed[SeatA] != *b.Sealed[SeatA] {
		t.Fatalf("forced card not reproducible: %d vs %d", *a.Sealed[SeatA], *b.Sealed[SeatA])
	}
	// Least-harmful default = the lowest card in hand.
	lowest := s.Hands[SeatA][0]
	for _, c := range s.Hands[SeatA] {
		if c < lowest {
			lowest = c
		}
	}
	if *a.Sealed[SeatA] != lowest {
		t.Fatalf("forced card = %d, want lowest in hand %d", *a.Sealed[SeatA], lowest)
	}
}

func TestPurityNoMutation(t *testing.T) {
	eng := openEngine(t)
	s, _ := eng.Init([]byte("s"))
	before := append([]int(nil), s.Hands[SeatA]...)
	_, _, _ = eng.Seal(s, SeatA, 1) // must not mutate caller's state
	for i := range before {
		if s.Hands[SeatA][i] != before[i] {
			t.Fatal("Seal mutated the input state's hand")
		}
	}
	if s.Sealed[SeatA] != nil {
		t.Fatal("Seal mutated the input state's sealed slot")
	}
}
