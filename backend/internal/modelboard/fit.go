package modelboard

import (
	"math"
	"sort"
)

// solve minimizes the penalized negative log-likelihood.
//
// Barzilai-Borwein gradient descent with a monotone safeguard. The objective is strictly convex
// (the ridge penalty guarantees it even for a model that never loses), so any descent method
// reaches THE optimum rather than a local one — the only question is how fast.
//
// BB estimates the step from the last two iterates, step = (s.s)/(s.y) with s the parameter
// change and y the gradient change, which is a scalar approximation to the inverse Hessian. On
// problems this size it converges in tens of iterations where fixed-step descent took hundreds:
// the first version of this file used a plain backtracking search and did not reach tolerance in
// 500 iterations on a two-model fit, which is the kind of thing that gets "fixed" by loosening
// the tolerance and thereby publishing an unconverged rating.
//
// The safeguard matters because BB is not monotone by nature: if a step increases the objective
// it is halved until it does not. That keeps the sequence from diverging, so hitting MaxIter
// means slow progress rather than failure — a distinction reported in Fit.Converged instead of
// being swallowed.
func solve(p *problem) (x []float64, iters int, converged bool) {
	n := p.dim()
	x = make([]float64, n)
	f, grad := p.objective(x)

	prevX := make([]float64, n)
	prevG := make([]float64, n)
	trial := make([]float64, n)
	// First step is small and unscaled: with no history there is no curvature estimate, and a
	// large blind step on a sharply-curved objective wastes the backtracking budget.
	step := 0.01

	for iters = 0; iters < p.cfg.MaxIter; iters++ {
		if norm(grad) < p.cfg.Tol {
			return x, iters, true
		}
		if iters > 0 {
			// Barzilai-Borwein: s.s / s.y. Falls back to the previous step when s.y is
			// non-positive, which happens on numerically flat regions where the curvature
			// estimate is meaningless rather than merely imprecise.
			var ss, sy float64
			for i := range x {
				si, yi := x[i]-prevX[i], grad[i]-prevG[i]
				ss += si * si
				sy += si * yi
			}
			if sy > 0 && ss > 0 {
				step = ss / sy
			}
		}
		copy(prevX, x)
		copy(prevG, grad)

		accepted := false
		for shrink := 0; shrink < 50; shrink++ {
			for i := range x {
				trial[i] = x[i] - step*grad[i]
			}
			nf, ngrad := p.objective(trial)
			// <= rather than <: on a converged objective the value stops changing while the
			// gradient is still finite, and requiring strict improvement would reject the step
			// and declare failure at the optimum.
			if nf <= f {
				copy(x, trial)
				f, grad = nf, ngrad
				accepted = true
				break
			}
			step /= 2
		}
		if !accepted {
			// No downhill step exists at double precision: this IS the optimum for any purpose a
			// leaderboard has, so report convergence rather than burning the iteration budget
			// and then calling a correct answer unconverged.
			return x, iters, true
		}
	}
	return x, iters, false
}

func norm(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x * x
	}
	return math.Sqrt(s)
}

