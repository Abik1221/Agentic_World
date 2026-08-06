package skill

import mono "github.com/agent-arena/arena/internal/engine/monopoly"

// How often each square is actually landed on — solved, not assumed.
//
// # Why this has to be computed rather than guessed
//
// Every Monopoly decision worth scoring reduces to the same question: what is this square
// worth? And that is rent × how often somebody lands on it. Get the second factor wrong
// and every downstream verdict is wrong in the same direction.
//
// The naive answer is 1/40 for every square. That is badly wrong, because three
// mechanisms bend the walk: Go To Jail teleports you, Chance and Community Chest move you
// (ten of the thirty-four cards in this engine's decks relocate the player), and rolling
// three doubles jails you. The published Markov-chain analyses of Monopoly all agree on
// the consequence — Jail is by far the most-visited square, and the ORANGE group sits at
// the sweet spot 6, 8 and 9 squares past it, which are the most common dice totals.
//
// # Why compute it from the engine instead of hardcoding published numbers
//
// The literature's numbers describe the standard board. This engine's board and decks
// happen to match it, but hardcoding a table would silently rot the moment a card is
// edited or a house rule shipped, and the scorer would keep returning confident numbers
// derived from a board that no longer exists. Solving the chain from mono.Board() and the
// engine's own decks means the scorer always describes THE GAME BEING PLAYED.
//
// It also removes a whole class of transcription error, and it is cheap: a 40×40 chain
// solved by power iteration converges in milliseconds and is computed once per process.
//
// # The model
//
// States are the 40 squares. From each square, the 2d6 distribution gives the next square,
// then the destination's own effect is applied (jail teleport, card draw). Card decks are
// modelled as a uniform draw over the deck's movement effects, which is exact in
// expectation because the engine shuffles both decks and cycles them.
//
// Deliberately NOT modelled: the doubles-triple-jail rule and jail turn mechanics. Both
// shift the distribution by well under a percentage point, and modelling them would need
// the state to carry doubles-count and jail-turn history — a ~10× larger chain for a
// correction far below the noise in everything downstream. Documented rather than hidden,
// because "we approximated here" is a fact a reader of the score deserves.

// landingProbs is the stationary distribution over board squares, indexed by square.
// Computed once on first use; a pure function of the engine's board and decks.
var landingProbs = solveLandingProbs()

// LandingProb returns the long-run probability that a moving player lands on `square`.
func LandingProb(square int) float64 {
	if square < 0 || square >= mono.BoardSize {
		return 0
	}
	return landingProbs[square]
}

// diceDist is the probability of each 2d6 total, indexed by total (2..12).
func diceDist() [13]float64 {
	var d [13]float64
	for a := 1; a <= 6; a++ {
		for b := 1; b <= 6; b++ {
			d[a+b] += 1.0 / 36.0
		}
	}
	return d
}

// cardRelocations returns, for each deck, the probability that a draw sends the player to
// a given square, plus the probability the draw leaves them where they are.
//
// Mirrors this engine's own decks (internal/engine/monopoly/cards.go). The "nearest
// railroad / nearest utility" cards are resolved per drawing square, so they are handled
// by the caller rather than baked in here.
type deckEffect struct {
	// moveTo maps destination square → probability, for cards with a fixed destination.
	moveTo map[int]float64
	// toJail is the probability the draw sends the player to jail.
	toJail float64
	// nearestRail / nearestUtil are probabilities of the relative-move cards.
	nearestRail float64
	nearestUtil float64
	// back3 is the probability of "go back three spaces".
	back3 float64
	// stay is the probability the card has no movement effect.
	stay float64
}

const jailSquare = 10

// effectsOf converts a deck's card COUNTS into movement probabilities.
//
// Derived from the engine's own decks rather than transcribed, and that is not fussiness:
// the first version of this file hand-wrote the canonical Rand McNally deck and was
// WRONG, because this engine's Chance deck carries one "advance to the nearest railroad"
// card where the canonical deck carries two. The probabilities then failed to sum to 1,
// the chain leaked mass on every iteration, and it never converged — which the tests
// caught only because they check convergence rather than just plausibility.
//
// Reading the deck removes that entire failure mode: edit a card and the chain follows.
func effectsOf(m mono.DeckMovement) deckEffect {
	n := float64(m.Size)
	if n == 0 {
		return deckEffect{stay: 1}
	}
	e := deckEffect{
		moveTo:      make(map[int]float64, len(m.MoveTo)),
		toJail:      float64(m.ToJail) / n,
		nearestRail: float64(m.NearestRail) / n,
		nearestUtil: float64(m.NearestUtil) / n,
		back3:       float64(m.Back3) / n,
		stay:        float64(m.Stay) / n,
	}
	for dest, count := range m.MoveTo {
		e.moveTo[dest] = float64(count) / n
	}
	return e
}

