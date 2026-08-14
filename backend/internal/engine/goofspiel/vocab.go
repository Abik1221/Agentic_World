package goofspiel

// vocab.go declares Goofspiel's PUBLIC, agent-facing vocabulary as enumerable
// slices. Goofspiel has no roles/phases and its "action" is simply a card from
// the current hand, so only the event types and the configurable rule values are
// enumerated here. Consumed by the developer-docs generator (cmd/gamespec) and a
// drift test (internal/gamespec).
//
// INVARIANT: when you add or remove an Ev*/Fairness*/Tie* const, update the
// matching slice here in the same change.

// AllEventTypes is every event `type` the engine emits, in match order.
var AllEventTypes = []EventType{
	EvMatchCreated,
	EvPrizeRevealed,
	EvCardSealed,
	EvRoundRevealed,
	EvMatchFinished,
}

// AllFairnessModes is every prize-order fairness mode a match may be configured
// with.
var AllFairnessModes = []string{
	FairnessShuffled,
	FairnessOpen,
}

// AllTieRules is every tied-round settlement rule a match may be configured with.
var AllTieRules = []string{
	TieCarry,
	TieDiscard,
	TieSplit,
}
