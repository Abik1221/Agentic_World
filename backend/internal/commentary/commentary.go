// Package commentary turns a revealed Goofspiel round into deterministic
// play-by-play text and a "dramatic" flag. It is a pure leaf: no I/O, no imports
// beyond the standard library, so both the live broadcaster (spectator) and the
// replay payload (match) can use it without creating a dependency cycle. The same
// Round always yields the same Line — replays read identically to the live call.
package commentary

import "fmt"

// Tie is the winner sentinel for a drawn round (matches the engine's gs.Tie).
const Tie = -1

const (
	highCard  = 11 // "burning" a card this big is notable
	lowPrize  = 3  // ...especially for a prize this small
	bigPot    = 20 // a pot this large is dramatic on its own
	wireRange = 1  // score gap this tight late in the match is "to the wire"
)

// Round is the minimal revealed view of one resolved round (no hidden state).
type Round struct {
	Round       int
	TotalRounds int
	Prize       int
	PrizePool   int
	CardA       int
	CardB       int
	Winner      int // 0 = A, 1 = B, Tie = draw
	ScoreA      int
	ScoreB      int
}

// Line returns one deterministic sentence of commentary for the round.
func Line(r Round) string {
	margin := r.ScoreA - r.ScoreB
	if margin < 0 {
		margin = -margin
	}
	switch {
	case r.Winner == Tie:
		return fmt.Sprintf("Both agents burned a %d — the %d-coin prize carries over. Stakes are climbing to %d.",
			r.CardA, r.Prize, r.PrizePool)
	case r.winnerCard() >= highCard && r.Prize <= lowPrize:
		return fmt.Sprintf("%s torches a %d to grab a measly %d-coin prize — pure aggression.",
			r.winnerLabel(), r.winnerCard(), r.Prize)
	case r.PrizePool >= bigPot:
		return fmt.Sprintf("A massive %d-coin pot on the line — %s seizes it. Score %d–%d.",
			r.PrizePool, r.winnerLabel(), r.ScoreA, r.ScoreB)
	case r.Round >= r.TotalRounds-1 && margin <= wireRange:
		return fmt.Sprintf("Razor-thin at %d–%d with the finish in sight — this one's going to the wire.",
			r.ScoreA, r.ScoreB)
	default:
		return fmt.Sprintf("%s takes the %d-coin prize. Score %d–%d.",
			r.winnerLabel(), r.PrizePool, r.ScoreA, r.ScoreB)
	}
}

// Dramatic flags rounds worth clipping (consumed by Stage 8): a carried pot, a
// big pot, a high card spent on a tiny prize, or a nail-biter near the end.
func Dramatic(r Round) bool {
	margin := r.ScoreA - r.ScoreB
	if margin < 0 {
		margin = -margin
	}
	carried := r.PrizePool > r.Prize
	return carried ||
		r.PrizePool >= bigPot ||
		(r.winnerCard() >= highCard && r.Prize <= lowPrize) ||
		(r.Round >= r.TotalRounds-1 && margin <= wireRange)
}

func (r Round) winnerLabel() string {
	if r.Winner == 1 {
		return "Player B"
	}
	return "Player A"
}

func (r Round) winnerCard() int {
	if r.Winner == 1 {
		return r.CardB
	}
	return r.CardA
}