func chanceEffects() deckEffect { return effectsOf(mono.ChanceMovement()) }
func chestEffects() deckEffect  { return effectsOf(mono.ChestMovement()) }

// nearestOf returns the next square of the given kind at or after `from`, wrapping.
func nearestOf(board []mono.Space, from int, kind mono.SpaceKind) int {
	for i := 1; i <= mono.BoardSize; i++ {
		idx := (from + i) % mono.BoardSize
		if board[idx].Kind == kind {
			return idx
		}
	}
	return from
}

// resolveSquare spreads the probability of ARRIVING at `sq` over where the player
// actually ends up once the square's own effect has applied.
//
// Recursion depth is bounded: "go back three" from Chance at 36 lands on Community Chest
// at 33, which can itself move the player. Two levels is enough to cover every path this
// board admits, and the depth guard makes that explicit rather than trusting it.
func resolveSquare(board []mono.Space, sq int, p float64, out []float64, depth int) {
	if p <= 0 {
		return
	}
	if depth > 2 {
		out[sq] += p
		return
	}
	switch board[sq].Kind {
	case mono.KindGoToJail:
		out[jailSquare] += p
	case mono.KindChance, mono.KindCommunityChest:
		eff := chanceEffects()
		if board[sq].Kind == mono.KindCommunityChest {
			eff = chestEffects()
		}
		out[sq] += p * eff.stay
		out[jailSquare] += p * eff.toJail
		for dest, q := range eff.moveTo {
			out[dest] += p * q
		}
		if eff.nearestRail > 0 {
			out[nearestOf(board, sq, mono.KindRailroad)] += p * eff.nearestRail
		}
		if eff.nearestUtil > 0 {
			out[nearestOf(board, sq, mono.KindUtility)] += p * eff.nearestUtil
		}
		if eff.back3 > 0 {
			back := (sq - 3 + mono.BoardSize) % mono.BoardSize
			resolveSquare(board, back, p*eff.back3, out, depth+1)
		}
	default:
		out[sq] += p
	}
}

// solveLandingProbs solves the chain by power iteration.
//
// Power iteration rather than an eigen-solver: the chain is small, irreducible and
// aperiodic, so it converges geometrically, and a fixed iteration count keeps the result
// bit-identical on every machine and every run — which the rest of this package depends
// on.
func solveLandingProbs() [mono.BoardSize]float64 {
	board := mono.Board()
	dice := diceDist()

	var cur [mono.BoardSize]float64
	cur[0] = 1 // everyone starts on GO

	const iterations = 600
	for it := 0; it < iterations; it++ {
		var next [mono.BoardSize]float64
		for from := 0; from < mono.BoardSize; from++ {
			p := cur[from]
			if p <= 0 {
				continue
			}
			// A player sitting in Jail still rolls out of it, so jail is not absorbing.
			for total := 2; total <= 12; total++ {
				resolveSquare(board, (from+total)%mono.BoardSize, p*dice[total], next[:], 0)
			}
		}
		cur = next
	}

	// Renormalise. The iteration is measure-preserving in exact arithmetic; this removes
	// accumulated floating-point drift so callers can rely on the vector summing to 1.
	var sum float64
	for _, v := range cur {
		sum += v
	}
	if sum > 0 {
		for i := range cur {
			cur[i] /= sum
		}
	}
	return cur
}

// GroupLandingProb is the combined probability of landing on any square in a colour
// group — the number that decides what a monopoly in that group is actually worth.
func GroupLandingProb(group string) float64 {
	var total float64
	for _, s := range mono.Board() {
		if s.Group == group {
			total += LandingProb(s.Index)
		}
	}
	return total
}
