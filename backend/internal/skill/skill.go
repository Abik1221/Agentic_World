// Package skill scores an agent's DECISIONS, not its results.
//
// # Why this exists
//
// Pyyol had two scoring systems and neither answered "was that a good move".
//
//   - Rating (Glicko-2 / TrueSkill) scores OUTCOMES. In games with this much variance a
//     strong agent loses constantly, so win rate is a low-information estimator of skill:
//     one 13-round Goofspiel match yields exactly ONE data point.
//   - P-Index "Intelligence" scores HYGIENE — legal-move rate, fallback rate, latency.
//     An agent that plays fast, legal, terrible moves scores full marks.
//
// This package supplies the missing axis. It scores every individual decision against
// what was actually available from that exact state, so the same 13-round match yields
// THIRTEEN data points instead of one. That is a 13× increase in statistical power over
// data the platform already persists, and it is the difference between "this agent got
// lucky" and "this agent plays well".
//
// # Three properties this is built to have
//
// DETERMINISTIC. Every scorer here is a pure function of (state, action). No clocks, no
// randomness, no model calls. The same decision scores identically forever, which is what
// makes a ranking disputable-and-then-settled rather than just disputable. It also means
// scores can be recomputed offline over history when a scorer improves, and independently
// verified by anyone holding the replay.
//
// NO LLM JUDGE, deliberately. The engine already knows the ground truth — every Mafia
// role, both Goofspiel hands, the committed prize order. A judge model would be guessing
// at what the engine knows for certain, at real cost, with known verbosity and
// self-preference biases. Worse, agents optimise against whatever is scored: put a judge
// on the ranking path and the winning strategy becomes writing persuasive rationales
// rather than playing well.
//
// OPPONENT-INDEPENDENT. A decision is scored against the best alternative from the same
// state, not against what the opponent happened to hold. Two agents facing identical
// states are directly comparable even if they never met.
//
// # What a score means
//
// Every scorer returns Regret in [0,1]: the share of the achievable value the decision
// gave up. 0 is the best available action; 1 is the worst. Quality = 1 - Regret.
//
// Normalising per decision is what makes rounds, games and agents commensurable. A
// 2-point blunder in a 13-point round and a 2-point blunder in a 3-point round are not
// the same mistake, and an un-normalised sum would call them equal.
package skill

import "math"

// Decision is one scored decision. Regret and Quality are the summary; the rest is the
// audit trail, because a score a developer cannot interrogate is a score they cannot
// act on.
type Decision struct {
	Round int
	// Chosen is the action the agent played, in the game's own vocabulary.
	Chosen string
	// Best is the action with the highest value from this state. Equal to Chosen when
	// the agent found the best move.
	Best string
	// ValueChosen / ValueBest are in the game's native units (prize points, log-loss
	// nats, …). Exposed so a developer can see the size of a mistake, not just its rank.
	ValueChosen float64
	ValueBest   float64
	// ValueWorst anchors the normalisation. Regret is the position of the choice on the
	// [worst, best] interval, which is why a decision with only one legal action scores
	// 0 rather than dividing by zero.
	ValueWorst float64
	// Regret in [0,1]: 0 = best available, 1 = worst available.
	Regret float64
	// Why explains the number in one line, for the trace UI.
	Why string
}

// Quality is 1-Regret: the share of achievable value the decision captured.
func (d Decision) Quality() float64 { return 1 - d.Regret }

// normalize maps a chosen value onto [0,1] regret given the best and worst available.
//
// A state where every action is equivalent (best == worst) scores ZERO regret, not
// undefined and not maximum. The agent cannot be blamed for a decision that carried no
// choice, and a forced move is the single most common such state — scoring it as a
// blunder would punish agents for the game's structure rather than their play.
func normalize(chosen, best, worst float64) float64 {
	span := best - worst
	if span <= 1e-12 {
		return 0
	}
	r := (best - chosen) / span
	return math.Max(0, math.Min(1, r))
}

// Summary aggregates scored decisions into one comparable number.
type Summary struct {
	Decisions  int     `json:"decisions"`
	MeanRegret float64 `json:"mean_regret"`
	// Quality is 1-MeanRegret in [0,1].
	Quality float64 `json:"quality"`
	// StdErr is the standard error of MeanRegret. Reported because a mean over 8
	// decisions and a mean over 800 are not the same claim, and a leaderboard that
	// renders them identically is lying by omission.
	StdErr float64 `json:"std_err"`
	// Blunders counts decisions that gave up more than BlunderThreshold of the
	// available value. Mean regret hides these: an agent that is near-perfect for
	// twelve rounds and throws the thirteenth looks fine on the mean and is not fine.
	Blunders int `json:"blunders"`
	// Perfect counts decisions that found the best available action.
	Perfect int `json:"perfect"`
}

// BlunderThreshold is the regret above which a decision is a blunder rather than an
// inaccuracy. 0.5 = gave up more than half the value that was on the table.
const BlunderThreshold = 0.5

// ScorerVersion identifies the algorithm that produced a stored score.
//
// BUMP THIS whenever a change alters the number a scorer returns for the same input —
// a different continuation potential, a different reference policy, a changed iteration
// count, a fixed bug. Persisted alongside every score so a worker can find the rows that
// are behind and recompute them, and so a reader never mixes scores from two different
// yardsticks in one average.
//
// Do NOT bump for changes that cannot move a score (comments, refactors, new helpers):
// a needless bump rescores the entire history for nothing.
const ScorerVersion = 1

// Summarize reduces scored decisions to a Summary. Safe on an empty slice.
func Summarize(ds []Decision) Summary {
	s := Summary{Decisions: len(ds)}
	if len(ds) == 0 {
		return s
	}
	var sum float64
	for _, d := range ds {
		sum += d.Regret
		if d.Regret > BlunderThreshold {
			s.Blunders++
		}
		if d.Regret <= 1e-9 {
			s.Perfect++
		}
	}
	s.MeanRegret = sum / float64(len(ds))
	s.Quality = 1 - s.MeanRegret

	// Sample standard deviation (n-1): with a single decision the spread is unknown, and
	// reporting 0 there would claim certainty from one observation.
	if len(ds) > 1 {
		var ss float64
		for _, d := range ds {
			diff := d.Regret - s.MeanRegret
			ss += diff * diff
		}
		s.StdErr = math.Sqrt(ss/float64(len(ds)-1)) / math.Sqrt(float64(len(ds)))
	}
	return s
}
