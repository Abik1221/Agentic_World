package skill

import (
	"math"
	"sort"
)

// Ranking many agents fairly when they have wildly different sample sizes.
//
// # The problem a raw mean cannot solve
//
// Decision quality is a mean, and means over small samples are wild. An agent with five
// scored decisions and a lucky run reads 100%; an agent with four thousand decisions and
// genuine strength reads 78%. Sorting by the raw mean puts the five-decision agent on top
// of a public leaderboard, and no amount of "we also show the sample size" fixes it —
// people sort by the number, not by the footnote.
//
// A minimum-sample cutoff is the usual reflex and it is worse than it looks: it throws
// away every agent below the line rather than ranking them, and the cutoff is arbitrary.
//
// # Empirical Bayes shrinkage
//
// The correct treatment is to stop asking "what did this agent score" and start asking
// "what is this agent's TRUE quality, given what we observed AND what we know about
// agents in general". Quality lives in [0,1], so model it as Beta(α, β) and each agent's
// record as draws from it. The posterior mean is
//
//	q̂ = (observed_successes + α) / (n + α + β)
//
// which is the observed mean pulled toward the population mean by an amount that depends
// on n. Five decisions barely move off the population mean; four thousand decisions sit
// essentially at their observed value. The estimator does automatically, and with a
// principled weight, what a hand-tuned sample-size gate does badly.
//
// α and β are estimated FROM THE POPULATION by method of moments — this is the
// "empirical" in empirical Bayes. Nothing is hand-picked, so the prior tightens by itself
// as the platform gets more agents and more play.
//
// This is the same estimator behind Bayesian batting averages and every credible
// small-sample sports ranking. It is the right tool and it is not a new one.

// Observation is one agent's scored record, ready for ranking.
type Observation struct {
	AgentPublicID string
	// Decisions is the sample size — how many decisions could be scored.
	Decisions int
	// Quality is the observed mean in [0,1] (1 − mean regret).
	Quality float64
}

// Prior is a Beta(α, β) fitted to the population of agents.
type Prior struct {
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
	// Mean is α/(α+β): what the platform expects of an agent it knows nothing about.
	Mean float64 `json:"mean"`
	// Strength is α+β, in units of decisions. This is the intuitive number: it is how
	// many observed decisions it takes to move an agent halfway from the population mean
	// to its own observed mean. Reported because it is the one parameter an operator
	// should sanity-check.
	Strength float64 `json:"strength"`
	// Agents is how many agents the prior was fitted from.
	Agents int `json:"agents"`
}

// defaultPriorStrength is used when the population is too small or too uniform to fit a
// prior from. 50 decisions is roughly four Goofspiel matches — enough that a genuinely
// strong agent separates from the mean, few enough that it is not a cutoff in disguise.
const defaultPriorStrength = 50.0

// FitPrior estimates Beta(α, β) from the population by method of moments.
//
// Weighted by sample size: an agent with 4000 decisions tells us far more about the
// population than one with 4, and weighting them equally would let a crowd of tiny
// samples drag the prior around — which would then distort every shrunk estimate that
// depends on it.
//
// Falls back to a weakly-informative prior centred on the observed mean when the
// population cannot support a fit (too few agents, or a between-agent variance so small
// that the method-of-moments denominator is meaningless). Falling back is not a failure
// mode here: with no spread between agents there is nothing for a prior to encode.
func FitPrior(obs []Observation) Prior {
	// Sum in a deterministic order. Floating-point addition is not associative, so the
	// same population supplied in a different order fitted a prior that differed in the
	// last bits — and since every agent's estimate is computed against that prior, the
	// whole leaderboard would shift microscopically depending on row order. Small, but it
	// breaks the reproducibility this package is built on.
	ordered := make([]Observation, len(obs))
	copy(ordered, obs)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].AgentPublicID != ordered[j].AgentPublicID {
			return ordered[i].AgentPublicID < ordered[j].AgentPublicID
		}
		if ordered[i].Decisions != ordered[j].Decisions {
			return ordered[i].Decisions < ordered[j].Decisions
		}
		return ordered[i].Quality < ordered[j].Quality
	})

	var totalW, sumW float64
	usable := 0
	for _, o := range ordered {
		if o.Decisions <= 0 {
			continue
		}
		w := float64(o.Decisions)
		totalW += w
		sumW += w * clamp01(o.Quality)
		usable++
	}
	if usable == 0 || totalW == 0 {
		// No data at all: a symmetric prior that expresses genuine ignorance.
		return Prior{Alpha: defaultPriorStrength / 2, Beta: defaultPriorStrength / 2,
			Mean: 0.5, Strength: defaultPriorStrength, Agents: 0}
	}
	// Held off the boundary. A population where every agent scores 1.0 would fit β = 0,
	// which is not a Beta distribution at all and makes every posterior degenerate. The
	// clamp costs nothing — no real population sits exactly on 0 or 1 — and it guarantees
	// α and β stay strictly positive whatever the data does.
	mean := clampRange(sumW/totalW, 0.01, 0.99)

	// Weighted between-agent variance.
	var ss float64
	for _, o := range ordered {
		if o.Decisions <= 0 {
			continue
		}
		d := clamp01(o.Quality) - mean
		ss += float64(o.Decisions) * d * d
	}
	variance := ss / totalW

	// Method of moments for a Beta: strength = μ(1−μ)/σ² − 1. Guard the degenerate
	// cases — a mean at 0 or 1, or a variance at or above the maximum μ(1−μ) — where the
	// expression is undefined or non-positive.
	maxVar := mean * (1 - mean)
	if usable < 3 || variance <= 1e-9 || maxVar <= 1e-9 || variance >= maxVar {
		return Prior{
			Alpha: defaultPriorStrength * mean, Beta: defaultPriorStrength * (1 - mean),
			Mean: mean, Strength: defaultPriorStrength, Agents: usable,
		}
	}
	strength := maxVar/variance - 1
	// Keep the prior weakly informative. An unbounded fit on an early, lumpy population
	// can produce a prior so strong that every agent is pinned to the mean and the
	// leaderboard stops discriminating at all.
	strength = math.Max(1, math.Min(strength, 500))

	return Prior{
		Alpha: strength * mean, Beta: strength * (1 - mean),
		Mean: mean, Strength: strength, Agents: usable,
	}
}

