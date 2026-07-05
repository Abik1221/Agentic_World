package goofspiel

import (
	"math/rand"
	"sort"
	"testing"
)

func TestPrizeOrderDeterministic(t *testing.T) {
	cfg := DefaultConfig()
	seed := []byte("a-fixed-seed")
	o1 := derivePrizeOrder(cfg, seed)
	o2 := derivePrizeOrder(cfg, seed)
	if !equalInts(o1, o2) {
		t.Fatalf("same seed produced different orders:\n%v\n%v", o1, o2)
	}
	// Must be a permutation of the deck.
	sorted := append([]int(nil), o1...)
	sort.Ints(sorted)
	for i, v := range cfg.Cards {
		if sorted[i] != v {
			t.Fatalf("prize order is not a permutation of the deck: %v", o1)
		}
	}
}

func TestOpenModeFixedOrder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FairnessMode = FairnessOpen
	if !equalInts(derivePrizeOrder(cfg, []byte("ignored")), cfg.Cards) {
		t.Fatal("open mode must use the fixed card order")
	}
}

// randomPolicy plays a uniformly random legal card, deterministically per rng.
func randomPolicy(rng *rand.Rand) func(s State, seat int) int {
	return func(s State, seat int) int {
		hand := s.Hands[seat]
		return hand[rng.Intn(len(hand))]
	}
}

func drive(t *testing.T, eng *Engine, seed []byte, pick func(State, int) int) (State, []Event) {
	t.Helper()
	s, evs := eng.Init(seed)
	for !s.Finished {
		var e1, e2, e3 []Event
		var err error
		if s, e1, err = eng.Seal(s, SeatA, pick(s, SeatA)); err != nil {
			t.Fatal(err)
		}
		if s, e2, err = eng.Seal(s, SeatB, pick(s, SeatB)); err != nil {
			t.Fatal(err)
		}
		if s, e3, err = eng.Resolve(s); err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e1...)
		evs = append(evs, e2...)
		evs = append(evs, e3...)
	}
	return s, evs
}

func TestFullMatchInvariants(t *testing.T) {
	eng := New(DefaultConfig())
	deckSum := 0
	for _, c := range eng.Config().Cards {
		deckSum += c
	}
	for trial := 0; trial < 500; trial++ {
		rng := rand.New(rand.NewSource(int64(trial)))
		s, _ := drive(t, eng, []byte{byte(trial), byte(trial >> 8)}, randomPolicy(rng))

		if !s.Finished {
			t.Fatalf("trial %d: not finished", trial)
		}
		if len(s.History) != eng.Config().Rounds {
			t.Fatalf("trial %d: %d rounds, want %d", trial, len(s.History), eng.Config().Rounds)
		}
		if len(s.Hands[SeatA]) != 0 || len(s.Hands[SeatB]) != 0 {
			t.Fatalf("trial %d: hands not empty", trial)
		}
		// Each seat must have played every card exactly once.
		assertPlayedWholeDeck(t, eng.Config().Cards, s.History, trial)
		// Total awarded == sum of pools on rounds with a winner; total ≤ deck sum.
		awarded := s.Scores[SeatA] + s.Scores[SeatB]
		var expected int
		for _, r := range s.History {
			if r.Winner != Tie {
				expected += r.PrizePool
			}
		}
		if awarded != expected {
			t.Fatalf("trial %d: awarded %d != sum of won pools %d", trial, awarded, expected)
		}
		if awarded > deckSum {
			t.Fatalf("trial %d: awarded %d exceeds deck sum %d", trial, awarded, deckSum)
		}
	}
}

func TestDeterminismSameSeedSamePolicy(t *testing.T) {
	eng := New(DefaultConfig())
	policy := func(s State, seat int) int { return s.Hands[seat][0] } // deterministic, always legal
	s1, e1 := drive(t, eng, []byte("seed-Z"), policy)
	s2, e2 := drive(t, eng, []byte("seed-Z"), policy)
	if s1.Scores != s2.Scores || s1.Winner != s2.Winner {
		t.Fatalf("nondeterministic outcome: %v/%d vs %v/%d", s1.Scores, s1.Winner, s2.Scores, s2.Winner)
	}
	if len(e1) != len(e2) {
		t.Fatalf("event count differs: %d vs %d", len(e1), len(e2))
	}
}

// TestFinalRoundTieDiscardsPool locks the documented carry-over rule: ties carry
// and stack, and a tie on the LAST round leaves the carried pool unawarded.
func TestFinalRoundTieDiscardsPool(t *testing.T) {
	eng := New(Config{Cards: []int{1, 2}, Rounds: 2, FairnessMode: FairnessOpen})
	s, _ := eng.Init(nil) // open mode → prize order is the fixed [1,2]

	// Round 1: both play 1 → tie; the pool carries and stacks into round 2 (1+2=3).
	s, _, _ = eng.Seal(s, SeatA, 1)
	s, _, _ = eng.Seal(s, SeatB, 1)
	s, _, _ = eng.Resolve(s)
	if s.PrizePool != 3 {
		t.Fatalf("carried pool = %d, want 3 (round1 prize 1 + round2 prize 2)", s.PrizePool)
	}

	// Round 2 (final): both play 2 → tie; the carried pool is discarded.
	s, _, _ = eng.Seal(s, SeatA, 2)
	s, _, _ = eng.Seal(s, SeatB, 2)
	s, _, _ = eng.Resolve(s)
	if !s.Finished {
		t.Fatal("match should be finished after the last round")
	}
	if s.Scores != [2]int{0, 0} {
		t.Fatalf("scores = %v, want [0 0] (a final-round tie awards no one)", s.Scores)
	}
	if s.Winner != Tie {
		t.Fatalf("winner = %d, want Tie", s.Winner)
	}
}

func assertPlayedWholeDeck(t *testing.T, deck []int, history []RoundResult, trial int) {
	t.Helper()
	for seat := 0; seat < 2; seat++ {
		played := map[int]int{}
		for _, r := range history {
			played[r.Cards[seat]]++
		}
		if len(played) != len(deck) {
			t.Fatalf("trial %d seat %d: played %d distinct cards, want %d", trial, seat, len(played), len(deck))
		}
		for _, c := range deck {
			if played[c] != 1 {
				t.Fatalf("trial %d seat %d: card %d played %d times", trial, seat, c, played[c])
			}
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
