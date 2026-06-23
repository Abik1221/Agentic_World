package goofspiel

import "errors"

// Engine evaluates Goofspiel rules. It is stateless beyond its Config; all game
// state is passed in and returned, never held.
type Engine struct{ cfg Config }

// Engine errors. They are stable so the match layer can map them to API codes.
var (
	ErrFinished     = errors.New("goofspiel: match is finished")
	ErrInvalidSeat  = errors.New("goofspiel: invalid seat")
	ErrAlreadyActed = errors.New("goofspiel: seat already sealed this round")
	ErrIllegalCard  = errors.New("goofspiel: card is not in the seat's hand")
	ErrNotReady     = errors.New("goofspiel: both seats must seal before resolve")
)

// New builds an engine, normalizing the config (defaults + clamps) so callers
// cannot construct an inconsistent game.
func New(cfg Config) *Engine {
	if len(cfg.Cards) == 0 {
		cfg = DefaultConfig()
	}
	if cfg.Rounds <= 0 || cfg.Rounds > len(cfg.Cards) {
		cfg.Rounds = len(cfg.Cards)
	}
	if cfg.FairnessMode == "" {
		cfg.FairnessMode = FairnessShuffled
	}
	return &Engine{cfg: cfg}
}

// Config returns the engine's normalized configuration.
func (e *Engine) Config() Config { return e.cfg }

// Init deals identical hands, derives the prize order from the seed, and emits
// match_created (with the commit) + the first prize_revealed.
func (e *Engine) Init(seed []byte) (State, []Event) {
	order := derivePrizeOrder(e.cfg, seed)
	s := State{
		Round:      1,
		PrizeOrder: order,
		PrizePool:  order[0],
		Hands:      [2][]int{append([]int(nil), e.cfg.Cards...), append([]int(nil), e.cfg.Cards...)},
		Winner:     Tie,
	}
	evs := []Event{
		e.emit(&s, EvMatchCreated, MatchCreatedPayload{
			Version: Version, Cards: append([]int(nil), e.cfg.Cards...),
			Rounds: e.cfg.Rounds, FairnessMode: e.cfg.FairnessMode, Commit: Commit(seed),
		}),
		e.emit(&s, EvPrizeRevealed, PrizeRevealedPayload{Round: 1, Prize: order[0], PrizePool: s.PrizePool}),
	}
	return s, evs
}

// LegalActions returns the cards a seat may play now (a copy of its hand), or nil
// if the match is finished or the seat already sealed this round.
func (e *Engine) LegalActions(s State, seat int) []int {
	if s.Finished || seat < 0 || seat > 1 || s.Sealed[seat] != nil {
		return nil
	}
	return append([]int(nil), s.Hands[seat]...)
}

// Seal records a seat's secret card for the current round. The emitted card_sealed
// event deliberately omits the value (spectator/opponent must not see it yet).
func (e *Engine) Seal(s State, seat, card int) (State, []Event, error) {
	if s.Finished {
		return s, nil, ErrFinished
	}
	if seat < 0 || seat > 1 {
		return s, nil, ErrInvalidSeat
	}
	if s.Sealed[seat] != nil {
		return s, nil, ErrAlreadyActed
	}
	if !contains(s.Hands[seat], card) {
		return s, nil, ErrIllegalCard
	}
	ns := s.clone()
	v := card
	ns.Sealed[seat] = &v
	ev := e.emit(&ns, EvCardSealed, CardSealedPayload{Round: ns.Round, Seat: seat})
	return ns, []Event{ev}, nil
}

// Resolve reveals both sealed cards, awards (or carries) the pool, discards the
// played cards, advances the round, and emits round_revealed followed by either
// prize_revealed (next round) or match_finished.
func (e *Engine) Resolve(s State) (State, []Event, error) {
	if s.Finished {
		return s, nil, ErrFinished
	}
	if s.Sealed[SeatA] == nil || s.Sealed[SeatB] == nil {
		return s, nil, ErrNotReady
	}
	ns := s.clone()
	c0, c1 := *ns.Sealed[SeatA], *ns.Sealed[SeatB]
	ns.Hands[SeatA] = remove(ns.Hands[SeatA], c0)
	ns.Hands[SeatB] = remove(ns.Hands[SeatB], c1)

	prize := ns.PrizeOrder[ns.Round-1]
	pool := ns.PrizePool

	winner := Tie
	switch {
	case c0 > c1:
		winner = SeatA
	case c1 > c0:
		winner = SeatB
	}
	if winner != Tie {
		ns.Scores[winner] += pool
	}

	rr := RoundResult{Round: ns.Round, Prize: prize, PrizePool: pool, Cards: [2]int{c0, c1}, Winner: winner, Scores: ns.Scores}
	ns.History = append(ns.History, rr)

	evs := []Event{e.emit(&ns, EvRoundRevealed, RoundRevealedPayload{
		Round: rr.Round, Prize: rr.Prize, PrizePool: rr.PrizePool, Cards: rr.Cards, Winner: winner, Scores: ns.Scores,
	})}

	ns.Sealed = [2]*int{}
	ns.Round++
	if ns.Round > e.cfg.Rounds {
		ns.Finished = true
		ns.PrizePool = 0
		ns.Winner = finalWinner(ns.Scores)
		evs = append(evs, e.emit(&ns, EvMatchFinished, MatchFinishedPayload{Scores: ns.Scores, Winner: ns.Winner}))
	} else {
		nextPrize := ns.PrizeOrder[ns.Round-1]
		if winner == Tie {
			ns.PrizePool = pool + nextPrize // tie carries & stacks
		} else {
			ns.PrizePool = nextPrize
		}
		evs = append(evs, e.emit(&ns, EvPrizeRevealed, PrizeRevealedPayload{Round: ns.Round, Prize: nextPrize, PrizePool: ns.PrizePool}))
	}
	return ns, evs, nil
}

// ForceTimeout plays a deterministic random legal card for a seat that missed its
// window. Idempotent: if the seat already sealed, it is a no-op. The chosen card
// is recorded through the normal Seal path, so replay reproduces it exactly.
func (e *Engine) ForceTimeout(s State, seat int, rnd Rand) (State, []Event, error) {
	if s.Finished {
		return s, nil, ErrFinished
	}
	if seat < 0 || seat > 1 {
		return s, nil, ErrInvalidSeat
	}
	if s.Sealed[seat] != nil {
		return s, nil, nil // already acted
	}
	hand := s.Hands[seat]
	if len(hand) == 0 {
		return s, nil, ErrIllegalCard
	}
	return e.Seal(s, seat, hand[rnd.Intn(len(hand))])
}

func finalWinner(scores [2]int) int {
	switch {
	case scores[SeatA] > scores[SeatB]:
		return SeatA
	case scores[SeatB] > scores[SeatA]:
		return SeatB
	default:
		return Tie
	}
}
