package skill

import (
	"fmt"
	"sort"
)

// Goofspiel decision scoring.
//
// # The game, stated as game theory
//
// Goofspiel (GOPS) is a finite two-player ZERO-SUM game. Each round a prize is revealed
// and both players bid a card from their hand SIMULTANEOUSLY; the higher bid takes the
// prize, a tie carries the pool into the next round. Both hands are public — the only
// hidden information is the current simultaneous choice.
//
// That structure is what makes rigorous scoring possible. A single round, with both hands
// known, is a finite two-player zero-sum MATRIX GAME. By von Neumann's minimax theorem it
// has a value and an optimal mixed strategy, and the value is computable exactly. So
// "what was this decision worth" is a well-posed question with a real answer, not a
// heuristic judgement.
//
// # Why the round is not scored in isolation
//
// Solving only the current round would score "play the 13 to win a 2-point prize" as
// fine — locally it wins. The whole strategic tension in Goofspiel is that a card spent
// now is a card unavailable for the prizes still to come, so a scorer that ignores
// continuation value measures the wrong game.
//
// Each action is therefore valued as immediate payoff PLUS the continuation value of the
// hands it leaves behind:
//
//	EV(a) = Σ_b π(b) · [ payoff(a, b) + Φ(H_me∖a, H_opp∖b) ]
//
// π is the opponent's reference policy and Φ is the continuation potential below.
//
// # The continuation potential Φ
//
// Exact backward induction is intractable: 13! prize orders crossed with every pair of
// hand subsets. Φ instead uses the game's own structure. Goofspiel is a Blotto-like
// contest — you are allocating a fixed budget of bidding power across a fixed set of
// contests — and in the continuous relaxation of such a contest the expected share of
// the remaining pool is the share of remaining bidding power:
//
//	Φ(H_me, H_opp) = R · Σ(H_me) / (Σ(H_me) + Σ(H_opp))
//
// where R is the total prize value still to be contested. This is exact in the continuous
// relaxation and a well-motivated approximation in the discrete game. Crucially it is
// SYMMETRIC and ZERO-SUM-CONSISTENT: equal hands split the remaining pool exactly evenly,
// so the scorer cannot manufacture value out of nothing. TestPotentialIsZeroSum pins that.
//
// # Why regret, not "did you win the round"
//
// Winning a round can be luck; giving up available expected value cannot. Regret is
// measured against the BEST alternative from the identical state, so it is
// opponent-independent: two agents that faced the same state are comparable even though
// they never played each other, and an agent is never punished for its opponent drawing
// well.

// GoofspielState is everything needed to score one bid. It maps directly onto the view
// the agent was handed, which is persisted per decision — so scoring runs offline over
// history and needs no live match.
type GoofspielState struct {
	Round int
	// Prize is the value contested this round, including any pool carried by ties.
	Prize int
	// MyHand / OppHand are the cards each side still holds, this round's card included.
	MyHand  []int
	OppHand []int
	// RemainingPrizes are the prize values still to be revealed AFTER this round. Their
	// sum is R in Φ. Empty on the final round, which correctly collapses Φ to zero: with
	// nothing left to contest, only the immediate payoff matters.
	RemainingPrizes []int
}

// ScoreGoofspielBid scores one bid.
//
// Returns ok=false when the state cannot be scored (no cards, or the played card was not
// in hand). A decision that cannot be scored is EXCLUDED rather than scored zero: a
// scorer that silently invents a value for malformed input corrupts the mean it feeds.
func ScoreGoofspielBid(st GoofspielState, played int) (Decision, bool) {
	if len(st.MyHand) == 0 || len(st.OppHand) == 0 {
		return Decision{}, false
	}
	if !containsInt(st.MyHand, played) {
		return Decision{}, false
	}

	// The opponent's reference policy. Solved, not assumed — see referencePolicy.
	pi := referencePolicy(st)

	remaining := 0
	for _, p := range st.RemainingPrizes {
		remaining += p
	}

	type scored struct {
		card int
		ev   float64
	}
	evs := make([]scored, 0, len(st.MyHand))
	for _, a := range st.MyHand {
		evs = append(evs, scored{card: a, ev: expectedValue(st, a, pi, remaining)})
	}
	// Sort by EV desc, then by card asc, so ties resolve identically on every run. A
	// scorer whose output depends on map iteration order is not reproducible, and
	// reproducibility is the entire premise of this package.
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].ev != evs[j].ev {
			return evs[i].ev > evs[j].ev
		}
		return evs[i].card < evs[j].card
	})

	best, worst := evs[0], evs[len(evs)-1]
	var chosen float64
	for _, e := range evs {
		if e.card == played {
			chosen = e.ev
			break
		}
	}

	d := Decision{
		Round:       st.Round,
		Chosen:      fmt.Sprintf("%d", played),
		Best:        fmt.Sprintf("%d", best.card),
		ValueChosen: chosen,
		ValueBest:   best.ev,
		ValueWorst:  worst.ev,
		Regret:      normalize(chosen, best.ev, worst.ev),
	}
	switch {
	case d.Regret <= 1e-9:
		d.Why = fmt.Sprintf("best available bid for the %d-point prize", st.Prize)
	case d.Regret > BlunderThreshold:
		d.Why = fmt.Sprintf("bid %d on the %d-point prize; %d was worth %.2f more",
			played, st.Prize, best.card, best.ev-chosen)
	default:
		d.Why = fmt.Sprintf("bid %d; %d was slightly better (+%.2f)", played, best.card, best.ev-chosen)
	}
	return d, true
}