// Ranked is one agent's shrunk, comparable score.
type Ranked struct {
	AgentPublicID string  `json:"agent_public_id"`
	Decisions     int     `json:"decisions"`
	Observed      float64 `json:"observed"` // the raw mean, kept for transparency
	// Estimate is the posterior mean — the best single guess at true quality, and the
	// number to DISPLAY.
	Estimate float64 `json:"estimate"`
	// Lower / Upper bound a 95% credible interval on Estimate.
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
	// RankScore is the LOWER bound, and it is what a leaderboard must sort on.
	//
	// The posterior mean alone is not enough, and the reason is subtle: empirical Bayes
	// fits the prior from the population, so when agents genuinely differ a lot the fitted
	// prior is WEAK — correctly, because individual observations really are informative.
	// But a weak prior barely shrinks a 5-decision record, so a small perfect sample can
	// still finish above a large strong one on the posterior mean.
	//
	// The lower bound closes that, and closes it for a principled reason rather than by
	// adding a fudge factor: a small sample has a wide interval, so ranking on "what are
	// we CONFIDENT this agent is at least worth" penalises uncertainty exactly in
	// proportion to how uncertain we are. Big samples are barely affected; tiny ones are
	// held back until they earn their place. This is the standard treatment for ranking
	// under unequal sample sizes and it needs no arbitrary minimum-games cutoff.
	RankScore float64 `json:"rank_score"`
	// Shrinkage in [0,1] is how far the estimate was pulled toward the population mean.
	// 0 = the data spoke for itself; 1 = we learned nothing from this agent.
	Shrinkage float64 `json:"shrinkage"`
}

// Shrink converts an observation into a rankable estimate under the given prior.
func Shrink(o Observation, p Prior) Ranked {
	q := clamp01(o.Quality)
	n := float64(o.Decisions)
	if n <= 0 {
		// No evidence: the estimate IS the prior, and the interval is as wide as it gets.
		// RankScore 0 puts an unscored agent last, which is correct — it has shown
		// nothing, and the prior mean is an assumption rather than an achievement.
		return Ranked{AgentPublicID: o.AgentPublicID, Observed: 0,
			Estimate: p.Mean, Lower: 0, Upper: 1, RankScore: 0, Shrinkage: 1}
	}

	alpha := q*n + p.Alpha
	beta := (1-q)*n + p.Beta
	total := alpha + beta
	est := alpha / total

	// Posterior standard deviation of a Beta, then a normal approximation for the
	// interval. Adequate here because α and β are both comfortably large by the time an
	// agent has any decisions at all, and the alternative (exact Beta quantiles) buys
	// precision that no leaderboard renders.
	sd := math.Sqrt(alpha * beta / (total * total * (total + 1)))

	lower := clamp01(est - 1.96*sd)
	return Ranked{
		AgentPublicID: o.AgentPublicID,
		Decisions:     o.Decisions,
		Observed:      q,
		Estimate:      est,
		Lower:         lower,
		Upper:         clamp01(est + 1.96*sd),
		RankScore:     lower,
		Shrinkage:     p.Strength / (n + p.Strength),
	}
}

// Rank fits a prior to the population, shrinks every observation against it, and returns
// the agents in leaderboard order (best first).
//
// One call, because the steps must not be separated: shrinking against a prior fitted
// from a DIFFERENT population is how an estimator quietly stops being calibrated, and
// sorting on the wrong field is how a leaderboard quietly becomes sortable by luck.
func Rank(obs []Observation) ([]Ranked, Prior) {
	p := FitPrior(obs)
	out := make([]Ranked, 0, len(obs))
	for _, o := range obs {
		out = append(out, Shrink(o, p))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RankScore != out[j].RankScore {
			return out[i].RankScore > out[j].RankScore
		}
		// Deterministic tie-break: more evidence first, then id. Never leave leaderboard
		// order to sort instability.
		if out[i].Decisions != out[j].Decisions {
			return out[i].Decisions > out[j].Decisions
		}
		return out[i].AgentPublicID < out[j].AgentPublicID
	})
	return out, p
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func clampRange(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
