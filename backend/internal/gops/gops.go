// Package gops is an exact solver for small-n Goofspiel (GOPS).
//
// # Why this exists
//
// Every published LLM game benchmark — GTBench, GAMA-Bench, MAgIC, TMGBench, LLMsPark —
// reports win rate against a pool of other models. That number is POOL-RELATIVE: it moves
// when the pool changes, it can be farmed by choosing opponents, it saturates, and it has no
// absolute meaning. Two labs running the same benchmark a year apart cannot compare results.
//
// Game theory offers the alternative nobody is using at scale: an opponent-independent
// absolute yardstick. For a two-player zero-sum game, the exploitability of a strategy is
//
//	eps(sigma) = max_{sigma'} u(sigma', sigma) - v
//
// Zero means unexploitable. It does not move when the pool changes, and it cannot be farmed
// by choosing opponents. (That is a property of the DEFINITION. The measurement built on it in
// internal/exploit is farmable as currently configured — see the gameability note there and
// labdriver.TestProberCanBeMemorized. Do not read this paragraph as a claim about the
// published number.) It
// has a known floor, so "distance from optimal" means something, unlike a saturating percent.
//
// Goofspiel is the reason we can do this and others cannot: it is a finite, zero-sum,
// perfect-recall game that the platform ALREADY RUNS, and `Config.Cards` is a config field
// (engine/goofspiel/state.go:57-63), so a solver-tractable small-n variant costs one line
// rather than a new game.
//
// # The certified ladder is OPEN-prize Goofspiel
//
// The engine supports two fairness modes. Under FairnessShuffled the prize order is hidden,
// which makes the information set include the whole revealed-prize history and blows up the
// state space. Under FairnessOpen the prize order is public, and then the ONLY imperfect
// information in the game is the simultaneous bid — both hands are public and the engine
// already ships the opponent's remaining hand in the turn view (match/view.go:208).
//
// That reduction is what makes an EXACT solve cheap: the game becomes a finite sequence of
// simultaneous-move nodes over public state. We pin the certified ladder to FairnessOpen and
// small n, and we say so in the published spec. Shuffled-prize solving is future work and is
// deliberately NOT claimed.
//
// # The value of this game is exactly 0, and that is not an approximation
//
// Both seats start with an identical hand {1..n}, face the same prize sequence, and the tie
// rule treats them alike. The game is therefore SYMMETRIC under exchanging the two players.
// A symmetric two-player zero-sum game has value 0: if the value were v > 0 for player 1,
// the same strategy played by player 2 would guarantee v > 0 for player 2 as well, and
// v + v > 0 contradicts zero-sum.
//
// This matters more than it looks. It means computing exploitability needs NO linear program
// and NO equilibrium solve:
//
//	eps(sigma) = max_{sigma'} u(sigma', sigma) - 0 = u(BR(sigma), sigma)
//
// and a best response to a FIXED opponent policy is pure dynamic programming. The expensive,
// error-prone half of the standard pipeline disappears. TestValueIsZeroBySymmetry pins the
// symmetry the argument rests on, so a future change to the tie rule that breaks it fails the
// build rather than silently invalidating every published number.
//
// # Purity
//
// Same discipline as internal/skill and the engines: no clock, no RNG, no I/O, no ambient
// state. Same inputs, same output, forever. A published exploitability figure that cannot be
// recomputed bit-for-bit next year is not evidence.
package gops

import (
	"fmt"
	"math/bits"
)

// MaxCards is the largest deck this solver accepts.
//
// The bound is honesty, not arithmetic. Hands are uint16 bitmasks so 13 would fit, but the
// memo table is keyed on (myHand, oppHand, carry) and the reachable state count grows as
// C(n, k)^2 summed over k. n=6 is ~50k states and solves in milliseconds; n=8 is ~10M and
// starts to hurt; n=13 is not reachable on any machine we own. Refusing loudly at the
// boundary is better than a certified number that took an hour and nobody re-checks.
const MaxCards = 8

// TieRule selects what happens to a round whose bids are equal.
//
// Only Carry is implemented, because it is the engine's default (engine/goofspiel/state.go:34-46)
// and it is the rule the certified ladder pins. Split and Discard are named here so a caller
// passing one gets an explicit error instead of silently receiving Carry semantics under a
// different label — a wrong number that looks right is the failure mode this package exists
// to prevent.
type TieRule int

const (
	// TieCarry rolls a tied pot forward onto the next round's prize. Engine default.
	TieCarry TieRule = iota
	// TieSplit halves the pot between the seats. NOT IMPLEMENTED.
	TieSplit
	// TieDiscard throws a tied pot away. NOT IMPLEMENTED.
	TieDiscard
)

