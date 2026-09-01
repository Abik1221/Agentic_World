package arenanorm

import (
	"math"
	"testing"
)

// TestQueueFilterExploitIsClosed is the reason this package exists.
//
// The audit found roughly +27 points of "skill above model" available to a developer who
// queues ONLY 1v1: a raw 55% in a one-of-two arena looks strong against a population baseline
// dragged down by four-player Monopoly. Normalised, a 55% in an arena where everyone wins 50%
// is a SMALLER achievement than 40% in an arena where everyone wins 25%.
//
// If this test ever fails, the pooled-win-rate exploit is back.
func TestQueueFilterExploitIsClosed(t *testing.T) {
	population := []Record{
		// Goofspiel: one-of-two, population sits at 50%.
		{Arena: "goofspiel", Wins: 5000, Losses: 5000},
		// Monopoly: one-of-four, population sits at 25%.
		{Arena: "monopoly", Wins: 2500, Losses: 7500},
	}
	base := PopulationBaselines(population)
	if math.Abs(base["goofspiel"]-0.5) > 1e-9 || math.Abs(base["monopoly"]-0.25) > 1e-9 {
		t.Fatalf("baselines wrong: %+v", base)
	}

	cherryPicker := Compute([]Record{{Arena: "goofspiel", Wins: 550, Losses: 450}}, base)
	allRounder := Compute([]Record{{Arena: "monopoly", Wins: 400, Losses: 600}}, base)

	if !(allRounder.LiftLower > cherryPicker.LiftLower) {
		t.Fatalf("40%% in a 25%%-baseline arena (lift %.4f) must beat 55%% in a 50%%-baseline "+
			"arena (lift %.4f) — the queue-filter exploit survives",
			allRounder.LiftLower, cherryPicker.LiftLower)
	}
	// And raw win rate would have said the opposite, which is the point.
	if 0.55 <= 0.40 {
		t.Fatal("premise of the test is wrong")
	}
}

// TestBaselineScoresZero. A model exactly at its arena's base rate has demonstrated nothing,
// and must score 0 rather than "50%, which sounds decent".
func TestBaselineScoresZero(t *testing.T) {
	base := Baselines{"goofspiel": 0.5, "monopoly": 0.25}
	for arena, b := range base {
		w := 5000
		n := int(float64(w) / b)
		s := Compute([]Record{{Arena: arena, Wins: w, Losses: n - w}}, base)
		if math.Abs(s.Lift) > 0.01 {
			t.Errorf("%s at baseline scored lift %.4f, want ~0", arena, s.Lift)
		}
	}
}

// TestRankingUsesTheLowerBoundNotThePointEstimate. Four games at 100% must not outrank four
// hundred at a strong-but-real rate. This is the discipline modelboard and skill/ranking
// already follow and the agent ladder does not.
func TestRankingUsesTheLowerBoundNotThePointEstimate(t *testing.T) {
	base := Baselines{"goofspiel": 0.5}
	lucky := Compute([]Record{{Arena: "goofspiel", Wins: 4, Losses: 0}}, base)
	proven := Compute([]Record{{Arena: "goofspiel", Wins: 280, Losses: 120}}, base)

	if lucky.Lift <= proven.Lift {
		t.Fatal("premise: the lucky row should look better on the POINT estimate")
	}
	if !(proven.LiftLower > lucky.LiftLower) {
		t.Fatalf("4-0 (LB %.4f) outranked 280-120 (LB %.4f) — ranking is not on the lower bound",
			lucky.LiftLower, proven.LiftLower)
	}
	order := Rank(map[string]Score{"lucky": lucky, "proven": proven})
	if order[0].Key != "proven" {
		t.Fatalf("ranked %q first, want proven", order[0].Key)
	}
}

// TestUnknownArenaIsSkippedNotAssumed. Inventing a 0.5 baseline for an unrecognised arena
// would silently restore the assumption this package removes.
func TestUnknownArenaIsSkippedNotAssumed(t *testing.T) {
	base := Baselines{"goofspiel": 0.5}
	s := Compute([]Record{
		{Arena: "goofspiel", Wins: 60, Losses: 40},
		{Arena: "brand-new-arena", Wins: 99, Losses: 1},
	}, base)
	if s.Arenas != 1 {
		t.Fatalf("counted %d arenas, want 1 — the unknown arena was not skipped", s.Arenas)
	}
	if s.Decisive != 100 {
		t.Fatalf("decisive %d, want 100; the unknown arena's games leaked into the denominator", s.Decisive)
	}
}

// TestNoEvidenceIsNotAverage. A zero score from no data must be flagged, not ranked as
// exactly typical — the same reason deception refuses to print a point estimate alone.
func TestNoEvidenceIsNotAverage(t *testing.T) {
	s := Compute(nil, Baselines{"goofspiel": 0.5})
	if s.Comparable {
		t.Fatal("empty input reported as comparable")
	}
	if got := Rank(map[string]Score{"nobody": s}); len(got) != 0 {
		t.Fatalf("an incomparable row was ranked: %+v", got)
	}
}

// TestTiesAreExcludedFromTheDenominator. The arenas disagree about what a tie means;
// averaging that disagreement into the numerator is the pooling mistake in miniature.
func TestTiesAreExcludedFromTheDenominator(t *testing.T) {
	r := Record{Arena: "goofspiel", Wins: 10, Losses: 5}
	if r.Decisive() != 15 {
		t.Fatalf("decisive %d, want 15", r.Decisive())
	}
}

// TestDegenerateBaselineCarriesNoSignal. If everyone wins, the arena cannot discriminate and
// must contribute 0 rather than an infinity.
func TestDegenerateBaselineCarriesNoSignal(t *testing.T) {
	if l := lift(1.0, 1.0); l != 0 {
		t.Fatalf("lift at a saturated baseline = %v, want 0", l)
	}
	s := Compute([]Record{{Arena: "x", Wins: 10, Losses: 0}}, Baselines{"x": 1.0})
	if s.Lift != 0 {
		t.Fatalf("saturated arena produced lift %v", s.Lift)
	}
}

// TestRankIsTotalAndDeterministic. Go map iteration is randomised; a board whose rows shuffle
// between identical requests reads as broken.
func TestRankIsTotalAndDeterministic(t *testing.T) {
	base := Baselines{"g": 0.5}
	in := map[string]Score{}
	for _, k := range []string{"a", "b", "c", "d"} {
		in[k] = Compute([]Record{{Arena: "g", Wins: 60, Losses: 40}}, base)
	}
	first := Rank(in)
	for i := 0; i < 30; i++ {
		got := Rank(in)
		for j := range got {
			if got[j].Key != first[j].Key {
				t.Fatalf("run %d differs at %d: %q vs %q", i, j, got[j].Key, first[j].Key)
			}
		}
	}
}

// TestWeightingIsByGamesNotByArena. A model with 400 Goofspiel games and 4 Monopoly games is
// mostly a Goofspiel result, and must not have its Monopoly sample counted equally.
func TestWeightingIsByGamesNotByArena(t *testing.T) {
	base := Baselines{"goofspiel": 0.5, "monopoly": 0.25}
	s := Compute([]Record{
		{Arena: "goofspiel", Wins: 200, Losses: 200}, // lift 0, 400 games
		{Arena: "monopoly", Wins: 4, Losses: 0},      // lift 1, 4 games
	}, base)
	if s.Lift > 0.05 {
		t.Fatalf("lift %.4f — four Monopoly games outweighed four hundred Goofspiel ones", s.Lift)
	}
}