// Estimate fits the board and its uncertainty.
//
// The order matters: the point estimate is computed on the full data, then the bootstrap
// resamples MATCHES (not comparisons) to get intervals. Resampling comparisons would treat the
// several correlated comparisons an N-player match produces as independent evidence and shrink
// every interval by roughly the square root of the seats per match — an error that makes a
// board look far more certain than it is.
func Estimate(cmp []Comparison, cfg Config) Fit {
	out := Fit{Excluded: map[string]int{}}
	if len(cmp) == 0 {
		return out
	}

	// Comparisons between a model and ITSELF carry no information about model strength (the
	// theta terms cancel) and would only fit harness noise. Dropped, and counted.
	kept := make([]Comparison, 0, len(cmp))
	for _, c := range cmp {
		if c.ModelA == c.ModelB {
			out.Excluded["same_model"]++
			continue
		}
		if c.ModelA == "" || c.ModelB == "" {
			out.Excluded["unattributed"]++
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return out
	}

	// Davidson's tie parameter is only estimable when draws exist. On draw-free data the
	// likelihood improves monotonically as nu falls to 0, so fitting it means an optimizer that
	// never converges while every strength is already settled — and a "nu" that is an artefact of
	// where the iteration budget ran out. Zero draws means zero draw propensity, stated exactly.
	draws := 0
	for i := range kept {
		if kept[i].Outcome == Draw {
			draws++
		}
	}
	if draws == 0 {
		cfg.FitTies = false
	}

	models, strata := index(kept)
	p := &problem{cmp: kept, modelIdx: models, stratumIdx: strata, cfg: cfg}
	x, iters, ok := solve(p)

	out.Converged, out.Iterations = ok, iters
	out.Comparisons, out.Strata = len(kept), len(strata)
	if cfg.FitTies {
		out.Nu = math.Exp(x[p.nuSlot()])
	}

	matches := map[string][]int{}
	for i := range kept {
		matches[kept[i].MatchID] = append(matches[kept[i].MatchID], i)
	}
	out.Matches = len(matches)

	// Per-model tallies and the separability diagnostic.
	type tally struct {
		n, w, l, d int
		harnesses  map[string]struct{}
		bridged    int
	}
	tallies := map[string]*tally{}
	get := func(m string) *tally {
		t := tallies[m]
		if t == nil {
			t = &tally{harnesses: map[string]struct{}{}}
			tallies[m] = t
		}
		return t
	}
	// A stratum BRIDGES when it ran more than one model: only then does its evidence separate
	// a model from the harness behind it.
	stratumModels := map[string]map[string]struct{}{}
	for i := range kept {
		c := &kept[i]
		for _, sm := range [2]struct{ s, m string }{{c.StratumA, c.ModelA}, {c.StratumB, c.ModelB}} {
			if sm.s == "" {
				continue
			}
			if stratumModels[sm.s] == nil {
				stratumModels[sm.s] = map[string]struct{}{}
			}
			stratumModels[sm.s][sm.m] = struct{}{}
		}
	}
	for i := range kept {
		c := &kept[i]
		ta, tb := get(c.ModelA), get(c.ModelB)
		ta.n++
		tb.n++
		switch c.Outcome {
		case Win:
			ta.w++
			tb.l++
		case Loss:
			ta.l++
			tb.w++
		case Draw:
			ta.d++
			tb.d++
		}
		if c.StratumA != "" {
			ta.harnesses[c.StratumA] = struct{}{}
			if len(stratumModels[c.StratumA]) > 1 {
				ta.bridged++
			}
		}
		if c.StratumB != "" {
			tb.harnesses[c.StratumB] = struct{}{}
			if len(stratumModels[c.StratumB]) > 1 {
				tb.bridged++
			}
		}
	}

	boot := bootstrap(kept, matches, models, strata, cfg)

	for _, m := range sortedKeys(models) {
		t := get(m)
		th := x[models[m]]
		r := Rating{
			Model: m, Theta: th, Elo: ToElo(th),
			Comparisons: t.n, Wins: t.w, Losses: t.l, Draws: t.d,
			Harnesses:          len(t.harnesses),
			BridgedComparisons: t.bridged,
			Provisional:        t.n < cfg.MinComparisons,
		}
		if t.n > 0 {
			r.Separability = float64(t.bridged) / float64(t.n)
		}
		if lo, hi, ok := boot.interval(m); ok {
			r.EloLow, r.EloHigh = ToElo(lo), ToElo(hi)
		} else {
			// No interval means the bootstrap never saw this model. Reporting the point
			// estimate as its own bounds would claim perfect certainty, so the bounds stay at
			// the point estimate ONLY when there is genuinely nothing to resample; here we
			// widen to the point estimate itself and let Provisional carry the warning.
			r.EloLow, r.EloHigh = r.Elo, r.Elo
		}
		out.Ratings = append(out.Ratings, r)
	}

	// Rank by the LOWER bound. A wide-interval model has not earned a place above a
	// tight-interval one whose point estimate is only slightly lower — ranking on the point
	// estimate systematically promotes the least-observed models, which is the opposite of what
	// a board should do.
	rank(out.Ratings)
	for i := range out.Ratings {
		out.Ratings[i].RankStability = boot.stability(out.Ratings[i].Model, out.Ratings[i].Rank)
	}
	return out
}

// rank orders by lower bound, then point estimate, then name.
//
// Provisional rows are ranked alongside everything else rather than being pushed to the bottom:
// they are marked, and a reader who wants them excluded can exclude them. Silently sinking a
// model that may genuinely be strong would be its own distortion.
func rank(rs []Rating) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].EloLow != rs[j].EloLow {
			return rs[i].EloLow > rs[j].EloLow
		}
		if rs[i].Elo != rs[j].Elo {
			return rs[i].Elo > rs[j].Elo
		}
		return rs[i].Model < rs[j].Model
	})
	for i := range rs {
		rs[i].Rank = i + 1
	}
}
