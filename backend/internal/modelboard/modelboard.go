// Package modelboard ranks MODELS by game-theoretic play, controlling for the harness.
//
// # The question, and why the obvious answer is wrong
//
// "Is Claude better than GPT at strategic play?" cannot be answered by comparing win rates on
// this platform, because every Pyyol match confounds two things: the model a developer chose
// and the agent they built around it. A strong engineer on a weak model beats a weak engineer
// on a strong one, and a raw win-rate table cannot tell you which of those you are looking at.
// Publishing that table anyway would be the single most misleading thing this platform could
// do, because it would look exactly like an answer.
//
// # The estimator
//
// A Bradley-Terry model (Bradley & Terry 1952) fit as a regularized logistic regression over
// pairwise comparisons, which is the established approach for arena-style LLM leaderboards
// (Chiang et al. 2024, "Chatbot Arena", arXiv:2403.04132). Three departures from the textbook
// form, each forced by something real about this data:
//
//  1. TIES ARE MODELLED, not dropped. Goofspiel draws, and discarding draws throws away the
//     most informative comparisons — two closely matched models draw most often. Davidson's
//     (1970) extension adds one tie parameter nu:
//
//     P(i beats j) = e^(d/2) / (e^(d/2) + e^(-d/2) + nu),  d = theta_i - theta_j
//     P(tie)       = nu      / (e^(d/2) + e^(-d/2) + nu)
//
//     Chosen over Rao & Kupper (1967), whose threshold formulation makes ties a region of
//     indifference; Davidson's treats a draw as its own outcome, which is what a drawn
//     Goofspiel match actually is. nu = 0 recovers plain Bradley-Terry.
//
//  2. THE HARNESS IS A FIXED EFFECT. Each (developer, scaffold) pair gets its own ability
//     term alpha, entering the same difference as theta. Within one stratum alpha is common to
//     every match that stratum played, so a developer who ran two models on ONE scaffold
//     identifies the difference between those models with the harness held constant. That is
//     the whole design: it is a paired comparison, not an adjustment applied afterwards.
//
//     A preference arena cannot do this — there the prompt IS the submission, so there is no
//     harness to hold constant. It is available here only because scaffold fingerprints exist.
//
//  3. RANKINGS ARE BROKEN INTO PAIRS. Monopoly and Mafia are N-player. A placement is
//     decomposed into all pairwise comparisons it implies, which is consistent for the
//     Plackett-Luce family under full rank-breaking (Azari Soufiani et al. 2014). Mafia is
//     additionally ROLE-CONDITIONED: only seats that held the same role are compared, because
//     a mafioso beating a villager is mostly evidence about role assignment.
//
// # Manipulation, which is the part most boards get wrong
//
// "The Leaderboard Illusion" (Singh et al. 2025, arXiv:2504.20879) shows the dominant failure
// of arena leaderboards is not noise but SELECTION: a provider privately tests N variants and
// keeps the best, which violates Bradley-Terry's assumption that comparisons are sampled
// independently of their outcome. They measure roughly +100 Elo from ten private variants.
//
// Pyyol is structurally able to refuse that attack, and this is the substantive advantage over
// a preference arena rather than a methodological flourish:
//
//   - EVERY comparison entering this fit is a proof-bound decision recorded when it happened,
//     with coins moved against it. A developer cannot run ten variants and submit the best,
//     because the losses are already in the ledger. There is no submission step to select at.
//   - COVERAGE is known. A row states what fraction of its play was proven, so "the part we
//     did not see" is a published number rather than an unmodelled hole.
//
// What Pyyol cannot refuse structurally is a developer who ABANDONS a losing model — the
// deprecation bias the same paper documents. That is why every model's entry cohort and its
// full record travel with the rating, and why a model with few matches is marked provisional
// rather than quietly ranked.
//
// Following the perturbation framework of arXiv:2605.15761, the fit also reports what it is
// SENSITIVE to rather than only what it concluded: bootstrap intervals (cluster-resampled by
// match, as Chatbot Arena does), rank stability across replicates, and how much of each
// model's evidence bridges more than one harness. A rating whose ordering survives 60% of
// replicates is not the same claim as one that survives 99%, and a board that prints both as
// a rank is hiding the difference.
package modelboard

import (
	"math"
	"sort"
)

// Outcome is what happened between two seats in one comparison.
type Outcome int

const (
	// Win means the first side won.
	Win Outcome = iota
	// Loss means the second side won.
	Loss
	// Draw means neither did. Modelled, not discarded — see the Davidson note above.
	Draw
)