// expectedValue is EV(a) = Σ_b π(b)·[payoff(a,b) + Φ(hands after)].
func expectedValue(st GoofspielState, a int, pi map[int]float64, remaining int) float64 {
	myAfter := removeInt(st.MyHand, a)
	var ev float64
	for _, b := range st.OppHand {
		p := pi[b]
		if p == 0 {
			continue
		}
		oppAfter := removeInt(st.OppHand, b)

		var immediate float64
		var carry int
		switch {
		case a > b:
			immediate = float64(st.Prize)
		case a < b:
			immediate = 0
		default:
			// A tie carries the whole pool into the next round rather than splitting it.
			// Modelled as such — treating a tie as half the prize would misprice exactly
			// the bids agents most often get wrong.
			carry = st.Prize
		}
		ev += p * (immediate + potential(myAfter, oppAfter, remaining+carry))
	}
	return ev
}

// potential is Φ: the share of the remaining pool this pair of hands is worth.
//
// Zero-sum by construction — Φ(A,B) + Φ(B,A) = R exactly — so no action can be valued
// above the total prize actually available.
func potential(myHand, oppHand []int, remaining int) float64 {
	if remaining <= 0 {
		return 0
	}
	mine, theirs := sumInts(myHand), sumInts(oppHand)
	total := mine + theirs
	if total == 0 {
		// Both hands empty with prizes outstanding cannot occur in a legal game; split
		// evenly rather than dividing by zero.
		return float64(remaining) / 2
	}
	return float64(remaining) * float64(mine) / float64(total)
}

// referencePolicy is the opponent model each action is scored against.
//
// Solved by REGRET MATCHING (Hart & Mas-Colell 2000) on the current round's payoff
// matrix. In a two-player zero-sum game the empirical average of regret-matching play
// converges to a Nash equilibrium — it is the algorithm at the core of CFR, the method
// behind the superhuman poker agents. So the opponent model here is not a guess about
// how opponents behave; it is an approximation of how a rational opponent WOULD behave,
// which is the only opponent model that cannot be farmed.
//
// The alternative — scoring against the opponent's actual card — would be hindsight
// regret: it would reward an agent for guessing what was unknowable at decision time and
// punish correct play that ran into a good card. That measures luck.
//
// Fixed iteration count, no clock, no RNG: identical input yields an identical policy on
// every machine forever.
func referencePolicy(st GoofspielState) map[int]float64 {
	const iterations = 400

	myCards, oppCards := st.MyHand, st.OppHand
	n, m := len(myCards), len(oppCards)

	// Immediate payoff to the ROW player (me). The continuation term is deliberately
	// excluded from the matrix the opponent is solved against: Φ depends on both hands
	// after the move, so folding it in here would make the matrix depend on the very
	// policy being solved for. The one-round matrix is the right object to solve; the
	// continuation is applied afterwards in expectedValue.
	payoff := make([][]float64, n)
	for i, a := range myCards {
		payoff[i] = make([]float64, m)
		for j, b := range oppCards {
			switch {
			case a > b:
				payoff[i][j] = float64(st.Prize)
			case a < b:
				payoff[i][j] = -float64(st.Prize)
			default:
				payoff[i][j] = 0
			}
		}
	}

	rowRegret := make([]float64, n)
	colRegret := make([]float64, m)
	rowSum := make([]float64, n)
	colSum := make([]float64, m)

	for t := 0; t < iterations; t++ {
		rowP := matchRegret(rowRegret)
		colP := matchRegret(colRegret)
		for i := range rowSum {
			rowSum[i] += rowP[i]
		}
		for j := range colSum {
			colSum[j] += colP[j]
		}

		// Counterfactual value of each pure action against the other side's current mix.
		for i := 0; i < n; i++ {
			var u float64
			for j := 0; j < m; j++ {
				u += colP[j] * payoff[i][j]
			}
			rowRegret[i] += u
		}
		var rowVal float64
		for i := 0; i < n; i++ {
			for j := 0; j < m; j++ {
				rowVal += rowP[i] * colP[j] * payoff[i][j]
			}
		}
		for i := range rowRegret {
			rowRegret[i] -= rowVal
		}
		// The column player's payoff is the negation — the game is zero-sum.
		for j := 0; j < m; j++ {
			var u float64
			for i := 0; i < n; i++ {
				u += rowP[i] * -payoff[i][j]
			}
			colRegret[j] += u + rowVal
		}
	}

	// The equilibrium approximation is the AVERAGE strategy, not the final one. Only the
	// average converges (Hart & Mas-Colell); the current iterate keeps oscillating.
	out := make(map[int]float64, m)
	var total float64
	for _, v := range colSum {
		total += v
	}
	if total <= 0 {
		for _, b := range oppCards {
			out[b] = 1 / float64(m)
		}
		return out
	}
	for j, b := range oppCards {
		out[b] = colSum[j] / total
	}
	return out
}

// matchRegret turns a regret vector into a mixed strategy: probability proportional to
// positive regret, uniform when no action has any.
func matchRegret(regret []float64) []float64 {
	out := make([]float64, len(regret))
	var total float64
	for i, r := range regret {
		if r > 0 {
			out[i] = r
			total += r
		}
	}
	if total <= 0 {
		for i := range out {
			out[i] = 1 / float64(len(out))
		}
		return out
	}
	for i := range out {
		out[i] /= total
	}
	return out
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func removeInt(xs []int, v int) []int {
	out := make([]int, 0, len(xs))
	dropped := false
	for _, x := range xs {
		if x == v && !dropped {
			dropped = true
			continue
		}
		out = append(out, x)
	}
	return out
}

func sumInts(xs []int) int {
	s := 0
	for _, x := range xs {
		s += x
	}
	return s
}
