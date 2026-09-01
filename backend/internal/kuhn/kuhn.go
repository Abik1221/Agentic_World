// Package kuhn is an exact solver for Kuhn poker — the second solved game on the certified
// ladder, and the one that turns "a result about Goofspiel" into "a method".
//
// # Why a second game, and why this one
//
// The strongest remaining objection to the ladder is construct validity: a certified number on
// one small card game licenses no claim about strategic reasoning in general. GameBench's own
// published self-critique is that its aggregate was "not robust to a single game", and the
// ladder was a single game.
//
// Kuhn is the right second game because it differs from Goofspiel on the axes that matter,
// not on cosmetics:
//
//   - PRIVATE INFORMATION. Goofspiel-open hides only the simultaneous bid; both hands are
//     public. Kuhn deals each seat a card nobody else sees, so the information set is
//     (my card, betting history) and beliefs about the opponent's card are the whole game.
//   - CHANCE. Goofspiel-open has none once the board is fixed. Kuhn's deal is a chance node,
//     so exploitability is an expectation over deals rather than a value on one line.
//   - NOT SYMMETRIC. Goofspiel with equal hands is symmetric, which is what let the game
//     value be exactly 0 and killed the need for an equilibrium solve. Kuhn's value is
//     -1/18 to the first player, so the ladder's arithmetic has to carry a real value
//     constant — see Exploitability.
//   - BLUFFING IS OPTIMAL. Kuhn's equilibria require betting the worst card with positive
//     probability. A model that never bluffs is exploitable by construction, which is a
//     strategic property no amount of Goofspiel measures.
//
// It is also the standard toy of the imperfect-information literature and is in OpenSpiel, so
// a reviewer can check our numbers against theirs.
//
// # Why brute force rather than CFR
//
// Each seat has six information sets with two actions apiece, so 64 pure strategies per seat.
// A best response is therefore an exact maximum over 64 evaluations — no regret matching, no
// reach-probability bookkeeping, no convergence threshold to argue about, and nothing that
// can be subtly wrong in a way tests would not catch. The whole solve is microseconds.
//
// # Purity
//
// Same discipline as internal/gops: no clock, no RNG, no I/O. A published exploitability
// figure that cannot be recomputed bit-for-bit is not evidence.
package kuhn

import (
	"fmt"
	"sort"
)

// Cards are 1 (Jack), 2 (Queen), 3 (King).
const (
	Jack  = 1
	Queen = 2
	King  = 3
)

// Seats.
const (
	P1 = 0
	P2 = 1
)

// GameValue is the equilibrium payoff to P1, in antes, per hand.
//
// Exactly -1/18: the first player is at a structural disadvantage in Kuhn because acting
// first leaks information. This is the classical result (Kuhn, 1950) and
// TestGameValueMatchesTheClassicalResult verifies it against this implementation rather than
// taking it on faith — a value constant that disagreed with the code would bias every
// exploitability number by a fixed offset that no other test would notice.
const GameValue = -1.0 / 18.0

// Histories. Each seat faces exactly two decision points, so the whole tree is five nodes.
const (
	HistStart = ""   // P1 to act: check or bet
	HistCheck = "c"  // P1 checked; P2 to act: check or bet
	HistBet   = "b"  // P1 bet; P2 to act: fold or call
	HistCB    = "cb" // P1 checked, P2 bet; P1 to act: fold or call
)

// Infoset is what a seat knows when it acts: its own card and the betting so far.
//
// Deliberately NOT the full state. The opponent's card is exactly what the seat does not know,
// and an information set that leaked it would make the game trivially solvable and the
// measurement meaningless.
type Infoset struct {
	Card int
	Hist string
}

// Policy gives, for each infoset, the probability of the AGGRESSIVE action — bet at HistStart
// and HistCheck, call at HistBet and HistCB.
//
// One number per infoset rather than a distribution, because every node in Kuhn is binary.
// That removes a whole class of normalisation bug: there is no way to write a policy whose
// probabilities fail to sum to one.
type Policy map[Infoset]float64

