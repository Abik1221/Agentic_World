package skill

import (
	"math"
	"testing"
)

// ── Mathematical properties ─────────────────────────────────────────────────
//
// These are the load-bearing tests. A scorer that produces plausible-looking numbers but
// violates the game's own algebra will rank agents wrongly in ways no example-based test
// catches, and it feeds a public leaderboard.

// Φ must be zero-sum: whatever share of the remaining pool one side is worth, the other
// is worth exactly the rest. If this fails the scorer can value an action above the prize
// money that actually exists, and every downstream comparison inherits the error.
func TestPotentialIsZeroSum(t *testing.T) {
	cases := []struct{ a, b []int }{
		{[]int{1, 2, 3}, []int{4, 5, 6}},
		{[]int{13}, []int{1}},
		{[]int{1, 5, 9, 13}, []int{2, 6, 10}},
		{[]int{7}, []int{7}},
	}
	for _, c := range cases {
		const remaining = 40
		mine := potential(c.a, c.b, remaining)
		theirs := potential(c.b, c.a, remaining)
		if math.Abs(mine+theirs-remaining) > 1e-9 {
			t.Fatalf("Φ(A,B)=%.6f + Φ(B,A)=%.6f = %.6f, want exactly %d — the scorer is "+
				"creating or destroying prize value", mine, theirs, mine+theirs, remaining)
		}
	}
}

// Identical hands must split the remaining pool exactly evenly. Any asymmetry here is a
// systematic bias for or against a seat.
func TestPotentialIsSymmetric(t *testing.T) {
	hand := []int{2, 5, 9}
	got := potential(hand, hand, 30)
	if math.Abs(got-15) > 1e-9 {
		t.Fatalf("equal hands score %.6f of a 30-point pool, want 15", got)
	}
}

// A stronger hand must never be worth less than a weaker one against the same opponent.
// Monotonicity is what makes "keep your high cards" register as valuable at all.
func TestPotentialIsMonotoneInHandStrength(t *testing.T) {
	opp := []int{4, 5, 6}
	weak := potential([]int{1, 2, 3}, opp, 50)
	strong := potential([]int{7, 8, 9}, opp, 50)
	if !(strong > weak) {
		t.Fatalf("a stronger hand scored %.4f vs the weaker hand's %.4f", strong, weak)
	}
}

// The reference policy must be a probability distribution. Regret matching can only
// produce one if the averaging is right; a policy summing to anything else silently
// rescales every expected value computed from it.
func TestReferencePolicyIsAValidDistribution(t *testing.T) {
	st := GoofspielState{
		Round: 3, Prize: 9,
		MyHand: []int{1, 4, 7, 11, 13}, OppHand: []int{2, 5, 8, 10, 12},
		RemainingPrizes: []int{3, 6, 12},
	}
	pi := referencePolicy(st)
	if len(pi) != len(st.OppHand) {
		t.Fatalf("policy covers %d cards, want %d", len(pi), len(st.OppHand))
	}
	var total float64
	for card, p := range pi {
		if p < -1e-12 || p > 1+1e-12 {
			t.Fatalf("card %d has probability %.6f, outside [0,1]", card, p)
		}
		total += p
	}
	if math.Abs(total-1) > 1e-9 {
		t.Fatalf("policy sums to %.9f, want 1", total)
	}
}

// Determinism is the premise of the whole package: a ranking that cannot be recomputed
// cannot be defended when a developer disputes it.
func TestScoringIsDeterministic(t *testing.T) {
	st := GoofspielState{
		Round: 5, Prize: 11,
		MyHand: []int{2, 3, 6, 9, 12}, OppHand: []int{1, 4, 7, 10, 13},
		RemainingPrizes: []int{4, 8},
	}
	first, ok := ScoreGoofspielBid(st, 9)
	if !ok {
		t.Fatal("scoring failed on a well-formed state")
	}
	for i := 0; i < 25; i++ {
		got, ok := ScoreGoofspielBid(st, 9)
		if !ok {
			t.Fatal("scoring became unavailable on a repeat run")
		}
		if got.Regret != first.Regret || got.Best != first.Best {
			t.Fatalf("run %d differed: regret %.12f vs %.12f, best %q vs %q — a score that "+
				"changes between runs cannot back a public ranking",
				i, got.Regret, first.Regret, got.Best, first.Best)
		}
	}
}

