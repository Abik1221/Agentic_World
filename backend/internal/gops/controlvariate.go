package gops

// Control variates: removing the opponent's dice from the score.
//
// # What variance is left, and which part we can honestly remove
//
// Duplicate scheduling removes the variance from WHICH board was dealt. What remains inside a
// board is two things: our agent's own stochasticity, and the opponent's. Those are not equally
// tractable, and conflating them is how a variance-reduction technique turns into a bias.
//
// AIVAT (Burch, Schmid, Moravčík & Bowling) corrects both, because in poker research the agent
// being evaluated is a bot whose policy you hold. Ours is a black-box language model at a
// temperature nobody published. Correcting OUR actions would require a fitted policy, and a
// control variate built on a wrong policy does not have mean zero — it silently biases the very
// number it was added to tighten. That trade is not worth making on a figure we intend to
// publish.
//
// The opponent is different. In a certification run the opponent is the reference policy or the
// prober mixture: we wrote it, we know its distribution exactly, and its randomisation is pure
// nuisance. So this corrects the opponent's dice and nothing else.
//
// # Why it is exactly unbiased
//
// For a decision at node n where we played a and the opponent played b, define
//
//	W(n,a,b) = swing(a,b) + V(next(n,a,b))      the realised continuation value
//	Q(n,a)   = sum_b P_opp(b) * W(n,a,b)        its expectation over the opponent's mixture
//	C_t      = W(n,a,b) - Q(n,a)
//
// Conditional on the history and on our own action a, the opponent's b is drawn from a
// distribution we know, so E[C_t] = 0 exactly — not approximately, and with no assumption about
// our own policy. The corrected score U - sum_t C_t therefore has the same expectation as U and
// strictly less variance whenever the opponent actually randomises.
//
// The independence from our policy is the point. Whatever the model does, however it mixes,
// however its temperature drifts between runs, this correction stays unbiased.
//
// # What it cannot do
//
// Against a deterministic opponent every C_t is zero and the correction buys nothing, correctly:
// there was no opponent dice to remove. It also does nothing about our own agent's variance,
// which is the larger share once the opponent is a fixed reference. Removing that honestly needs
// the model's own action distribution, and estimating one from logs reintroduces exactly the bias
// this design refuses.

import "fmt"

// Step is one played round from the certified seat's point of view.
type Step struct {
	Node    Node
	MyCard  int
	OppCard int
}

// ControlVariate returns sum_t C_t for one match, and the number of rounds it covered.
//
// Subtract it from the match's realised point differential to obtain the corrected score. memo
// may be nil; pass the same map across matches in a batch to share the solve.
func ControlVariate(cfg Config, opp Policy, steps []Step, memo map[Node]float64) (float64, int, error) {
	if err := cfg.Validate(); err != nil {
		return 0, 0, err
	}
	if memo == nil {
		memo = make(map[Node]float64)
	}
	total, counted := 0.0, 0
	for i, s := range steps {
		if s.Node.Me == 0 || s.Node.Opp == 0 {
			return 0, 0, fmt.Errorf("gops: step %d is at a terminal node", i)
		}
		if s.Node.Me&(1<<(s.MyCard-1)) == 0 {
			return 0, 0, fmt.Errorf("gops: step %d played card %d which is not in hand", i, s.MyCard)
		}
		if s.Node.Opp&(1<<(s.OppCard-1)) == 0 {
			return 0, 0, fmt.Errorf("gops: step %d opponent played %d which is not in its hand",
				i, s.OppCard)
		}
		pot := cfg.Pot(s.Node)
		mix := opp.Step(cfg, Node{Me: s.Node.Opp, Opp: s.Node.Me, Carry: s.Node.Carry})

		// Realised continuation value for the pair actually played.
		realised := continuation(cfg, opp, s.Node, s.MyCard, s.OppCard, pot, memo)

		// Its expectation over the opponent's known mixture, holding our action fixed.
		expected := 0.0
		for b := 1; b <= cfg.N; b++ {
			pr := mix[b-1]
			if pr == 0 {
				continue
			}
			expected += pr * continuation(cfg, opp, s.Node, s.MyCard, b, pot, memo)
		}
		total += realised - expected
		counted++
	}
	return total, counted, nil
}

// continuation is swing(a,b) plus the value of the node that follows.
func continuation(cfg Config, opp Policy, n Node, a, b, pot int, memo map[Node]float64) float64 {
	swing, carry := resolve(a, b, pot)
	next := Node{
		Me:    n.Me &^ (1 << (a - 1)),
		Opp:   n.Opp &^ (1 << (b - 1)),
		Carry: carry,
	}
	return float64(swing) + solve(cfg, opp, next, memo, map[Node]int{})
}

// Corrected applies the control variate to a realised differential.
//
// Returned separately from ControlVariate so a caller can log both the raw and the corrected
// score. Publishing only the corrected one would hide how much of a result came from the
// estimator rather than from play, and that ratio is exactly what a sceptical reader should be
// able to check.
func Corrected(realised, controlVariate float64) float64 { return realised - controlVariate }
