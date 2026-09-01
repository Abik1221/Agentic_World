package gops

// Exact per-decision value loss.
//
// # Why this exists — the information argument
//
// A benchmark that scores a MATCH extracts one number from a whole game. Thirteen rounds of play
// collapse to win or lose, and a model that played well and lost a coin-flip is recorded
// identically to one that blundered every round. That is the reason our 18-match run produced
// separability 0.00 and reversed four conclusions when five matches were added: the estimator was
// starved, not the models.
//
// The solver already knows better. `solve` computes the expected value of EVERY legal action at
// every node and then discards all but the argmax. Keeping them turns one bit per match into one
// exactly-known real number per DECISION — four per match at n=5, twelve at n=13 — with no extra
// play and no extra spend. It is the cheapest available increase in statistical power by a wide
// margin, and unlike more matches it does not cost anything.
//
// # What the number means, stated precisely
//
// Loss(n, a) = max_b Q(n, b) - Q(n, a), where Q is the expected point differential from node n
// after playing a, assuming the REFERENCE OPPONENT plays `opp` and the decider plays optimally
// thereafter.
//
// Two things this deliberately is NOT:
//
//   - It is not distance from Nash equilibrium. Goofspiel is simultaneous-move, so each node is a
//     matrix game and an equilibrium would need a linear program per node. This is a best-response
//     value against a NAMED opponent, which is exact and cheap; calling it "distance from optimal
//     play" would be an overclaim, and the opponent must be published with any figure derived here.
//   - It is not a claim that a zero-loss move is the only good one. Ties are common and a loss of
//     exactly 0 means the action was AMONG the best replies, not that it was unique.
//
// # Why the memo is shared across decisions
//
// Scoring decisions one at a time re-solves the subtree under each node from scratch. Sharing one
// memo across a whole batch makes the cost of scoring an entire match barely more than scoring its
// first decision, which is what makes per-decision scoring practical to run over a season of logs
// rather than only on a handful of hand-picked hands.

import "fmt"

// Decision is one logged move to be scored.
type Decision struct {
	Node   Node
	Chosen int
}

// DecisionLoss is the exact value given up by one move.
type DecisionLoss struct {
	Node   Node
	Chosen int
	// Best is one action attaining the maximum. Ties are broken by the lowest card, so the
	// field is reproducible; it is not a claim that the others were worse.
	Best        int
	ChosenValue float64
	BestValue   float64
	// Loss is BestValue - ChosenValue, always >= 0. Zero means the move was among the best
	// replies at this node.
	Loss float64
	// Legal reports whether Chosen was actually available. An illegal move has no Q-value, so
	// it is reported rather than scored — silently treating it as a maximal loss would let a
	// logging bug masquerade as terrible play.
	Legal bool
}

// ActionValues returns Q(n, a) for every legal action at one node.
//
// memo may be nil; pass the same non-nil map across calls to share work.
func ActionValues(cfg Config, opp Policy, n Node, memo map[Node]float64) (map[int]float64, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if n.Me == 0 || n.Opp == 0 {
		return nil, fmt.Errorf("gops: no decision at a terminal node")
	}
	if memo == nil {
		memo = make(map[Node]float64)
	}
	return actionValues(cfg, opp, n, memo), nil
}

// actionValues mirrors the inner loop of solve, keeping every branch rather than the max.
//
// It deliberately re-derives the per-action expectations instead of calling solve and reading its
// br map: br records only the argmax, so a caller could not tell a decision that gave up 0.01 from
// one that gave up the whole pot — which is the entire quantity of interest here.
func actionValues(cfg Config, opp Policy, n Node, memo map[Node]float64) map[int]float64 {
	pot := cfg.Pot(n)
	oppMix := opp.Step(cfg, Node{Me: n.Opp, Opp: n.Me, Carry: n.Carry})

	out := make(map[int]float64)
	for mine := 1; mine <= cfg.N; mine++ {
		if n.Me&(1<<(mine-1)) == 0 {
			continue
		}
		ev := 0.0
		for theirs := 1; theirs <= cfg.N; theirs++ {
			pr := oppMix[theirs-1]
			if pr == 0 {
				continue
			}
			swing, carry := resolve(mine, theirs, pot)
			next := Node{
				Me:    n.Me &^ (1 << (mine - 1)),
				Opp:   n.Opp &^ (1 << (theirs - 1)),
				Carry: carry,
			}
			ev += pr * (float64(swing) + solve(cfg, opp, next, memo, map[Node]int{}))
		}
		out[mine] = ev
	}
	return out
}

// ScoreDecisions scores a batch against one reference opponent, sharing a single memo.
//
// Order is preserved so a caller can zip the result back onto its own records.
func ScoreDecisions(cfg Config, opp Policy, ds []Decision) ([]DecisionLoss, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	memo := make(map[Node]float64)
	out := make([]DecisionLoss, 0, len(ds))
	for _, d := range ds {
		if d.Node.Me == 0 || d.Node.Opp == 0 {
			return nil, fmt.Errorf("gops: decision at a terminal node")
		}
		q := actionValues(cfg, opp, d.Node, memo)
		dl := DecisionLoss{Node: d.Node, Chosen: d.Chosen}

		bestCard, bestVal := 0, 0.0
		for card := 1; card <= cfg.N; card++ {
			v, ok := q[card]
			if !ok {
				continue
			}
			// Lowest card wins a tie, so the reported Best is stable across runs.
			if bestCard == 0 || v > bestVal {
				bestCard, bestVal = card, v
			}
		}
		dl.Best, dl.BestValue = bestCard, bestVal

		chosen, ok := q[d.Chosen]
		if !ok {
			// Illegal or absent. Reported, never scored: a maximal loss here would turn a
			// logging bug into a headline about a model playing terribly.
			dl.Legal = false
			out = append(out, dl)
			continue
		}
		dl.Legal = true
		dl.ChosenValue = chosen
		dl.Loss = bestVal - chosen
		if dl.Loss < 0 {
			// Floating point only; the chosen action cannot beat the maximum by construction.
			dl.Loss = 0
		}
		out = append(out, dl)
	}
	return out, nil
}

// TotalLoss sums the loss over the scored decisions that were legal, and reports how many were
// counted.
//
// Illegal decisions are excluded from BOTH the sum and the count rather than counted as zero:
// averaging them in as perfect play would make a broken log look like a flawless agent.
func TotalLoss(ls []DecisionLoss) (total float64, counted int) {
	for _, l := range ls {
		if !l.Legal {
			continue
		}
		total += l.Loss
		counted++
	}
	return total, counted
}
