package skill

import (
	"math"
	"testing"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
)

// These tests check the solved chain against results the published Markov analyses of
// Monopoly agree on. That is the point of them: the numbers here are computed from this
// engine's own board and decks, so an independent check against known facts about the
// real game is what tells us the model is right rather than merely self-consistent.

// Each deck's movement probabilities must sum to exactly 1, or every iteration of the
// chain leaks or invents probability mass.
//
// This is the test for the bug that actually happened: the first version hand-wrote the
// canonical Chance deck, which carries TWO "nearest railroad" cards where this engine's
// carries one. The effects summed to 15/16, the chain bled 6% of its mass per step, and
// the final renormalisation made the result look like a valid distribution while it was
// really a transient. Deriving the deck from the engine is the fix; this is the guard.
func TestDeckEffectsSumToOne(t *testing.T) {
	for name, e := range map[string]deckEffect{
		"chance": chanceEffects(),
		"chest":  chestEffects(),
	} {
		total := e.stay + e.toJail + e.nearestRail + e.nearestUtil + e.back3
		for _, p := range e.moveTo {
			total += p
		}
		if math.Abs(total-1) > 1e-12 {
			t.Fatalf("%s deck effects sum to %.9f, want exactly 1 — the chain will leak "+
				"probability mass on every step and converge to nothing meaningful", name, total)
		}
	}
}

// The derived profile must match the deck the engine actually ships, card for card.
func TestDeckMovementMatchesTheEngineDeck(t *testing.T) {
	ch := mono.ChanceMovement()
	if ch.Size != 16 {
		t.Errorf("chance deck has %d cards, expected 16", ch.Size)
	}
	moving := ch.ToJail + ch.NearestRail + ch.NearestUtil + ch.Back3
	for _, c := range ch.MoveTo {
		moving += c
	}
	if moving+ch.Stay != ch.Size {
		t.Fatalf("chance deck: %d moving + %d staying != %d cards", moving, ch.Stay, ch.Size)
	}
	if ch.Stay == ch.Size {
		t.Fatal("no chance card moves the player; the chain would be a plain random walk")
	}
}

// A distribution, first of all. Everything downstream multiplies by these.
func TestLandingProbsAreAValidDistribution(t *testing.T) {
	var sum float64
	for i := 0; i < mono.BoardSize; i++ {
		p := LandingProb(i)
		if p < 0 || p > 1 {
			t.Fatalf("square %d has probability %.6f", i, p)
		}
		sum += p
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("probabilities sum to %.9f, want 1", sum)
	}
	// Out-of-range must be 0, not a panic and not a wrapped index.
	if LandingProb(-1) != 0 || LandingProb(mono.BoardSize) != 0 {
		t.Fatal("out-of-range squares returned a non-zero probability")
	}
}

// The single most-cited result in Monopoly probability: Jail is the most-landed square by
// a wide margin, because three separate mechanisms funnel players into it.
func TestJailIsTheMostLandedSquare(t *testing.T) {
	jail := LandingProb(jailSquare)
	for i := 0; i < mono.BoardSize; i++ {
		if i == jailSquare {
			continue
		}
		if LandingProb(i) >= jail {
			t.Fatalf("square %d (%.4f) is landed on at least as often as Jail (%.4f) — the "+
				"go-to-jail funnel is not being modelled", i, LandingProb(i), jail)
		}
	}
	// Published analyses put it near 5.9%. A generous band: the assertion is that jail is
	// dramatically over-represented versus a flat 1/40 = 2.5%, not a decimal match to a
	// paper whose jail-turn model differs from this one.
	if jail < 0.035 || jail > 0.09 {
		t.Fatalf("Jail probability %.4f is outside the plausible 3.5–9%% band", jail)
	}
}

