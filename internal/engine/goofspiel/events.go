package goofspiel

// EventType enumerates the engine's append-only event kinds. The ordered stream
// of these events IS the replay (see internal/replay).
type EventType string

const (
	EvMatchCreated  EventType = "match_created"
	EvPrizeRevealed EventType = "prize_revealed"
	EvCardSealed    EventType = "card_sealed"   // spectator-safe: carries NO card value
	EvRoundRevealed EventType = "round_revealed"
	EvMatchFinished EventType = "match_finished"
)

// Event is one entry in the match log. Seq is gap-free and monotonic per match.
// Payload is one of the typed payloads below.
type Event struct {
	Seq     int       `json:"seq"`
	Type    EventType `json:"type"`
	Payload any       `json:"payload"`
}

// ── Typed payloads ───────────────────────────────────────────────────────────

// MatchCreatedPayload pins the rules + the commit so a replay is reproducible and
// provably fair. The seed itself is NEVER in the log; it is revealed at
// settlement (matches.prize_seed) and supplied to Verify out-of-band.
type MatchCreatedPayload struct {
	Version      string `json:"version"`
	Cards        []int  `json:"cards"`
	Rounds       int    `json:"rounds"`
	FairnessMode string `json:"fairness_mode"`
	TieRule      string `json:"tie_rule"`
	Commit       string `json:"commit"` // sha256(seed), published before any card is played
}

type PrizeRevealedPayload struct {
	Round     int `json:"round"`
	Prize     int `json:"prize"`
	PrizePool int `json:"prize_pool"`
}

// CardSealedPayload intentionally omits the card value — a spectator (or the
// opponent) must not learn a move before both seats have submitted.
type CardSealedPayload struct {
	Round int `json:"round"`
	Seat  int `json:"seat"`
}

type RoundRevealedPayload struct {
	Round     int    `json:"round"`
	Prize     int    `json:"prize"`
	PrizePool int    `json:"prize_pool"`
	Cards     [2]int `json:"cards"`
	Winner    int    `json:"winner"`
	Scores    [2]int `json:"scores"`
}

type MatchFinishedPayload struct {
	Scores [2]int `json:"scores"`
	Winner int    `json:"winner"`
}

// emit attaches the next sequence number to an event and advances the counter.
func (e *Engine) emit(s *State, t EventType, payload any) Event {
	ev := Event{Seq: s.NextSeq, Type: t, Payload: payload}
	s.NextSeq++
	return ev
}
