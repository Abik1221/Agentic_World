// Package arenanorm makes win rates comparable across arenas, and rankable on evidence.
//
// # The bug this exists to close
//
// ModelStat.WinRate is Wins/(Wins+Losses) pooled across every arena, and BuildDeveloperEdges
// keys its population baselines on provider+"/"+model with no game dimension at all. But the
// arenas do not share a base rate: Goofspiel is one-of-two, Monopoly is one-of-n, and Mafia
// pays a whole winning team at once. Pooling them compares numbers that were never on the
// same scale.
//
// The consequence is a one-line exploit. A developer who queues ONLY 1v1 gets a raw win rate
// near 50% while the population baseline for their model is dragged down by four-player
// Monopoly, and the board publishes the difference as if it were skill. The audit put that at
// roughly +27 points of "skill above model" available from a queue filter and no strategy.
//
// # The fix, using the platform's own patterns rather than new ones
//
// Two devices already in this codebase, combined:
//
//  1. LIFT OVER A CHANCE BASELINE. internal/skill scores Mafia as Cohen's-kappa lift,
//     (acc - chance)/(1 - chance), and internal/deception publishes ExcessOverChance beside
//     every rate. Same idea here: a rate is only meaningful against the base rate of the
//     arena it was earned in. lift = (rate - baseline)/(1 - baseline) maps baseline to 0 and
//     perfection to 1, on every arena, whatever its shape.
//
//  2. RANK ON THE LOWER BOUND. internal/modelboard ranks on the bootstrap lower bound and
//     internal/skill/ranking.go on a credible lower bound. Ranking on a point estimate lets
//     four games at 100% outrank four hundred at 70%, which is how a young board embarrasses
//     itself. Here the Wilson lower bound is taken per arena BEFORE lifting.
//
// # Why the baseline is measured, not declared
//
// The obvious implementation is a table of chance rates: 0.5 for Goofspiel, 1/n for Monopoly,
// something hand-derived for Mafia. That table would be wrong the moment a game's rules,
// seat count or tie handling changed, and wrong silently.
//
// Mafia shows why it is also hard to get right on paper: wins are recorded for every seat at
// the minimum placement, so a town victory writes nine wins and three losses. The naive
// "1/12" is badly wrong; the true base rate depends on how often each team wins AND on the
// roster split. Deriving that by hand invites exactly the kind of constant this repo has
// learned to distrust.
//
// So the baseline for an arena is the POPULATION's own win rate in that arena. It is correct
// by construction, it tracks rule changes for free, and it needs no game knowledge. It is the
// same choice edge.go already makes when it builds population baselines per model.
//
// # Purity
//
// No clock, no RNG, no I/O. Same inputs, same ranking, forever.
package arenanorm

import (
	"math"
	"sort"
)

// z95 is the two-sided 95% normal quantile used for the Wilson interval, matching
// rating.wilsonHalfWidth95 so two surfaces cannot disagree about what "95%" means.
const z95 = 1.96

// Record is one model's decisive results in one arena. Ties are excluded rather than counted
// as half a win: the arenas disagree about what a tie means, and averaging that disagreement
// into the numerator is the pooling mistake in miniature.
type Record struct {
	Arena  string
	Wins   int
	Losses int
}

// Decisive is the denominator for this record.
func (r Record) Decisive() int { return r.Wins + r.Losses }

// Baselines maps an arena to that arena's population win rate.
//
// Build it with PopulationBaselines rather than by hand.
type Baselines map[string]float64

// PopulationBaselines derives each arena's base rate from every record in that arena.
//
// Pooled from raw counts, not averaged over per-model rates: a model with four games must not
// pull the baseline as hard as one with four hundred. internal/rating.groups makes the same
// choice for the same reason.
func PopulationBaselines(all []Record) Baselines {
	wins := map[string]int{}
	dec := map[string]int{}
	for _, r := range all {
		wins[r.Arena] += r.Wins
		dec[r.Arena] += r.Decisive()
	}
	out := make(Baselines, len(dec))
	for arena, n := range dec {
		if n > 0 {
			out[arena] = float64(wins[arena]) / float64(n)
		}
	}
	return out
}

