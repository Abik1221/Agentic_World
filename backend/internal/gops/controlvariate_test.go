package gops_test

import (
	"math"
	"math/rand"
	"testing"

	"github.com/agent-arena/arena/internal/gops"
)

// playMatch runs one match: our seat samples from `mine`, the opponent from `opp`.
// Returns the realised point differential and the steps, so a caller can score both.
func playMatch(t *testing.T, cfg gops.Config, mine, opp gops.Policy, rng *rand.Rand) (float64, []gops.Step) {
	t.Helper()
	n := gops.Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}
	diff := 0.0
	var steps []gops.Step
	for n.Me != 0 && n.Opp != 0 {
		pot := cfg.Pot(n)
		a := sample(mine.Step(cfg, n), rng)
		b := sample(opp.Step(cfg, gops.Node{Me: n.Opp, Opp: n.Me, Carry: n.Carry}), rng)
		if a == 0 || b == 0 {
			t.Fatalf("policy produced no action at %v", n)
		}
		steps = append(steps, gops.Step{Node: n, MyCard: a, OppCard: b})
		switch {
		case a > b:
			diff += float64(pot)
		case a < b:
			diff -= float64(pot)
		}
		carry := 0
		if a == b {
			carry = pot
		}
		n = gops.Node{Me: n.Me &^ (1 << (a - 1)), Opp: n.Opp &^ (1 << (b - 1)), Carry: carry}
	}
	return diff, steps
}

func sample(mix []float64, rng *rand.Rand) int {
	u, acc := rng.Float64(), 0.0
	for i, p := range mix {
		acc += p
		if p > 0 && u <= acc {
			return i + 1
		}
	}
	for i := len(mix) - 1; i >= 0; i-- {
		if mix[i] > 0 {
			return i + 1
		}
	}
	return 0
}

// TestControlVariateIsUnbiased is the property that makes this safe to publish from.
//
// A variance reduction that shifts the mean is not a reduction, it is a fabrication. Over many
// matches the corrected average must agree with the raw average to within sampling error.
func TestControlVariateIsUnbiased(t *testing.T) {
	cfg := cfg5(t)
	opp := gops.Uniform()
	mine := gops.Uniform()
	rng := rand.New(rand.NewSource(4242))

	const trials = 6000
	var rawSum, corSum float64
	raws := make([]float64, 0, trials)
	cors := make([]float64, 0, trials)
	memo := map[gops.Node]float64{}

	for i := 0; i < trials; i++ {
		diff, steps := playMatch(t, cfg, mine, opp, rng)
		cv, counted, err := gops.ControlVariate(cfg, opp, steps, memo)
		if err != nil {
			t.Fatal(err)
		}
		if counted != len(steps) {
			t.Fatalf("covered %d of %d steps", counted, len(steps))
		}
		cor := gops.Corrected(diff, cv)
		rawSum += diff
		corSum += cor
		raws = append(raws, diff)
		cors = append(cors, cor)
	}
	rawMean, corMean := rawSum/trials, corSum/trials
	// Standard error of the raw mean sets the tolerance; the corrected estimator is tighter, so
	// agreeing within 4 raw standard errors is a strict check, not a loose one.
	se := math.Sqrt(variance(raws) / trials)
	if math.Abs(rawMean-corMean) > 4*se {
		t.Fatalf("corrected mean %.4f differs from raw mean %.4f by more than 4 SE (%.4f) — "+
			"the control variate is biasing the estimate it was meant to tighten",
			corMean, rawMean, se)
	}
	t.Logf("raw mean %.4f, corrected mean %.4f (4 SE = %.4f)", rawMean, corMean, 4*se)
}