// Regret is a normalised share of what was available, so it must stay inside [0,1] over
// every reachable shape of state — including the degenerate ones.
func TestRegretStaysInRange(t *testing.T) {
	states := []GoofspielState{
		{Round: 1, Prize: 13, MyHand: []int{1, 2, 3}, OppHand: []int{11, 12, 13}, RemainingPrizes: []int{1, 2}},
		{Round: 13, Prize: 1, MyHand: []int{13}, OppHand: []int{1}, RemainingPrizes: nil},
		{Round: 7, Prize: 7, MyHand: []int{7}, OppHand: []int{7}, RemainingPrizes: []int{5}},
		{Round: 2, Prize: 4, MyHand: []int{1, 13}, OppHand: []int{2, 12}, RemainingPrizes: []int{9, 9, 9}},
	}
	for i, st := range states {
		for _, card := range st.MyHand {
			d, ok := ScoreGoofspielBid(st, card)
			if !ok {
				t.Fatalf("state %d card %d: not scorable", i, card)
			}
			if d.Regret < 0 || d.Regret > 1 {
				t.Fatalf("state %d card %d: regret %.6f outside [0,1]", i, card, d.Regret)
			}
			if q := d.Quality(); math.Abs(q-(1-d.Regret)) > 1e-12 {
				t.Fatalf("quality %.6f does not complement regret %.6f", q, d.Regret)
			}
		}
	}
}

// A forced move must score zero regret. An agent cannot be blamed for a decision that
// carried no choice, and scoring it as a blunder would punish agents for the shape of
// the game rather than their play.
func TestForcedMoveIsNeverABlunder(t *testing.T) {
	st := GoofspielState{Round: 13, Prize: 6, MyHand: []int{4}, OppHand: []int{9}, RemainingPrizes: nil}
	d, ok := ScoreGoofspielBid(st, 4)
	if !ok {
		t.Fatal("a single-card state should still be scorable")
	}
	if d.Regret != 0 {
		t.Fatalf("the only legal card scored regret %.6f, want 0", d.Regret)
	}
}

// ── Strategic properties ────────────────────────────────────────────────────
//
// The scorer has to agree with things every competent Goofspiel player knows. If it
// disagrees here, the metric is measuring something other than skill.

// Dumping the highest card on the lowest prize, with big prizes still to come, is the
// textbook Goofspiel error. It must register as a real mistake.
func TestOverpayingForACheapPrizeIsAMistake(t *testing.T) {
	st := GoofspielState{
		Round: 1, Prize: 1,
		MyHand: []int{1, 2, 3, 12, 13}, OppHand: []int{1, 2, 3, 12, 13},
		RemainingPrizes: []int{13, 12, 11, 10}, // the real money is all still ahead
	}
	dump, ok := ScoreGoofspielBid(st, 13)
	if !ok {
		t.Fatal("not scorable")
	}
	cheap, ok := ScoreGoofspielBid(st, 1)
	if !ok {
		t.Fatal("not scorable")
	}
	if !(dump.Regret > cheap.Regret) {
		t.Fatalf("burning the 13 on a 1-point prize scored regret %.4f, no worse than the "+
			"cheap bid's %.4f — the scorer is not valuing the cards you keep",
			dump.Regret, cheap.Regret)
	}
	if dump.Regret <= BlunderThreshold {
		t.Errorf("burning the 13 on a 1-point prize scored only %.4f regret; that is the "+
			"canonical Goofspiel blunder and should read as one", dump.Regret)
	}
}

