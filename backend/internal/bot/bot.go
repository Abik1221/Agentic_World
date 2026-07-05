// Package bot holds the Goofspiel strategies the platform itself can play: the
// offline simulator (cmd/arena-sim) and the live "house" agent that newly-
// registered developers practice against in sandbox mode both pull from here, so
// there is one source of truth and no copy-paste divergence.
//
// A Policy is pure in spirit — it reads the open game state and returns a card —
// but is allowed an *rng for tie-breaking/noise. Goofspiel is open-information,
// so a policy may inspect the opponent's remaining hand and the full history.
package bot

import (
	"math/rand"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// Policy chooses a card to play from `seat`'s current view of the game. It MUST
// return a card still in hand (s.Hands[seat]).
type Policy func(s gs.State, seat int, rng *rand.Rand) int

// Strategy names. These double as match.bot_policy values persisted on a sandbox
// match so the round loop knows how the house should play.
const (
	Random       = "random"
	Highest      = "highest"
	Lowest       = "lowest"
	Proportional = "proportional"
	Balanced     = "balanced"
)

// Strategies is the registry of playable policies.
var Strategies = map[string]Policy{
	Random:       RandomPlay,
	Highest:      HighestCard,
	Lowest:       LowestCard,
	Proportional: Proportional2Prize,
	Balanced:     BalancedPlay,
}

// HighestCard always plays the largest remaining card (greedy, exploitable).
func HighestCard(s gs.State, seat int, _ *rand.Rand) int { return extreme(s.Hands[seat], true) }

// LowestCard always plays the smallest remaining card (hoards high cards).
func LowestCard(s gs.State, seat int, _ *rand.Rand) int { return extreme(s.Hands[seat], false) }

// RandomPlay picks a uniformly random legal card — a noisy baseline.
func RandomPlay(s gs.State, seat int, rng *rand.Rand) int {
	h := s.Hands[seat]
	if rng == nil {
		return h[0]
	}
	return h[rng.Intn(len(h))]
}

// Proportional2Prize bids the card closest in value to the current prize —
// "pay what it's worth". A reasonable, beatable baseline.
func Proportional2Prize(s gs.State, seat int, _ *rand.Rand) int {
	prize, h := s.CurrentPrize(), s.Hands[seat]
	best, bestD := h[0], 1<<30
	for _, c := range h {
		d := c - prize
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

// BalancedPlay wins each prize as cheaply as it can and concedes when it can't:
// play the smallest card that still meets the prize's value; if no card reaches
// it, dump the lowest card to preserve high cards for prizes it can win. A solid
// "competent opponent" that reliably beats random and naive highest play.
func BalancedPlay(s gs.State, seat int, _ *rand.Rand) int {
	prize, h := s.CurrentPrize(), s.Hands[seat]
	best := -1
	for _, c := range h {
		if c >= prize && (best == -1 || c < best) {
			best = c
		}
	}
	if best == -1 {
		return extreme(h, false) // can't win it cheaply — concede with the lowest card
	}
	return best
}

// extreme returns the max (max=true) or min (max=false) of a non-empty hand.
func extreme(hand []int, max bool) int {
	v := hand[0]
	for _, c := range hand[1:] {
		if (max && c > v) || (!max && c < v) {
			v = c
		}
	}
	return v
}