// The uniform assumption is what a naive scorer would use, and it is wrong enough to
// matter. If the chain were flat, every property would price identically and the whole
// exercise would be pointless.
func TestDistributionIsMeaningfullyNonUniform(t *testing.T) {
	const flat = 1.0 / mono.BoardSize
	var maxP, minP float64 = 0, 1
	for i := 0; i < mono.BoardSize; i++ {
		p := LandingProb(i)
		maxP = math.Max(maxP, p)
		minP = math.Min(minP, p)
	}
	if maxP/minP < 2 {
		t.Fatalf("most-landed %.4f vs least-landed %.4f — the board is nearly uniform, so "+
			"the chain is not capturing jail or the card decks", maxP, minP)
	}
	if math.Abs(maxP-flat) < 0.005 {
		t.Fatalf("the busiest square sits at %.4f, essentially the flat %.4f", maxP, flat)
	}
}

// THE result every Monopoly strategy guide is built on: the orange group is the most
// landed colour group, because it sits 6, 8 and 9 squares past Jail — and 6, 8 and 9 are
// among the most common 2d6 totals. A scorer that does not reproduce this is not pricing
// the board the way the game actually plays.
func TestOrangeIsTheBusiestColourGroup(t *testing.T) {
	groups := []string{
		mono.GroupBrown, mono.GroupLightBlue, mono.GroupPink, mono.GroupOrange,
		mono.GroupRed, mono.GroupYellow, mono.GroupGreen, mono.GroupDarkBlue,
	}
	best, bestP := "", 0.0
	for _, g := range groups {
		if p := GroupLandingProb(g); p > bestP {
			best, bestP = g, p
		}
	}
	if best != mono.GroupOrange {
		t.Fatalf("busiest colour group is %q (%.4f), want orange — the classic post-jail "+
			"landing result is not reproduced, so the chain is mispricing the board",
			best, bestP)
	}
	// Brown is the canonical weakest group: two squares, low traffic.
	if GroupLandingProb(mono.GroupOrange) <= GroupLandingProb(mono.GroupBrown) {
		t.Fatal("orange is not out-landing brown")
	}
}

// Squares that teleport you cannot be landed ON in the steady state — the chain resolves
// them through to where the player ends up. Go To Jail is the clean case: arriving there
// always means ending in Jail.
func TestGoToJailIsNeverATerminalSquare(t *testing.T) {
	for _, s := range mono.Board() {
		if s.Kind == mono.KindGoToJail {
			if p := LandingProb(s.Index); p > 1e-9 {
				t.Fatalf("Go To Jail (square %d) holds probability %.6f; players never REMAIN "+
					"there, so any mass left on it is mass missing from Jail", s.Index, p)
			}
		}
	}
}

// Determinism, as everywhere else in this package: the chain is solved by fixed-iteration
// power iteration precisely so a score computed today matches one computed next year.
func TestChainSolveIsDeterministic(t *testing.T) {
	first := solveLandingProbs()
	for i := 0; i < 5; i++ {
		got := solveLandingProbs()
		for sq := 0; sq < mono.BoardSize; sq++ {
			if got[sq] != first[sq] {
				t.Fatalf("run %d differs at square %d: %.15f vs %.15f", i, sq, got[sq], first[sq])
			}
		}
	}
}

// The chain must have CONVERGED, not merely run. If the iteration count were too low the
// result would still be a valid distribution while being quietly wrong, which is the
// failure mode a sum-to-one check cannot catch.
func TestChainHasConverged(t *testing.T) {
	board := mono.Board()
	dice := diceDist()
	cur := solveLandingProbs()

	// One more step from the fixed point must move essentially nothing.
	var next [mono.BoardSize]float64
	for from := 0; from < mono.BoardSize; from++ {
		p := cur[from]
		if p <= 0 {
			continue
		}
		for total := 2; total <= 12; total++ {
			resolveSquare(board, (from+total)%mono.BoardSize, p*dice[total], next[:], 0)
		}
	}
	var drift float64
	for i := range next {
		drift += math.Abs(next[i] - cur[i])
	}
	if drift > 1e-9 {
		t.Fatalf("a further iteration moved the distribution by %.3g in total variation — "+
			"the chain has not converged and every square is priced off a transient", drift)
	}
}