// Comparison is one pairwise observation, already rank-broken and role-conditioned.
//
// MatchID is carried because the bootstrap resamples MATCHES, not comparisons: an N-player
// match yields several correlated comparisons, and resampling them independently would treat
// one match as several and shrink every interval by a factor nobody can justify.
type Comparison struct {
	MatchID string
	// ModelA/ModelB are canonical model keys ("anthropic/claude-opus-4").
	ModelA, ModelB string
	// StratumA/StratumB identify the (developer, scaffold) harness behind each seat. Equal
	// strata cancel exactly, which is the cleanest evidence this estimator can receive.
	StratumA, StratumB string
	Outcome            Outcome
	// Weight allows a comparison to count less than one. Used by rank-breaking so an
	// N-player match does not contribute more total evidence than a 1v1 match.
	Weight float64
}

// Config is the fit's tunable parameters. Every default is stated and justified, because a
// leaderboard whose constants are undocumented is not reproducible.
type Config struct {
	// L2 is the ridge penalty on model strengths — equivalently a zero-mean Normal prior, so
	// the fit is a MAP estimate. It does two necessary jobs: it makes the problem strictly
	// convex (so the optimum is unique even when a model has only wins), and it shrinks
	// thinly-observed models toward the population mean instead of letting a 2-0 record
	// produce an infinite rating.
	L2 float64
	// L2Stratum penalizes harness abilities. Weaker than L2 by default: harness ability is
	// the thing we are conditioning on rather than estimating carefully, and over-shrinking it
	// would push the confound back into the model terms, which is the exact failure this
	// package exists to prevent.
	L2Stratum float64
	// FitTies estimates Davidson's nu. Disable only to reproduce a plain Bradley-Terry fit.
	//
	// Even when enabled, nu is NOT fitted on data containing no draws — see Estimate. With zero
	// draws the tie parameter has no finite maximum-likelihood estimate: the likelihood is
	// strictly increasing as nu falls toward 0, so the optimizer drifts log(nu) toward negative
	// infinity forever while the strengths sit converged. That presents as a stubbornly
	// unconverged fit and invites tolerance-loosening, when the honest answer is that a
	// draw-propensity parameter describes nothing on data with no draws.
	FitTies bool
	// L2Tie keeps nu identified when draws are RARE rather than absent. A handful of draws in
	// tens of thousands of comparisons pushes nu to the same boundary; a weak prior on log(nu)
	// bounds it without meaningfully moving a fit that has real draws to learn from.
	L2Tie float64
	// MaxIter / Tol bound the optimizer. Convergence is monotone (see solve), so hitting
	// MaxIter means slow progress rather than divergence — reported, never silently accepted.
	MaxIter int
	// Tol is the gradient-norm stopping threshold. 1e-7 rather than something tighter because
	// theta enters the published number through a 400/ln10 ~ 174 multiplier: a residual of 1e-7
	// in log-odds is 2e-5 Elo points, far below anything a board displays or a reader could act
	// on. A tolerance tighter than the output's resolution buys iterations, not accuracy.
	Tol float64
	// BootstrapReplicates sets the interval precision. 1000 is the arena convention; below a
	// few hundred the percentile bounds are themselves noisy enough to reorder a board.
	BootstrapReplicates int
	// MinComparisons is the evidence a model needs before it is ranked rather than marked
	// provisional. Provisional models are still SHOWN: hiding them would make the board look
	// complete when it is not, which is how a reader concludes a model was never tested.
	MinComparisons int
	// Seed makes the bootstrap reproducible. A published interval that cannot be recomputed is
	// not a published interval.
	Seed uint64
}

// DefaultConfig is the published configuration.
func DefaultConfig() Config {
	return Config{
		L2:                  1.0,
		L2Stratum:           0.25,
		FitTies:             true,
		L2Tie:               1e-3,
		MaxIter:             2000,
		Tol:                 1e-7,
		BootstrapReplicates: 1000,
		MinComparisons:      30,
		Seed:                20260806,
	}
}