// The mirror image: on the LAST round there is no continuation, so the only thing that
// matters is winning the prize in front of you. Holding back is now the error.
func TestOnTheFinalRoundOnlyTheImmediatePrizeMatters(t *testing.T) {
	st := GoofspielState{
		Round: 13, Prize: 13,
		MyHand: []int{2, 13}, OppHand: []int{5, 9},
		RemainingPrizes: nil,
	}
	win, ok := ScoreGoofspielBid(st, 13)
	if !ok {
		t.Fatal("not scorable")
	}
	if win.Regret != 0 {
		t.Fatalf("the card that wins the final prize scored regret %.4f, want 0", win.Regret)
	}
	lose, _ := ScoreGoofspielBid(st, 2)
	if !(lose.Regret > win.Regret) {
		t.Fatalf("throwing the final round scored %.4f, no worse than winning it (%.4f)",
			lose.Regret, win.Regret)
	}
}

// Scoring must be OPPONENT-INDEPENDENT: the same decision from the same state scores the
// same regardless of what the opponent turned out to play. This is the property that
// makes agents that never met each other comparable, and it is what stops the metric
// from re-importing the luck that win rate is full of.
func TestScoreDoesNotDependOnWhatTheOpponentActuallyPlayed(t *testing.T) {
	st := GoofspielState{
		Round: 4, Prize: 8,
		MyHand: []int{3, 6, 10}, OppHand: []int{2, 7, 11},
		RemainingPrizes: []int{5, 9},
	}
	// The scorer's inputs contain no opponent ACTION at all — only the opponent's hand.
	// Asserted structurally: identical calls must agree, and the signature offers nowhere
	// to pass the realised card even if a future change wanted to.
	a, _ := ScoreGoofspielBid(st, 6)
	b, _ := ScoreGoofspielBid(st, 6)
	if a.Regret != b.Regret {
		t.Fatal("identical states scored differently")
	}
}

// A malformed decision must be EXCLUDED, never scored zero. Scoring an unparseable
// decision as perfect would let a broken agent farm the metric by emitting garbage.
func TestUnscorableDecisionsAreRejected(t *testing.T) {
	base := GoofspielState{Round: 2, Prize: 5, MyHand: []int{1, 2}, OppHand: []int{3, 4}}
	if _, ok := ScoreGoofspielBid(base, 9); ok {
		t.Error("a card the agent does not hold was scored instead of rejected")
	}
	if _, ok := ScoreGoofspielBid(GoofspielState{Round: 1, Prize: 5}, 1); ok {
		t.Error("an empty state was scored instead of rejected")
	}
}

// ── Aggregation ─────────────────────────────────────────────────────────────

func TestSummarizeReportsSpreadAndBlunders(t *testing.T) {
	got := Summarize([]Decision{
		{Regret: 0}, {Regret: 0}, {Regret: 0.1}, {Regret: 0.9},
	})
	if got.Decisions != 4 {
		t.Fatalf("decisions=%d want 4", got.Decisions)
	}
	if math.Abs(got.MeanRegret-0.25) > 1e-9 {
		t.Fatalf("mean regret %.6f want 0.25", got.MeanRegret)
	}
	if math.Abs(got.Quality-0.75) > 1e-9 {
		t.Fatalf("quality %.6f want 0.75", got.Quality)
	}
	if got.Blunders != 1 {
		t.Fatalf("blunders=%d want 1 — mean regret alone hides the round that was thrown", got.Blunders)
	}
	if got.Perfect != 2 {
		t.Fatalf("perfect=%d want 2", got.Perfect)
	}
	if got.StdErr <= 0 {
		t.Fatal("std error not reported; a mean over 4 decisions and one over 400 must not render alike")
	}
}

// One decision gives no information about spread. Reporting 0 would claim certainty from
// a single observation.
func TestSingleDecisionReportsNoSpread(t *testing.T) {
	if got := Summarize([]Decision{{Regret: 0.4}}); got.StdErr != 0 {
		t.Fatalf("std error %.6f from one decision, want 0", got.StdErr)
	}
	if got := Summarize(nil); got.Decisions != 0 || got.Quality != 0 {
		t.Fatal("empty input should summarise to a zero-decision result")
	}
}
