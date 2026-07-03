// Package goofspiel is the deterministic Goofspiel rules engine. It is PURE: no
// database, no clock, no logging, no ambient randomness. Every transition is a
// function (state, input) -> (state', events, error). This is what makes matches
// reproducible, provably fair, and trivially testable. The impure shell (timers,
// persistence, broadcast) lives in internal/match (Stage 3).
//
// See docs/architecture/game-engine.md for the full specification.
package goofspiel

// Version identifies the rule set. It is stored on every match and embedded in
// the match_created event so a replay is reproduced with the exact same rules.
// Bump on ANY behavioral change to the engine.
const Version = "goofspiel-1.0.0"

// Seats.
const (
	SeatA = 0
	SeatB = 1
)

// Winner sentinel for a tie.
const Tie = -1

// FairnessMode controls how the prize order is produced.
const (
	// FairnessShuffled derives a secret, commit-revealed prize order from the seed.
	FairnessShuffled = "shuffled"
	// FairnessOpen uses the fixed Cards order (pure skill; no hidden information).
	FairnessOpen = "open"
)

// TieRule controls how a tied round's contested pool is settled. The spec frames
// this as a configured, consistently-applied rule.
const (
	// TieCarry carries the whole pool (stacked) into the next round (the classic
	// Goofspiel rule and the default).
	TieCarry = "carry"
	// TieSplit awards each seat half the pool; an odd remainder carries forward so
	// no points are ever lost.
	TieSplit = "split"
)

// Config defines a match's parameters. Defaults model standard Goofspiel.
type Config struct {
	Cards        []int  `json:"cards"`         // identical hand + prize deck values, e.g. 1..13
	Rounds       int    `json:"rounds"`        // number of rounds (== len(Cards) for standard play)
	FairnessMode string `json:"fairness_mode"` // FairnessShuffled | FairnessOpen
	TieRule      string `json:"tie_rule"`      // TieCarry (default) | TieSplit
}

// DefaultConfig returns standard 13-card shuffled Goofspiel.
func DefaultConfig() Config {
	cards := make([]int, 13)
	for i := range cards {
		cards[i] = i + 1
	}
	return Config{Cards: cards, Rounds: 13, FairnessMode: FairnessShuffled, TieRule: TieCarry}
}

// RoundResult is the immutable outcome of one resolved round.
type RoundResult struct {
	Round     int    `json:"round"`
	Prize     int    `json:"prize"`      // the prize card value revealed this round
	PrizePool int    `json:"prize_pool"` // total contested this round (incl. carry-over)
	Cards     [2]int `json:"cards"`      // [SeatA, SeatB]
	Winner    int    `json:"winner"`     // SeatA | SeatB | Tie
	Scores    [2]int `json:"scores"`     // running scores AFTER this round
}

// State is the complete, serializable game state. It is the engine's only memory;
// snapshots and reconstruction round-trip through it.
type State struct {
	Round      int           `json:"round"`       // 1-based current round (1..Rounds)
	PrizeOrder []int         `json:"prize_order"` // full prize sequence (server-side; redacted in the agent view)
	PrizePool  int           `json:"prize_pool"`  // contested pool for the current round (incl. carry-over)
	Hands      [2][]int      `json:"hands"`       // remaining cards per seat
	Scores     [2]int        `json:"scores"`
	Sealed     [2]*int       `json:"sealed"`  // this round's sealed cards; nil until submitted
	Finished   bool          `json:"finished"`
	Winner     int           `json:"winner"` // valid only when Finished
	History    []RoundResult `json:"history"`
	NextSeq    int           `json:"next_seq"` // next event sequence number (gap-free per match)
}

// clone returns a deep copy so transitions never mutate the caller's State.
func (s State) clone() State {
	cp := s
	cp.PrizeOrder = append([]int(nil), s.PrizeOrder...)
	cp.History = append([]RoundResult(nil), s.History...)
	for seat := 0; seat < 2; seat++ {
		cp.Hands[seat] = append([]int(nil), s.Hands[seat]...)
		if s.Sealed[seat] != nil {
			v := *s.Sealed[seat]
			cp.Sealed[seat] = &v
		}
	}
	return cp
}

// CurrentPrize is the prize card value for the current round (0 if finished).
func (s State) CurrentPrize() int {
	if s.Finished || s.Round < 1 || s.Round > len(s.PrizeOrder) {
		return 0
	}
	return s.PrizeOrder[s.Round-1]
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func remove(xs []int, v int) []int {
	out := make([]int, 0, len(xs)-1)
	for _, x := range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
