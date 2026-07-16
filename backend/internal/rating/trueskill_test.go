package rating

import (
	"math"
	"testing"
)

// The win-variance multiplier W must use (t − ε′) — the SAME argument passed to
// vWin — not t. Recompute the winner's post-match sigma with the correct term and
// assert the engine matches; this fails if W reverts to v·(v+t) (which shrinks
// uncertainty too fast and makes ratings overconfident).
func TestTrueSkillWinVarianceUsesDrawMargin(t *testing.T) {
	out := TrueSkillApply([]RatingState{{}, {}}, []int{1, 2}) // two default players; winner=seat0

	s2 := tsSigma0*tsSigma0 + tsTau*tsTau
	c2 := 2*tsBeta*tsBeta + 2*s2
	c := math.Sqrt(c2)
	e := drawMargin(tsDrawP, tsBeta, 2) / c
	v := vWin(0, e) // equal means ⇒ t = 0
	wCorrect := v * (v + 0 - e)
	wantSigma := math.Sqrt(s2 * (1 - s2/c2*wCorrect))

	if math.Abs(out[0].Sigma-wantSigma) > 1e-9 {
		t.Fatalf("winner sigma = %.9f, want %.9f (W must use t−ε/c, not t)", out[0].Sigma, wantSigma)
	}
}

// A decisive win must raise the winner's displayed rating and lower the loser's,
// and both players' uncertainty (sigma) must shrink after evidence.
func TestTrueSkillWinnerGainsLoserLoses(t *testing.T) {
	cur := []RatingState{{}, {}} // two fresh players (defaults applied inside)
	out := TrueSkillApply(cur, []int{1, 2})

	if out[0].Elo <= out[1].Elo {
		t.Fatalf("winner elo %d should exceed loser elo %d", out[0].Elo, out[1].Elo)
	}
	if out[0].Mu <= tsMu0 {
		t.Fatalf("winner mu %.3f should rise above prior %.3f", out[0].Mu, tsMu0)
	}
	if out[1].Mu >= tsMu0 {
		t.Fatalf("loser mu %.3f should fall below prior %.3f", out[1].Mu, tsMu0)
	}
	if out[0].Sigma >= tsSigma0 || out[1].Sigma >= tsSigma0 {
		t.Fatalf("sigma should shrink from %.3f: got %.3f / %.3f", tsSigma0, out[0].Sigma, out[1].Sigma)
	}
}

// A 4-player free-for-all must rank the output monotonically by placement, and be
// fully deterministic (identical inputs → byte-identical outputs), which the P-Index
// audit trail depends on.
func TestTrueSkillMultiplayerRankedAndDeterministic(t *testing.T) {
	cur := make([]RatingState, 4)
	placements := []int{1, 2, 3, 4}

	a := TrueSkillApply(cur, placements)
	b := TrueSkillApply(cur, placements)

	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at %d: %+v vs %+v", i, a[i], b[i])
		}
	}
	for i := 0; i+1 < len(a); i++ {
		if a[i].Mu <= a[i+1].Mu {
			t.Fatalf("placement %d (mu %.3f) should outrank placement %d (mu %.3f)",
				i+1, a[i].Mu, i+2, a[i+1].Mu)
		}
	}
}

// A tie between two fresh, equal players must leave the mean essentially unchanged
// (symmetry) while still reducing uncertainty.
func TestTrueSkillDrawIsSymmetric(t *testing.T) {
	cur := []RatingState{{}, {}}
	out := TrueSkillApply(cur, []int{1, 1})

	if d := out[0].Mu - out[1].Mu; d > 1e-6 || d < -1e-6 {
		t.Fatalf("draw between equals should keep means equal, delta=%.9f", d)
	}
	if out[0].Sigma >= tsSigma0 {
		t.Fatalf("a draw is still evidence: sigma should shrink from %.3f, got %.3f", tsSigma0, out[0].Sigma)
	}
}
