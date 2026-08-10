package goofspiel

import (
	"errors"
	"strings"
)

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
	ErrEmptyMessage = errors.New("goofspiel: message text is empty")
)

// Table-talk limits. One line is capped so a chatty agent cannot flood the log or
// the opponent's context; the retained transcript is capped so State stays bounded.
const (
	MaxChatLen        = 500
	MaxChatHistory    = 60
	ChatKindSay       = "say"
	ChatKindRationale = "rationale"
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
	if cfg.TieRule == "" {
		cfg.TieRule = TieCarry
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
			Rounds: e.cfg.Rounds, FairnessMode: e.cfg.FairnessMode, TieRule: e.cfg.TieRule, Commit: Commit(seed),
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

// Say records one line of public table talk and emits it for spectators.
//
// Deliberately NOT turn-gated: any seat may speak at any point in a live match,
// including while the other seat is still deciding, and speaking never consumes
// a turn or blocks the round. Only the card itself is ordered — talk is free.
// A finished match is closed to new talk so the replay stays immutable.
//
// Text is trimmed and clamped to MaxChatLen; empty text is rejected rather than
// emitting a blank line into the log.
func (e *Engine) Say(s State, seat int, text, kind string) (State, []Event, error) {
	if s.Finished {
		return s, nil, ErrFinished
	}
	if seat < 0 || seat > 1 {
		return s, nil, ErrInvalidSeat
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return s, nil, ErrEmptyMessage
	}
	if len(text) > MaxChatLen {
		text = strings.TrimSpace(text[:MaxChatLen])
	}
	if kind != ChatKindRationale {
		kind = ChatKindSay
	}
	ns := s.clone()
	line := ChatLine{Round: ns.Round, Seat: seat, Text: text, Kind: kind}
	ns.Chat = append(ns.Chat, line)
	// Bound the retained transcript: the log keeps every line, but State is
	// snapshotted on every transition and handed to agents, so it must not grow
	// without limit over a long match.
	if len(ns.Chat) > MaxChatHistory {
		ns.Chat = append([]ChatLine(nil), ns.Chat[len(ns.Chat)-MaxChatHistory:]...)
	}
	// Identical field set to ChatLine, so a conversion says so directly and cannot
	// drift out of sync when a field is added to one of them.
	ev := e.emit(&ns, EvAgentSays, AgentSaysPayload(line))
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
	// carry is the amount that rolls into the next round's pool (0 for a decisive
	// round; the whole pool under TieCarry; only the odd remainder under TieSplit).
	carry := 0
	switch {
	case winner != Tie:
		ns.Scores[winner] += pool
	case e.cfg.TieRule == TieSplit:
		half := pool / 2
		ns.Scores[SeatA] += half
		ns.Scores[SeatB] += half
		carry = pool - 2*half // 0 or 1; never lose a point
	default: // TieCarry
		carry = pool
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
		ns.PrizePool = nextPrize + carry // carry == pool (TieCarry), remainder (TieSplit), or 0
		evs = append(evs, e.emit(&ns, EvPrizeRevealed, PrizeRevealedPayload{Round: ns.Round, Prize: nextPrize, PrizePool: ns.PrizePool}))
	}
	return ns, evs, nil
}

// Platform timeout-forfeit rule (shared across all 3 games): a turn timeout NEVER
// stalls the match and NEVER rewards silence — the engine applies a deterministic,
// least-harmful legal default for the missing seat, the match plays on to
// completion, and a non-responding agent loses on the merits. Each game realizes
// this with the most neutral move it has: Mafia ABSTAINS (no vote/kill — it has a
// genuine no-op), while Goofspiel and Monopoly have no "do nothing" move, so they
// play the least-harmful legal action (Goofspiel: lowest card; Monopoly: roll then
// decline/pass/end-turn). Same principle, different realization per game's rules.
//
// ForceTimeout plays the deterministic, least-harmful card for a seat that missed
// its window: its LOWEST card in hand. Conceding the current prize with your
// weakest card is the minimal-damage forfeit (high cards are preserved for future
// prizes), and being deterministic it is predictable and replay-reproducible — far
// fairer to a timed-out agent (and far easier to defend in a money game) than a
// random discard that might throw away a strong card. Idempotent: a no-op if the
// seat already sealed. The card is recorded through the normal Seal path, so replay
// reproduces it exactly.
func (e *Engine) ForceTimeout(s State, seat int) (State, []Event, error) {
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
	low := hand[0]
	for _, c := range hand[1:] {
		if c < low {
			low = c
		}
	}
	ns, evs, err := e.Seal(s, seat, low)
	if err != nil {
		return ns, evs, err
	}
	// Count the miss. The card played is the seat's WORST, so absence already costs it
	// the round on the merits; this tally exists so settlement can tell a seat that went
	// dark from one that played and simply could not prove its reasoning was LLM-backed.
	// Those two look identical in the proof tables and must not be paid out the same way.
	ns.Timeouts[seat]++
	return ns, evs, nil
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