// Rating is one model's place on the board.
type Rating struct {
	Model string `json:"model"`
	// Theta is the fitted Bradley-Terry strength in log-odds. The primary quantity; everything
	// below is a presentation of it.
	Theta float64 `json:"theta"`
	// Elo is Theta on the conventional scale (400/ln10 per log-odds, anchored at 1500), which
	// is the same transform Chatbot Arena publishes. Carries no extra information — it exists
	// because readers can compare 1520-vs-1480 and cannot compare 0.115-vs-(-0.115).
	Elo float64 `json:"elo"`
	// EloLow/EloHigh are the 95% bootstrap percentile interval.
	EloLow  float64 `json:"elo_low"`
	EloHigh float64 `json:"elo_high"`
	// Rank is by the LOWER bound, not the point estimate. A model with a wide interval has not
	// earned a place above one whose interval is tight and only slightly lower — ranking on the
	// point estimate systematically promotes the least-observed models.
	Rank int `json:"rank"`
	// RankStability is the fraction of bootstrap replicates in which this model held Rank. The
	// "stability certificate" of arXiv:2605.15761, made cheap by reusing the bootstrap we
	// already run. 0.6 and 0.99 are different claims and must not both print as a rank.
	RankStability float64 `json:"rank_stability"`

	Comparisons int `json:"comparisons"`
	Wins        int `json:"wins"`
	Losses      int `json:"losses"`
	Draws       int `json:"draws"`
	// Harnesses is how many distinct (developer, scaffold) strata ran this model.
	Harnesses int `json:"harnesses"`
	// BridgedComparisons is the evidence that came from a stratum which ALSO ran another
	// model. Only that evidence separates the model from its harness: a model run by exactly
	// one developer on one scaffold is not distinguishable from that developer, however many
	// matches it played.
	BridgedComparisons int `json:"bridged_comparisons"`
	// Separability is BridgedComparisons / Comparisons. Published because a high rating with
	// low separability is a statement about a person, not a model.
	Separability float64 `json:"separability"`
	// Provisional marks too little evidence to rank on, mirroring how established arenas tag
	// low-vote models.
	Provisional bool `json:"provisional"`
}

// Fit is a completed estimation, including what it could not do.
type Fit struct {
	Ratings []Rating `json:"ratings"`
	// Nu is Davidson's fitted tie propensity. Large nu means draws dominate, which is itself
	// worth seeing: it says the games are not separating these models.
	Nu float64 `json:"nu"`
	// Converged is false when the optimizer hit MaxIter. Published rather than swallowed: an
	// unconverged fit may still be usable, and pretending otherwise is how a silently bad
	// number gets a rank next to it.
	Converged  bool `json:"converged"`
	Iterations int  `json:"iterations"`
	// Comparisons and Matches are the evidence base.
	Comparisons int `json:"comparisons"`
	Matches     int `json:"matches"`
	// Strata is how many distinct harnesses were conditioned out.
	Strata int `json:"strata"`
	// Excluded records what was dropped and why, so a missing model is explainable rather
	// than mysterious.
	Excluded map[string]int `json:"excluded,omitempty"`
}

// eloPerLogOdds converts Bradley-Terry log-odds to the Elo scale.
//
// 400/ln(10): Elo is defined so a 400-point gap is 10:1 odds, and Bradley-Terry works in
// natural log-odds. This is the standard correspondence, not a tuned constant.
const eloPerLogOdds = 400 / math.Ln10

// eloAnchor centres the scale. Arbitrary but conventional; only DIFFERENCES are meaningful,
// which is why the methodology page has to say so next to the number.
const eloAnchor = 1500.0

// ToElo maps a strength in log-odds onto the published scale.
func ToElo(theta float64) float64 { return eloAnchor + eloPerLogOdds*theta }

// probs returns P(A wins), P(B wins), P(draw) under Davidson's model.
//
// Written in terms of half-differences so it is numerically symmetric in A and B: computing
// exp(d) directly overflows for large gaps, and a leaderboard that produces NaN for a
// dominant model is worse than one that saturates.
func probs(d, nu float64) (pWin, pLoss, pDraw float64) {
	h := d / 2
	// Clamp the half-difference. At |h| = 30 the implied odds are already e^60, far beyond any
	// distinction this data can support, and past ~350 exp overflows to +Inf.
	if h > 30 {
		h = 30
	} else if h < -30 {
		h = -30
	}
	a, b := math.Exp(h), math.Exp(-h)
	z := a + b + nu
	return a / z, b / z, nu / z
}

// logLikelihood is the penalized log-likelihood, and gradient fills its derivatives.
//
// Returned together because every optimizer step needs both and computing them in one pass
// over the comparisons halves the work on the hot loop.
type problem struct {
	cmp        []Comparison
	modelIdx   map[string]int
	stratumIdx map[string]int
	cfg        Config
}

// nuIdx marks the tie parameter's slot in the packed parameter vector.
func (p *problem) dim() int { return len(p.modelIdx) + len(p.stratumIdx) + 1 }
func (p *problem) nuSlot() int {
	return len(p.modelIdx) + len(p.stratumIdx)
}

