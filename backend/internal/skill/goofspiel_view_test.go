package skill

import "testing"

// A view in the exact shape agent_match_decisions.input_json stores. Round 4, with three
// rounds of history — so the opponent has revealed 13, 3 and 4, and prizes 13, 3 and 4
// have been contested.
const realShapedView = `{
  "game":"goofspiel","seat":0,"round":4,"current_prize":7,"prize_pool":7,
  "your_hand":[2,4,5,6,7,8,9,10,11,12],
  "legal_actions":[2,4,5,6,7,8,9,10,11,12],
  "scores":[3,17],
  "history":[
    {"round":1,"prize":13,"prize_pool":13,"your_card":1,"opp_card":13,"winner":1},
    {"round":2,"prize":3,"prize_pool":3,"your_card":13,"opp_card":3,"winner":0},
    {"round":3,"prize":4,"prize_pool":4,"your_card":3,"opp_card":4,"winner":1}
  ]
}`

// The reconstruction is EXACT, not estimated — that is what lets the scorer run offline
// over stored decisions with no schema change and no live match.
func TestReconstructsOpponentHandAndRemainingPrizes(t *testing.T) {
	st, ok := GoofspielStateFromView([]byte(realShapedView))
	if !ok {
		t.Fatal("a well-formed stored view was rejected")
	}
	if st.Round != 4 || st.Prize != 7 {
		t.Fatalf("round=%d prize=%d want 4/7", st.Round, st.Prize)
	}

	// The opponent revealed 13, 3, 4 — so it holds everything else.
	wantOpp := map[int]bool{1: true, 2: true, 5: true, 6: true, 7: true, 8: true, 9: true, 10: true, 11: true, 12: true}
	if len(st.OppHand) != len(wantOpp) {
		t.Fatalf("opponent hand %v has %d cards, want %d", st.OppHand, len(st.OppHand), len(wantOpp))
	}
	for _, c := range st.OppHand {
		if !wantOpp[c] {
			t.Errorf("opponent hand contains %d, which it already played", c)
		}
	}

	// Prizes 13, 3, 4 are spent and 7 is on the table now; the rest are still to come.
	wantRemaining := map[int]bool{1: true, 2: true, 5: true, 6: true, 8: true, 9: true, 10: true, 11: true, 12: true}
	if len(st.RemainingPrizes) != len(wantRemaining) {
		t.Fatalf("remaining prizes %v has %d, want %d", st.RemainingPrizes, len(st.RemainingPrizes), len(wantRemaining))
	}
	for _, p := range st.RemainingPrizes {
		if !wantRemaining[p] {
			t.Errorf("prize %d is listed as remaining but was already contested", p)
		}
	}

	// And the reconstructed state must actually score.
	if _, ok := ScoreGoofspielBid(st, 12); !ok {
		t.Fatal("the reconstructed state could not be scored")
	}
}

// A tie carries the pool, and the carried pool is what is really being contested. Observed
// live: a round showing prize 6 with a pool of 19 after two ties. Scoring against the bare
// prize would under-price exactly the rounds where the stakes had built up — the ones an
// agent most needs to get right.
func TestCarriedPoolIsWhatGetsScored(t *testing.T) {
	carried := `{
	  "game":"goofspiel","round":5,"current_prize":6,"prize_pool":19,
	  "your_hand":[4,8,11,13],"legal_actions":[4,8,11,13],
	  "history":[{"round":4,"prize":7,"prize_pool":7,"your_card":2,"opp_card":2,"winner":2}]
	}`
	st, ok := GoofspielStateFromView([]byte(carried))
	if !ok {
		t.Fatal("rejected a valid carried-pool view")
	}
	if st.Prize != 19 {
		t.Fatalf("scored against prize %d, want the carried pool of 19 — a round where two "+
			"ties have stacked the pot is not a 6-point round", st.Prize)
	}
}

// Malformed or foreign input is EXCLUDED, never guessed at. A scorer that invents missing
// inputs produces numbers that look reasonable and rank agents wrongly.
func TestUnusableViewsAreRejected(t *testing.T) {
	for name, doc := range map[string]string{
		"not json":       `{nope`,
		"another game":   `{"game":"mafia","round":1,"your_hand":[1,2]}`,
		"no hand at all": `{"game":"goofspiel","round":1,"current_prize":5}`,
	} {
		if _, ok := GoofspielStateFromView([]byte(doc)); ok {
			t.Errorf("%s: accepted an unusable view", name)
		}
	}
}