// wilsonLower returns the lower end of the 95% Wilson score interval for k of n.
//
// Wilson rather than the normal approximation because the normal interval misbehaves exactly
// where a young board lives: at 4 wins from 4 games it reports a lower bound of 1.0, claiming
// certainty from nothing.
func wilsonLower(k, n int) float64 {
	if n <= 0 {
		return 0
	}
	nf := float64(n)
	p := float64(k) / nf
	denom := 1 + z95*z95/nf
	centre := p + z95*z95/(2*nf)
	margin := z95 * math.Sqrt(p*(1-p)/nf+z95*z95/(4*nf*nf))
	lo := (centre - margin) / denom
	if lo < 0 {
		return 0
	}
	return lo
}

// lift converts a rate into arena-independent units: 0 is the arena's base rate, 1 is
// perfection, negative is below base.
//
// A baseline at or above 1 leaves no headroom to measure — every seat wins — so the arena
// carries no signal and lift is 0 rather than an infinity.
func lift(rate, baseline float64) float64 {
	room := 1 - baseline
	if room <= 0 {
		return 0
	}
	l := (rate - baseline) / room
	if l < -1 {
		return -1
	}
	if l > 1 {
		return 1
	}
	return l
}

// Score is a model's arena-normalised standing.
type Score struct {
	// Lift is the games-weighted mean lift over each arena's base rate, using the POINT
	// estimate. This is the number to display.
	Lift float64
	// LiftLower is the same weighted mean computed from each arena's Wilson LOWER bound.
	// This is the number to RANK on: it cannot be inflated by a tiny lucky sample.
	LiftLower float64
	// Decisive is the total decisive games behind the score — the denominator a reader
	// needs and which the current boards do not publish.
	Decisive int
	// Arenas is how many distinct arenas contributed.
	Arenas int
	// Comparable is false when nothing scorable was supplied. A zero Lift from no data must
	// not read as "exactly average"; callers should withhold the row rather than rank it.
	Comparable bool
}

// Compute scores one model's records against the population baselines.
//
// Arenas absent from baselines are SKIPPED, not assumed. An arena we have no population rate
// for is one we cannot normalise, and inventing a baseline of 0.5 would silently reintroduce
// the very assumption this package removes.
func Compute(records []Record, base Baselines) Score {
	var num, numLo float64
	var weight int
	arenas := map[string]bool{}

	for _, r := range records {
		n := r.Decisive()
		if n <= 0 {
			continue
		}
		b, ok := base[r.Arena]
		if !ok {
			continue
		}
		rate := float64(r.Wins) / float64(n)
		num += float64(n) * lift(rate, b)
		numLo += float64(n) * lift(wilsonLower(r.Wins, n), b)
		weight += n
		arenas[r.Arena] = true
	}
	if weight == 0 {
		return Score{}
	}
	return Score{
		Lift:       num / float64(weight),
		LiftLower:  numLo / float64(weight),
		Decisive:   weight,
		Arenas:     len(arenas),
		Comparable: true,
	}
}

// Ranked is one entry in a normalised ranking.
type Ranked struct {
	Key   string
	Score Score
}

// Rank orders models by LiftLower, descending, breaking ties by sample size and then by key.
//
// The key tie-break exists so the order is total and deterministic: Go map iteration is
// randomised, and a leaderboard whose rows shuffle between requests reads as broken even when
// the numbers are identical.
//
// Entries that are not Comparable are dropped rather than sorted to the bottom. A row with no
// evidence is not "last"; it is unmeasured, and the caller should say so separately.
func Rank(scores map[string]Score) []Ranked {
	out := make([]Ranked, 0, len(scores))
	for k, s := range scores {
		if !s.Comparable {
			continue
		}
		out = append(out, Ranked{Key: k, Score: s})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score.LiftLower != b.Score.LiftLower {
			return a.Score.LiftLower > b.Score.LiftLower
		}
		if a.Score.Decisive != b.Score.Decisive {
			return a.Score.Decisive > b.Score.Decisive
		}
		return a.Key < b.Key
	})
	return out
}