// objective returns the NEGATIVE penalized log-likelihood and its gradient.
//
// Minimizing the negative keeps the optimizer conventional. nu is parameterized as
// nu = exp(g) so it stays strictly positive without a constrained solver; the chain rule for
// that reparameterization is applied at the end.
func (p *problem) objective(x []float64) (float64, []float64) {
	nModels := len(p.modelIdx)
	grad := make([]float64, p.dim())
	nu := 0.0
	g := x[p.nuSlot()]
	if p.cfg.FitTies {
		nu = math.Exp(g)
	}

	var nll float64
	for i := range p.cmp {
		c := &p.cmp[i]
		ia, ib := p.modelIdx[c.ModelA], p.modelIdx[c.ModelB]
		d := x[ia] - x[ib]
		var sa, sb int = -1, -1
		if c.StratumA != "" {
			if k, ok := p.stratumIdx[c.StratumA]; ok {
				sa = nModels + k
				d += x[sa]
			}
		}
		if c.StratumB != "" {
			if k, ok := p.stratumIdx[c.StratumB]; ok {
				sb = nModels + k
				d -= x[sb]
			}
		}

		pw, pl, pd := probs(d, nu)
		w := c.Weight
		if w <= 0 {
			w = 1
		}

		// dNLL/dd for each outcome. Derived from Davidson's likelihood: with
		// z = e^(d/2) + e^(-d/2) + nu, dz/dd = (e^(d/2) - e^(-d/2))/2, and the win term is
		// log(e^(d/2)) - log z = d/2 - log z. So d/dd of -log P(win) = -(1/2 - (dz/dd)/z),
		// and (dz/dd)/z = (pw - pl)/2. The same substitution gives the other two outcomes.
		var dd float64
		switch c.Outcome {
		case Win:
			nll -= w * math.Log(math.Max(pw, 1e-300))
			dd = -w * (0.5 - (pw-pl)/2)
		case Loss:
			nll -= w * math.Log(math.Max(pl, 1e-300))
			dd = -w * (-0.5 - (pw-pl)/2)
		case Draw:
			nll -= w * math.Log(math.Max(pd, 1e-300))
			dd = -w * (-(pw - pl) / 2)
		}
		grad[ia] += dd
		grad[ib] -= dd
		if sa >= 0 {
			grad[sa] += dd
		}
		if sb >= 0 {
			grad[sb] -= dd
		}

		if p.cfg.FitTies {
			// dNLL/dnu, then chain through nu = exp(g).
			var dnu float64
			switch c.Outcome {
			case Win, Loss:
				dnu = w / (math.Exp(d/2) + math.Exp(-d/2) + nu)
			case Draw:
				dnu = w * (1/(math.Exp(d/2)+math.Exp(-d/2)+nu) - 1/nu)
			}
			grad[p.nuSlot()] += dnu * nu
		}
	}

	// Ridge penalties. Model and stratum terms are penalized separately: see Config.L2Stratum
	// for why over-shrinking the harness would push the confound back into the model.
	for i := 0; i < nModels; i++ {
		nll += 0.5 * p.cfg.L2 * x[i] * x[i]
		grad[i] += p.cfg.L2 * x[i]
	}
	for i := nModels; i < nModels+len(p.stratumIdx); i++ {
		nll += 0.5 * p.cfg.L2Stratum * x[i] * x[i]
		grad[i] += p.cfg.L2Stratum * x[i]
	}
	// A weak prior on log(nu), which is what keeps the tie parameter identified when draws are
	// rare. Without it a near-draw-free dataset sends log(nu) toward negative infinity and the
	// fit never converges even though every strength has settled.
	if p.cfg.FitTies && p.cfg.L2Tie > 0 {
		nll += 0.5 * p.cfg.L2Tie * g * g
		grad[p.nuSlot()] += p.cfg.L2Tie * g
	}
	return nll, grad
}

// sortedKeys gives every map iteration a deterministic order.
//
// Not cosmetic: parameter ORDER decides floating-point summation order, so map iteration would
// make two runs on identical data produce ratings that differ in the last bits — and a
// published leaderboard that will not reproduce is not evidence of anything.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// index assigns stable slots to models and strata.
func index(cmp []Comparison) (models, strata map[string]int) {
	mset, sset := map[string]struct{}{}, map[string]struct{}{}
	for i := range cmp {
		mset[cmp[i].ModelA] = struct{}{}
		mset[cmp[i].ModelB] = struct{}{}
		if cmp[i].StratumA != "" {
			sset[cmp[i].StratumA] = struct{}{}
		}
		if cmp[i].StratumB != "" {
			sset[cmp[i].StratumB] = struct{}{}
		}
	}
	models, strata = map[string]int{}, map[string]int{}
	mkeys := make([]string, 0, len(mset))
	for k := range mset {
		mkeys = append(mkeys, k)
	}
	sort.Strings(mkeys)
	for i, k := range mkeys {
		models[k] = i
	}
	skeys := make([]string, 0, len(sset))
	for k := range sset {
		skeys = append(skeys, k)
	}
	sort.Strings(skeys)
	for i, k := range skeys {
		strata[k] = i
	}
	return models, strata
}