// Config pins one exact instance of the certified ladder.
//
// Order is the PUBLIC prize sequence (FairnessOpen). It must be a permutation of 1..N; a
// permutation is required rather than an arbitrary sequence because the symmetry argument
// that fixes the game value at 0 does not depend on it, but the published spec does — two
// runs of "the n=5 ladder" have to mean the same game.
type Config struct {
	N     int
	Order []int
	Tie   TieRule
}

// Validate reports why a Config cannot be solved, or nil.
//
// Called by every entry point rather than trusted from the caller. A malformed spec that
// solves anyway would produce a number that is precise, reproducible, and about a different
// game than the one we published.
func (c Config) Validate() error {
	if c.N < 2 {
		return fmt.Errorf("gops: N must be >= 2, got %d", c.N)
	}
	if c.N > MaxCards {
		return fmt.Errorf("gops: N must be <= %d, got %d (see MaxCards)", MaxCards, c.N)
	}
	if c.Tie != TieCarry {
		return fmt.Errorf("gops: only TieCarry is implemented, got %v", c.Tie)
	}
	if len(c.Order) != c.N {
		return fmt.Errorf("gops: Order has %d entries, want N=%d", len(c.Order), c.N)
	}
	seen := make([]bool, c.N+1)
	for _, p := range c.Order {
		if p < 1 || p > c.N {
			return fmt.Errorf("gops: Order entry %d outside 1..%d", p, c.N)
		}
		if seen[p] {
			return fmt.Errorf("gops: Order repeats prize %d; must be a permutation of 1..%d", p, c.N)
		}
		seen[p] = true
	}
	return nil
}

// FullHand is the opening hand {1..N} as a bitmask, bit (c-1) set for card c.
func (c Config) FullHand() uint16 {
	var h uint16
	for card := 1; card <= c.N; card++ {
		h |= 1 << (card - 1)
	}
	return h
}

// Node identifies one decision point.
//
// Under FairnessOpen both hands are public and the prize order is known, so the round index
// is implied by the hands (popcount) and the whole information set is exactly this triple.
// That is the property that makes the empirical policy estimable from logged play: the
// platform already stores the turn view per decision (migrations/0074) and
// skill/goofspiel_view.go:49-114 reconstructs opponent hand and remaining prizes EXACTLY —
// not estimated — so every logged decision maps to a Node with no inference.
//
// Me is the hand of the player whose turn-decision this node describes; Opp is the other.
// Carry is the pot rolled forward from tied rounds.
type Node struct {
	Me    uint16
	Opp   uint16
	Carry int
}

// Round is how many rounds have already been played at this node.
func (c Config) Round(n Node) int { return c.N - bits.OnesCount16(n.Me) }

// Pot is the total at stake this round: the round's prize plus anything carried.
func (c Config) Pot(n Node) int {
	r := c.Round(n)
	if r >= c.N {
		return 0
	}
	return c.Order[r] + n.Carry
}

// Policy is a behavioural strategy: for each node, a distribution over the cards in that
// node's Me hand.
//
// Weights need not be normalised — Step normalises on read. Unvisited nodes and all-zero
// distributions fall back to uniform over the legal cards, which is the correct treatment
// for a best-response computation: an opponent we have never observed at a node is one we
// cannot claim to exploit there, and assuming uniform is the choice that neither invents an
// exploit nor pretends the node is unreachable.
type Policy map[Node][]float64

// Step returns the normalised probability that the policy plays each card at n.
//
// The returned slice is indexed by card-1 and is zero for cards not in n.Me. Cards outside
// the hand are zeroed rather than trusted from the input: a policy estimated from logged
// play can carry a stray count for an illegal card if the log is dirty, and silently letting
// that mass through would move a best response toward an action the opponent could never
// have taken.
func (p Policy) Step(cfg Config, n Node) []float64 {
	out := make([]float64, cfg.N)
	raw, ok := p[n]
	total := 0.0
	if ok {
		for card := 1; card <= cfg.N; card++ {
			if n.Me&(1<<(card-1)) == 0 || card-1 >= len(raw) {
				continue
			}
			if w := raw[card-1]; w > 0 {
				out[card-1] = w
				total += w
			}
		}
	}
	if total == 0 {
		legal := bits.OnesCount16(n.Me)
		if legal == 0 {
			return out
		}
		u := 1.0 / float64(legal)
		for card := 1; card <= cfg.N; card++ {
			if n.Me&(1<<(card-1)) != 0 {
				out[card-1] = u
			}
		}
		return out
	}
	for i := range out {
		out[i] /= total
	}
	return out
}

// resolve returns the point swing to the bidder of `mine` and the carry passed forward.
func resolve(mine, theirs, pot int) (swing, carry int) {
	switch {
	case mine > theirs:
		return pot, 0
	case mine < theirs:
		return -pot, 0
	default:
		return 0, pot
	}
}

