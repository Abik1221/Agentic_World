package modelboard

import (
	"math"
	"testing"
)

// The analytic gradient checked against central finite differences.
//
// This is the first test that should exist for any hand-derived likelihood, and writing it
// second was a mistake: a wrong gradient on a strictly convex objective shows up as an optimizer
// that will not converge, which invites "fix" after "fix" to the optimizer while the actual
// error sits in the derivative. The Davidson tie term in particular has a d/dnu that is easy to
// get wrong in a way no output inspection would reveal.
func TestAnalyticGradientMatchesFiniteDifferences(t *testing.T) {
	cmp := []Comparison{
		{MatchID: "m1", ModelA: "A", ModelB: "B", StratumA: "s1", StratumB: "s2", Outcome: Win, Weight: 1},
		{MatchID: "m2", ModelA: "A", ModelB: "B", StratumA: "s1", StratumB: "s2", Outcome: Loss, Weight: 1},
		{MatchID: "m3", ModelA: "A", ModelB: "B", StratumA: "s1", StratumB: "s2", Outcome: Draw, Weight: 1},
		{MatchID: "m4", ModelA: "B", ModelB: "C", StratumA: "s2", StratumB: "s1", Outcome: Win, Weight: 0.5},
		{MatchID: "m5", ModelA: "C", ModelB: "A", StratumA: "s3", StratumB: "s1", Outcome: Draw, Weight: 2},
	}
	models, strata := index(cmp)
	cfg := DefaultConfig()
	p := &problem{cmp: cmp, modelIdx: models, stratumIdx: strata, cfg: cfg}

	// Several points, including asymmetric ones and a non-zero tie parameter, because a gradient
	// can be right at the origin by symmetry and wrong everywhere else.
	points := [][]float64{
		make([]float64, p.dim()),
		{0.4, -0.2, 0.9, 0.1, -0.5, 0.3, 0.2},
		{-1.1, 0.7, 0.05, -0.3, 0.8, -0.6, -0.9},
	}
	for pi, x := range points {
		if len(x) != p.dim() {
			t.Fatalf("point %d has %d params, problem has %d", pi, len(x), p.dim())
		}
		_, grad := p.objective(x)
		const h = 1e-6
		for i := range x {
			up := append([]float64(nil), x...)
			dn := append([]float64(nil), x...)
			up[i] += h
			dn[i] -= h
			fUp, _ := p.objective(up)
			fDn, _ := p.objective(dn)
			numeric := (fUp - fDn) / (2 * h)
			// Relative tolerance, since gradient magnitudes vary by orders of magnitude across
			// the parameter vector.
			scale := math.Max(1, math.Abs(numeric))
			if math.Abs(grad[i]-numeric)/scale > 1e-5 {
				var name string
				switch {
				case i == p.nuSlot():
					name = "nu (Davidson tie term)"
				case i < len(models):
					name = "model theta"
				default:
					name = "stratum alpha"
				}
				t.Errorf("point %d, %s slot %d: analytic %.10f vs numeric %.10f",
					pi, name, i, grad[i], numeric)
			}
		}
	}
}