// TestControlVariateCutsVariance. Unbiased and useless is a real possibility, so the size of the
// reduction is measured rather than assumed.
func TestControlVariateCutsVariance(t *testing.T) {
	cfg := cfg5(t)
	opp := gops.Uniform()
	mine := gops.Uniform()
	rng := rand.New(rand.NewSource(99))

	const trials = 6000
	raws := make([]float64, 0, trials)
	cors := make([]float64, 0, trials)
	memo := map[gops.Node]float64{}
	for i := 0; i < trials; i++ {
		diff, steps := playMatch(t, cfg, mine, opp, rng)
		cv, _, err := gops.ControlVariate(cfg, opp, steps, memo)
		if err != nil {
			t.Fatal(err)
		}
		raws = append(raws, diff)
		cors = append(cors, gops.Corrected(diff, cv))
	}
	vr, vc := variance(raws), variance(cors)
	if vc <= 0 {
		t.Fatal("corrected variance is zero; the simulation is degenerate")
	}
	ratio := vr / vc
	t.Logf("variance: raw %.4f, corrected %.4f — %.2fx reduction", vr, vc, ratio)
	if ratio < 1.5 {
		t.Fatalf("control variate cut variance only %.2fx against a randomising opponent; it is "+
			"not removing the opponent's dice", ratio)
	}
}

// TestDeterministicOpponentGivesZeroCorrection. With no opponent dice there is nothing to remove,
// and a correction that fired anyway would be adding noise while claiming to remove it.
func TestDeterministicOpponentGivesZeroCorrection(t *testing.T) {
	cfg := cfg5(t)
	// A pure policy: always the lowest card in hand.
	pure := gops.Policy{}
	var fill func(n gops.Node, depth int)
	seen := map[gops.Node]bool{}
	fill = func(n gops.Node, depth int) {
		if n.Me == 0 || n.Opp == 0 || depth > cfg.N || seen[n] {
			return
		}
		seen[n] = true
		low := 0
		for c := 1; c <= cfg.N; c++ {
			if n.Me&(1<<(c-1)) != 0 {
				low = c
				break
			}
		}
		w := make([]float64, cfg.N)
		w[low-1] = 1
		pure[n] = w
		for b := 1; b <= cfg.N; b++ {
			if n.Opp&(1<<(b-1)) == 0 {
				continue
			}
			carry := 0
			if low == b {
				carry = cfg.Pot(n)
			}
			fill(gops.Node{Me: n.Me &^ (1 << (low - 1)), Opp: n.Opp &^ (1 << (b - 1)), Carry: carry}, depth+1)
		}
	}
	// Fill from the OPPONENT's perspective too, since Step is queried on mirrored nodes.
	fill(gops.Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}, 0)

	rng := rand.New(rand.NewSource(5))
	memo := map[gops.Node]float64{}
	_, steps := playMatch(t, cfg, gops.Uniform(), pure, rng)
	cv, counted, err := gops.ControlVariate(cfg, pure, steps, memo)
	if err != nil {
		t.Fatal(err)
	}
	if counted == 0 {
		t.Fatal("no steps were covered")
	}
	if math.Abs(cv) > 1e-9 {
		t.Fatalf("deterministic opponent produced a non-zero correction %.9f — the estimator is "+
			"injecting noise where there was none to remove", cv)
	}
}

// TestRejectsInconsistentSteps. A step claiming a card that was not in hand means the caller's
// reconstruction is wrong, and silently scoring it would produce a confident number about a game
// that was never played.
func TestRejectsInconsistentSteps(t *testing.T) {
	cfg := cfg5(t)
	root := gops.Node{Me: cfg.FullHand() &^ 1, Opp: cfg.FullHand()} // card 1 spent
	if _, _, err := gops.ControlVariate(cfg, gops.Uniform(),
		[]gops.Step{{Node: root, MyCard: 1, OppCard: 2}}, nil); err == nil {
		t.Fatal("a step playing a spent card was accepted")
	}
	if _, _, err := gops.ControlVariate(cfg, gops.Uniform(),
		[]gops.Step{{Node: gops.Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}, MyCard: 1, OppCard: 99}},
		nil); err == nil {
		t.Fatal("a step with an out-of-range opponent card was accepted")
	}
}

func variance(xs []float64) float64 {
	m := 0.0
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	ss := 0.0
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	return ss / float64(len(xs)-1)
}