// BestResponse computes an exact best response to a fixed opponent policy.
//
// value is the expected point differential the best responder earns against opp, from the
// perspective of the seat holding root.Me. Because the game value is 0 (see package doc),
// that value IS the exploitability of opp — no equilibrium solve, no linear program.
//
// br maps every node the best responder can reach to the card it should play. It is a PURE
// strategy, which is not a limitation: a best response to a fixed opponent always has a pure
// maximiser at every node, since the opponent's mixing is already integrated out.
//
// Ties between equally-good cards are broken toward the LOWEST card, deterministically. An
// arbitrary tie-break would make the returned policy depend on map iteration order, and a
// prober whose behaviour changes between runs cannot be used to certify anything.
func BestResponse(cfg Config, opp Policy) (value float64, br map[Node]int, err error) {
	if err := cfg.Validate(); err != nil {
		return 0, nil, err
	}
	br = make(map[Node]int)
	memo := make(map[Node]float64)
	root := Node{Me: cfg.FullHand(), Opp: cfg.FullHand(), Carry: 0}
	value = solve(cfg, opp, root, memo, br)
	return value, br, nil
}

// solve is the backward induction. memo is keyed on the node because, under FairnessOpen,
// the node IS the whole information set — nothing about the path to it changes the value of
// what remains.
func solve(cfg Config, opp Policy, n Node, memo map[Node]float64, br map[Node]int) float64 {
	if n.Me == 0 {
		// Hands exhausted. Any pot still carried is lost under TieCarry, which is the
		// engine's behaviour and is deliberately NOT credited to either seat here.
		return 0
	}
	if v, ok := memo[n]; ok {
		return v
	}
	pot := cfg.Pot(n)
	// The opponent decides at the mirrored node: its own hand is Me from its point of view.
	oppMix := opp.Step(cfg, Node{Me: n.Opp, Opp: n.Me, Carry: n.Carry})

	best, bestCard := 0.0, 0
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
			ev += pr * (float64(swing) + solve(cfg, opp, next, memo, br))
		}
		if bestCard == 0 || ev > best {
			best, bestCard = ev, mine
		}
	}
	memo[n] = best
	br[n] = bestCard
	return best
}

// Evaluate returns the expected point differential when `me` plays against `opp`.
//
// Both are behavioural strategies, so this integrates over both mixtures exactly. It is the
// ground truth that TestBestResponseIsOptimal checks BestResponse against, and it is how a
// caller scores a candidate prober offline before paying to run it live.
func Evaluate(cfg Config, me, opp Policy) (float64, error) {
	if err := cfg.Validate(); err != nil {
		return 0, err
	}
	memo := make(map[Node]float64)
	root := Node{Me: cfg.FullHand(), Opp: cfg.FullHand(), Carry: 0}
	return eval(cfg, me, opp, root, memo), nil
}

func eval(cfg Config, me, opp Policy, n Node, memo map[Node]float64) float64 {
	if n.Me == 0 {
		return 0
	}
	if v, ok := memo[n]; ok {
		return v
	}
	pot := cfg.Pot(n)
	myMix := me.Step(cfg, n)
	oppMix := opp.Step(cfg, Node{Me: n.Opp, Opp: n.Me, Carry: n.Carry})

	total := 0.0
	for mine := 1; mine <= cfg.N; mine++ {
		pm := myMix[mine-1]
		if pm == 0 {
			continue
		}
		for theirs := 1; theirs <= cfg.N; theirs++ {
			po := oppMix[theirs-1]
			if po == 0 {
				continue
			}
			swing, carry := resolve(mine, theirs, pot)
			next := Node{
				Me:    n.Me &^ (1 << (mine - 1)),
				Opp:   n.Opp &^ (1 << (theirs - 1)),
				Carry: carry,
			}
			total += pm * po * (float64(swing) + eval(cfg, me, opp, next, memo))
		}
	}
	memo[n] = total
	return total
}

// Uniform is the level-0 reference opponent: uniform over the legal cards at every node.
//
// It is the natural fit prior and the natural sanity check — an agent that cannot beat
// Uniform is not playing the game, and a prober that cannot beat Uniform is broken.
func Uniform() Policy { return Policy{} }

// PureFrom lifts a deterministic card-per-node map (such as the one BestResponse returns)
// into a Policy, so a best response can be scored by Evaluate or used as an opponent in a
// later round of certification.
func PureFrom(cfg Config, moves map[Node]int) Policy {
	p := make(Policy, len(moves))
	for n, card := range moves {
		if card < 1 || card > cfg.N || n.Me&(1<<(card-1)) == 0 {
			continue // never let an illegal move become a policy
		}
		w := make([]float64, cfg.N)
		w[card-1] = 1
		p[n] = w
	}
	return p
}