// Get returns the aggression probability, clamped to [0,1]. A missing infoset is 0 — always
// passive — which is a deliberate choice: it is the strategy a seat that never acts would
// play, and it is exploitable, so an incomplete policy is penalised rather than flattered.
func (p Policy) Get(i Infoset) float64 {
	v, ok := p[i]
	if !ok {
		return 0
	}
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// Infosets lists a seat's decision points, in a fixed order so pure-strategy enumeration and
// digests are reproducible.
func Infosets(seat int) []Infoset {
	var hists []string
	if seat == P1 {
		hists = []string{HistStart, HistCB}
	} else {
		hists = []string{HistCheck, HistBet}
	}
	out := make([]Infoset, 0, 6)
	for _, h := range hists {
		for card := Jack; card <= King; card++ {
			out = append(out, Infoset{Card: card, Hist: h})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Hist != out[b].Hist {
			return out[a].Hist < out[b].Hist
		}
		return out[a].Card < out[b].Card
	})
	return out
}

// deals enumerates the six equally likely deals of two distinct cards.
func deals() [][2]int {
	var out [][2]int
	for c1 := Jack; c1 <= King; c1++ {
		for c2 := Jack; c2 <= King; c2++ {
			if c1 != c2 {
				out = append(out, [2]int{c1, c2})
			}
		}
	}
	return out
}

// showdown returns +1 when P1's card wins, -1 otherwise. Cards are distinct so there are no
// ties.
func showdown(c1, c2 int) float64 {
	if c1 > c2 {
		return 1
	}
	return -1
}

// dealPayoff is the expected payoff to P1 on one deal, in antes.
//
// The tree, with each seat's ante already in the pot:
//
//	P1 bets  -> P2 folds     : P1 +1
//	         -> P2 calls     : showdown for 2
//	P1 checks-> P2 checks    : showdown for 1
//	         -> P2 bets      -> P1 folds : P1 -1
//	                         -> P1 calls : showdown for 2
func dealPayoff(c1, c2 int, p1, p2 Policy) float64 {
	pBet := p1.Get(Infoset{Card: c1, Hist: HistStart})
	pCall2 := p2.Get(Infoset{Card: c2, Hist: HistBet})
	vBet := (1-pCall2)*1 + pCall2*showdown(c1, c2)*2

	pBet2 := p2.Get(Infoset{Card: c2, Hist: HistCheck})
	pCall1 := p1.Get(Infoset{Card: c1, Hist: HistCB})
	vCheckBet := (1-pCall1)*(-1) + pCall1*showdown(c1, c2)*2
	vCheck := (1-pBet2)*showdown(c1, c2)*1 + pBet2*vCheckBet

	return pBet*vBet + (1-pBet)*vCheck
}

// Evaluate is the expected payoff to P1 when p1 plays P1 and p2 plays P2.
func Evaluate(p1, p2 Policy) float64 {
	d := deals()
	total := 0.0
	for _, x := range d {
		total += dealPayoff(x[0], x[1], p1, p2)
	}
	return total / float64(len(d))
}

// BestResponse computes an exact best response for `seat` against a fixed opponent policy.
//
// value is the best responder's own expected payoff — so for P2 it is already sign-flipped.
// Exhaustive over all 64 pure strategies: a best response to a fixed opponent always has a
// pure maximiser, since the opponent's mixing is integrated out before the choice is made.
//
// Ties break toward the LOWER strategy index, deterministically, so the returned policy does
// not depend on iteration order. A prober whose behaviour changes between runs cannot certify
// anything.
func BestResponse(seat int, opp Policy) (value float64, br Policy) {
	sets := Infosets(seat)
	n := len(sets)
	best := 0.0
	var bestPol Policy
	for mask := 0; mask < 1<<n; mask++ {
		pol := make(Policy, n)
		for i, is := range sets {
			if mask&(1<<i) != 0 {
				pol[is] = 1
			} else {
				pol[is] = 0
			}
		}
		var v float64
		if seat == P1 {
			v = Evaluate(pol, opp)
		} else {
			v = -Evaluate(opp, pol)
		}
		if bestPol == nil || v > best {
			best, bestPol = v, pol
		}
	}
	return best, bestPol
}

// Exploitability is how much more than the equilibrium value a best response extracts from
// this seat's strategy.
//
// Zero means unexploitable. Unlike Goofspiel the game is NOT symmetric, so the reference is
// the real game value rather than zero, and it differs by seat: P1's equilibrium value is
// GameValue and P2's is -GameValue. Getting that sign wrong would shift every number by
// 1/9 — small enough to look plausible and large enough to reorder a leaderboard, which is
// why TestExploitabilityOfEquilibriumIsZero pins both seats.
func Exploitability(seat int, pol Policy) float64 {
	other := P2
	refValue := -GameValue // what the OPPONENT earns at equilibrium
	if seat == P2 {
		other = P1
		refValue = GameValue
	}
	v, _ := BestResponse(other, pol)
	eps := v - refValue
	if eps < 0 {
		// Only reachable through floating-point noise: no strategy can hold a best response
		// below the equilibrium value. Clamping rather than publishing a negative keeps a
		// meaningless -1e-16 out of a certificate.
		return 0
	}
	return eps
}

// Equilibrium returns one member of Kuhn's equilibrium family, parameterised by alpha.
//
// The family is continuous in alpha over [0, 1/3]: P1 bluffs the Jack at alpha, value-bets
// the King at 3*alpha, and calls the Queen at alpha + 1/3. P2's strategy is fixed. Exposed
// because a reference opponent has to be a real equilibrium, and because a ladder that always
// probed with the same one would be measuring a narrower thing than it claims.
func Equilibrium(alpha float64) (Policy, Policy, error) {
	if alpha < 0 || alpha > 1.0/3.0+1e-12 {
		return nil, nil, fmt.Errorf("kuhn: alpha must be in [0, 1/3], got %v", alpha)
	}
	p1 := Policy{
		{Card: Jack, Hist: HistStart}:  alpha,
		{Card: Queen, Hist: HistStart}: 0,
		{Card: King, Hist: HistStart}:  3 * alpha,
		{Card: Jack, Hist: HistCB}:     0,
		{Card: Queen, Hist: HistCB}:    alpha + 1.0/3.0,
		{Card: King, Hist: HistCB}:     1,
	}
	p2 := Policy{
		{Card: Jack, Hist: HistCheck}:  1.0 / 3.0,
		{Card: Queen, Hist: HistCheck}: 0,
		{Card: King, Hist: HistCheck}:  1,
		{Card: Jack, Hist: HistBet}:    0,
		{Card: Queen, Hist: HistBet}:   1.0 / 3.0,
		{Card: King, Hist: HistBet}:    1,
	}
	return p1, p2, nil
}
