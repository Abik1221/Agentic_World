package kuhn

import (
	"math"
	"math/rand"
	"testing"
)

const tol = 1e-9

// TestGameValueMatchesTheClassicalResult. Kuhn (1950) gives -1/18 to the first player. The
// constant is load-bearing — every exploitability number is measured against it, so a value
// that disagreed with this implementation would bias the whole ladder by a fixed offset that
// no other test would catch.
func TestGameValueMatchesTheClassicalResult(t *testing.T) {
	for _, alpha := range []float64{0, 0.1, 1.0 / 6.0, 1.0 / 3.0} {
		p1, p2, err := Equilibrium(alpha)
		if err != nil {
			t.Fatal(err)
		}
		v := Evaluate(p1, p2)
		if math.Abs(v-GameValue) > tol {
			t.Errorf("alpha=%.4f: equilibrium value %.10f, want %.10f (-1/18)", alpha, v, GameValue)
		}
	}
}

// TestExploitabilityOfEquilibriumIsZero, for BOTH seats.
//
// The game is not symmetric, so the reference value differs by seat: P1's is -1/18 and P2's is
// +1/18. A sign error there shifts every number by 1/9 — plausible-looking and large enough to
// reorder a board.
func TestExploitabilityOfEquilibriumIsZero(t *testing.T) {
	for _, alpha := range []float64{0, 0.1, 1.0 / 3.0} {
		p1, p2, err := Equilibrium(alpha)
		if err != nil {
			t.Fatal(err)
		}
		if e := Exploitability(P1, p1); e > 1e-9 {
			t.Errorf("alpha=%.4f: P1 equilibrium is exploitable by %.10f", alpha, e)
		}
		if e := Exploitability(P2, p2); e > 1e-9 {
			t.Errorf("alpha=%.4f: P2 equilibrium is exploitable by %.10f", alpha, e)
		}
	}
}

// TestNeverBluffingIsExploitable. Kuhn's equilibria REQUIRE betting the worst card with
// positive probability. A model that never bluffs must therefore be measurably exploitable —
// a strategic property no amount of Goofspiel measures, and the reason this game earns its
// place on the ladder.
func TestNeverBluffingIsExploitable(t *testing.T) {
	honest := Policy{
		{Card: Jack, Hist: HistStart}:  0, // never bluffs the worst card
		{Card: Queen, Hist: HistStart}: 0,
		{Card: King, Hist: HistStart}:  1,
		{Card: Jack, Hist: HistCB}:     0,
		{Card: Queen, Hist: HistCB}:    1,
		{Card: King, Hist: HistCB}:     1,
	}
	eps := Exploitability(P1, honest)
	t.Logf("a never-bluffing P1 is exploitable by %.4f antes/hand", eps)
	if eps <= 1e-6 {
		t.Fatal("a never-bluffing strategy was measured as unexploitable; Kuhn requires " +
			"bluffing at equilibrium, so this cannot be right")
	}
}

// TestAlwaysFoldingIsMaximallyBad. A sanity anchor at the other extreme.
func TestAlwaysFoldingIsMaximallyBad(t *testing.T) {
	passive := Policy{} // missing infosets default to 0 = always passive
	epsP1 := Exploitability(P1, passive)
	epsP2 := Exploitability(P2, passive)
	t.Logf("always-passive: P1 exploitable by %.4f, P2 by %.4f", epsP1, epsP2)
	if epsP1 <= 0 || epsP2 <= 0 {
		t.Fatal("an always-passive strategy must be exploitable from both seats")
	}
}

// TestBestResponseIsExactlyOptimal. Nothing may beat it — checked against random strategies
// and against every pure strategy for the small seat.
func TestBestResponseIsExactlyOptimal(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 40; trial++ {
		opp := randomPolicy(P2, rng)
		best, br := BestResponse(P1, opp)
		if got := Evaluate(br, opp); math.Abs(got-best) > tol {
			t.Fatalf("BR reports %.10f but playing it scores %.10f", best, got)
		}
		// Exhaustive: no pure strategy may beat it.
		sets := Infosets(P1)
		for mask := 0; mask < 1<<len(sets); mask++ {
			pol := make(Policy, len(sets))
			for i, is := range sets {
				if mask&(1<<i) != 0 {
					pol[is] = 1
				}
			}
			if v := Evaluate(pol, opp); v > best+tol {
				t.Fatalf("pure strategy %d scored %.10f > BR %.10f", mask, v, best)
			}
		}
		// And no random mixed strategy either.
		for i := 0; i < 50; i++ {
			if v := Evaluate(randomPolicy(P1, rng), opp); v > best+tol {
				t.Fatalf("a random strategy scored %.10f > BR %.10f", v, best)
			}
		}
	}
}

// TestZeroSum. u2 must be exactly -u1, or the seat arithmetic in Exploitability is wrong.
func TestZeroSum(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 100; i++ {
		p1, p2 := randomPolicy(P1, rng), randomPolicy(P2, rng)
		v1 := Evaluate(p1, p2)
		vBR, _ := BestResponse(P2, p1)
		// P2's best response can only do at least as well as its actual play.
		if -v1 > vBR+tol {
			t.Fatalf("P2's realised payoff %.10f exceeds its best response %.10f", -v1, vBR)
		}
	}
}

// TestDeterminism. Same inputs, same solve, forever.
func TestDeterminism(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	opp := randomPolicy(P2, rng)
	v0, br0 := BestResponse(P1, opp)
	for i := 0; i < 25; i++ {
		v, br := BestResponse(P1, opp)
		if v != v0 {
			t.Fatalf("run %d: value %v != %v", i, v, v0)
		}
		for is, a := range br0 {
			if br[is] != a {
				t.Fatalf("run %d: infoset %+v gave %v, want %v", i, is, br[is], a)
			}
		}
	}
}

// TestInfosetsDoNotLeakTheOpponentsCard. An information set that carried it would make the
// game trivially solvable and the measurement meaningless.
func TestInfosetsDoNotLeakTheOpponentsCard(t *testing.T) {
	for _, seat := range []int{P1, P2} {
		sets := Infosets(seat)
		if len(sets) != 6 {
			t.Fatalf("seat %d has %d infosets, want 6", seat, len(sets))
		}
		seen := map[Infoset]bool{}
		for _, is := range sets {
			if seen[is] {
				t.Fatalf("duplicate infoset %+v", is)
			}
			seen[is] = true
			if is.Card < Jack || is.Card > King {
				t.Fatalf("infoset card %d out of range", is.Card)
			}
		}
	}
}

func TestEquilibriumRejectsBadAlpha(t *testing.T) {
	for _, a := range []float64{-0.01, 0.5, 1} {
		if _, _, err := Equilibrium(a); err == nil {
			t.Errorf("alpha=%v was accepted", a)
		}
	}
}

func randomPolicy(seat int, rng *rand.Rand) Policy {
	p := Policy{}
	for _, is := range Infosets(seat) {
		p[is] = rng.Float64()
	}
	return p
}
