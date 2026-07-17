package rating

// TrueSkill rating for N-player (free-for-all) arenas — Mafia and Monopoly — where
// Glicko-2's strictly-pairwise model doesn't fit. Each agent carries a skill mean μ
// and an uncertainty σ; a match is a full ranking by placement (1 = best, equal
// placements = a tie). We update μ/σ using the standard TrueSkill 1v1 message-passing
// equations applied over the ranking's pairwise comparisons, processed in a fixed
// (seat-ascending) order so the result is fully DETERMINISTIC and reproducible —
// which the P-Index audit trail requires.
//
// This is the well-known "TrueSkill through pairwise comparisons" reduction of the
// full factor graph: exact for 2 players, a stable and widely-used approximation for
// N. The displayed rating stored in `elo` is a fixed affine transform of μ (centred
// on 1500 so it reads comparably to the Glicko arenas).
//
// Reference: Herbrich, Minka & Graepel, "TrueSkill(TM): A Bayesian Skill Rating
// System" (NIPS 2006).

import "math"

const (
	tsMu0    = 25.0            // starting skill mean
	tsSigma0 = tsMu0 / 3.0     // starting uncertainty (8.333…)
	tsBeta   = tsSigma0 / 2.0  // performance noise (skill→outcome)
	tsTau    = tsSigma0 / 100. // per-match dynamics: σ² floor so ratings stay adaptive
	tsDrawP  = 0.10            // assumed draw probability (sets the draw margin ε)

	tsDisplayCenter = 1500.0 // μ0 maps here, so a new TrueSkill agent shows ~1500
	tsDisplayScale  = 40.0   // elo = center + (μ − μ0)·scale
	tsSigmaMin      = 1e-4   // numerical floor on σ² so updates stay well-conditioned
)

// tsDisplay converts a TrueSkill mean to the displayed, leaderboard-sortable rating.
// Uses μ (not the conservative μ−3σ) so it reads comparably to Glicko's displayed r;
// the σ uncertainty is surfaced separately (and feeds the P-Index consistency pillar).
func tsDisplay(mu float64) int {
	v := int(math.Round(tsDisplayCenter + (mu-tsMu0)*tsDisplayScale))
	if v < 100 {
		v = 100
	}
	return v
}

// tsStateOrDefault returns the working (μ, σ) for a player, defaulting a fresh agent
// (zero σ) to the TrueSkill priors.
func tsStateOrDefault(s RatingState) (mu, sigma float64) {
	mu, sigma = s.Mu, s.Sigma
	if sigma <= 0 {
		mu, sigma = tsMu0, tsSigma0
	}
	return mu, sigma
}

// TrueSkillApply is the Compute closure for N-player arenas. It returns new
// RatingStates aligned to cur/placements by index, with Mu/Sigma updated and Elo set
// to the displayed transform. RD/Vol are left zero (Glicko-only).
func TrueSkillApply(cur []RatingState, placements []int) []RatingState {
	n := len(cur)
	mu := make([]float64, n)
	s2 := make([]float64, n) // variance (σ²), including the per-match dynamics bump
	for i := range cur {
		m, sig := tsStateOrDefault(cur[i])
		mu[i] = m
		s2[i] = sig*sig + tsTau*tsTau
	}

	eps := drawMargin(tsDrawP, tsBeta, 2) // 1v1 comparisons ⇒ two players

	// Every ordered pair once (i<j), updated sequentially in a fixed order.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			switch {
			case placements[i] < placements[j]:
				pairwiseWin(mu, s2, i, j, eps)
			case placements[i] > placements[j]:
				pairwiseWin(mu, s2, j, i, eps)
			default:
				pairwiseDraw(mu, s2, i, j, eps)
			}
		}
	}

	out := make([]RatingState, n)
	for i := range out {
		sig := math.Sqrt(s2[i])
		out[i] = RatingState{Elo: tsDisplay(mu[i]), Mu: mu[i], Sigma: sig}
	}
	return out
}

