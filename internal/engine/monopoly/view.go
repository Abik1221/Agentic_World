package monopoly

// view.go exposes the two things the impure shell (the match service) needs that
// the pure engine otherwise keeps private: who must act now, and a state that is
// safe to hand to agents and spectators.

// PendingSeat returns the seat whose decision the engine is waiting on. The match
// service uses it to authorize the acting agent (only the pending seat may act)
// and to drive bot-controlled seats.
func (e *Engine) PendingSeat(s State) int { return e.pendingActor(s) }

// PublicState returns a copy of the state that is safe to expose to agents and
// spectators. Monopoly is a perfect-information game with ONE exception: future
// randomness. The seed-shuffled card decks (ChanceOrder / CCOrder) fully
// determine every future Chance and Community Chest draw. Handing them to an
// agent would let it read future cards — which the spec explicitly forbids
// ("AI agents may NOT access future cards / hidden randomness / future dice").
//
// This strips the deck orders while preserving everything an agent is entitled
// to see (board, ownership, cash, positions, jail, debts, auctions, trades).
// Already-drawn cards remain fully visible through the event log (card_drawn),
// and the future dice were never in State to begin with (they derive from the
// secret seed, revealed only at settlement). The draw indices are kept: they
// reveal only how many cards have been drawn, never which come next.
func (s State) PublicState() State {
	cp := s.clone()
	cp.ChanceOrder = nil
	cp.CCOrder = nil
	return cp
}