// pairwiseWin applies a decisive win of player w over player l to the working μ/σ².
func pairwiseWin(mu, s2 []float64, w, l int, eps float64) {
	c2 := 2*tsBeta*tsBeta + s2[w] + s2[l]
	c := math.Sqrt(c2)
	t := (mu[w] - mu[l]) / c
	e := eps / c
	v := vWin(t, e)
	// W = v·(v + (t − ε′)); the (t − ε′) MUST match the argument passed to vWin, or
	// the variance shrinks too fast (overconfident ratings). See Herbrich et al.
	wt := v * (v + t - e)

	mu[w] += s2[w] / c * v
	mu[l] -= s2[l] / c * v
	s2[w] = clampVar(s2[w] * (1 - s2[w]/c2*wt))
	s2[l] = clampVar(s2[l] * (1 - s2[l]/c2*wt))
}

// pairwiseDraw applies a tie between players a and b to the working μ/σ².
func pairwiseDraw(mu, s2 []float64, a, b int, eps float64) {
	c2 := 2*tsBeta*tsBeta + s2[a] + s2[b]
	c := math.Sqrt(c2)
	t := (mu[a] - mu[b]) / c
	e := eps / c
	v := vDraw(t, e)
	wt := wDraw(t, e)

	mu[a] += s2[a] / c * v
	mu[b] -= s2[b] / c * v
	s2[a] = clampVar(s2[a] * (1 - s2[a]/c2*wt))
	s2[b] = clampVar(s2[b] * (1 - s2[b]/c2*wt))
}

func clampVar(x float64) float64 {
	if x < tsSigmaMin {
		return tsSigmaMin
	}
	return x
}

// ── truncated-Gaussian correction terms (TrueSkill v/w functions) ───────────────

// vWin / wWin: the mean/variance multipliers for a "greater than" outcome.
func vWin(t, eps float64) float64 {
	denom := normCDF(t - eps)
	if denom < 1e-9 {
		// Deep in the tail: return the asymptote to keep the update finite & stable.
		return -(t - eps)
	}
	return normPDF(t-eps) / denom
}

// vDraw / wDraw: the multipliers for a "draw within ±eps" outcome.
func vDraw(t, eps float64) float64 {
	denom := normCDF(eps-t) - normCDF(-eps-t)
	if denom < 1e-9 {
		if t < 0 {
			return -t - eps
		}
		return -t + eps
	}
	return (normPDF(-eps-t) - normPDF(eps-t)) / denom
}

func wDraw(t, eps float64) float64 {
	denom := normCDF(eps-t) - normCDF(-eps-t)
	if denom < 1e-9 {
		return 1.0
	}
	v := vDraw(t, eps)
	return v*v + ((eps-t)*normPDF(eps-t)-(-eps-t)*normPDF(-eps-t))/denom
}

// drawMargin is ε in performance space for the given draw probability, beta, and the
// number of players compared (2 for pairwise): ε = Φ⁻¹((p+1)/2)·√n·β.
func drawMargin(drawP, beta float64, n int) float64 {
	return invNormCDF((drawP+1)/2) * math.Sqrt(float64(n)) * beta
}

// ── standard normal helpers (deterministic, closed-form) ────────────────────────

func normPDF(x float64) float64 { return math.Exp(-x*x/2) / math.Sqrt(2*math.Pi) }

func normCDF(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

// invNormCDF is the inverse standard-normal CDF via Acklam's rational approximation
// (|error| < 1.15e-9), refined with one Halley step. Deterministic.
func invNormCDF(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}
	a := []float64{-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02, 1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00}
	b := []float64{-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02, 6.680131188771972e+01, -1.328068155288572e+01}
	c := []float64{-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00, -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00}
	d := []float64{7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00, 3.754408661907416e+00}
	const plow, phigh = 0.02425, 1 - 0.02425
	var x float64
	switch {
	case p < plow:
		q := math.Sqrt(-2 * math.Log(p))
		x = (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	case p <= phigh:
		q := p - 0.5
		r := q * q
		x = (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q /
			(((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
	default:
		q := math.Sqrt(-2 * math.Log(1-p))
		x = -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	}
	// One Halley refinement step.
	e := normCDF(x) - p
	u := e * math.Sqrt(2*math.Pi) * math.Exp(x*x/2)
	x -= u / (1 + x*u/2)
	return x
}
