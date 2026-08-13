package monopoly

// EventType enumerates the engine's append-only event kinds. The ordered stream
// of these events IS the replay (mirroring the goofspiel engine's contract).
type EventType string

const (
	EvMatchCreated      EventType = "match_created"
	EvTurnStarted       EventType = "turn_started"
	EvDiceRolled        EventType = "dice_rolled"
	EvMoved             EventType = "moved"
	EvCashChanged       EventType = "cash_changed" // one-sided bank transaction (salary, tax, card, dividend)
	EvRentPaid          EventType = "rent_paid"
	EvPropertyPurchased EventType = "property_purchased"
	EvCardDrawn         EventType = "card_drawn"
	EvWentToJail        EventType = "went_to_jail"
	EvLeftJail          EventType = "left_jail"
	EvHouseBuilt        EventType = "house_built"
	EvHouseSold         EventType = "house_sold"
	EvMortgaged         EventType = "mortgaged"
	EvUnmortgaged       EventType = "unmortgaged"
	EvAuctionStarted    EventType = "auction_started"
	EvBidPlaced         EventType = "bid_placed"
	EvAuctionPassed     EventType = "auction_passed"
	EvAuctionWon        EventType = "auction_won"
	EvAuctionUnsold     EventType = "auction_unsold"
	EvBankrupt          EventType = "bankrupt"
	EvTradeProposed     EventType = "trade_proposed"
	EvTradeExecuted     EventType = "trade_executed"
	EvTradeRejected     EventType = "trade_rejected"
	// EvTradeDeclined: one seat passed on an OPEN offer that is still standing for the
	// seats behind it. Distinct from trade_rejected, which ends the offer — a watching
	// agent that treated a pass as the end would stop tracking an offer it can still take.
	EvTradeDeclined EventType = "trade_declined"
	EvTurnEnded         EventType = "turn_ended"
	EvMatchFinished     EventType = "match_finished"
	// EvAgentSays is public table talk. Monopoly is a negotiation game — the deals
	// happen in the arguing, not the dice — so agents haggle, bluff and needle each
	// other continuously, and spectators watch them do it.
	EvAgentSays EventType = "agent_says"
)

// Event is one entry in the match log. Seq is gap-free and monotonic per match.
type Event struct {
	Seq     int       `json:"seq"`
	Type    EventType `json:"type"`
	Payload any       `json:"payload"`
}

// ── Typed payloads ───────────────────────────────────────────────────────────

// MatchCreatedPayload pins the rules + the commit so a replay is reproducible and
// provably fair. The seed itself is NEVER in the log; it is revealed at
// settlement and supplied to verification out-of-band.
type MatchCreatedPayload struct {
	Version      string `json:"version"`
	Players      int    `json:"players"`
	StartingCash int    `json:"starting_cash"`
	MaxTurns     int    `json:"max_turns"`
	Commit       string `json:"commit"` // sha256(seed), published before any roll
}

type TurnStartedPayload struct {
	Seat      int `json:"seat"`
	TurnCount int `json:"turn_count"`
}

type DiceRolledPayload struct {
	Seat    int  `json:"seat"`
	Die1    int  `json:"die1"`
	Die2    int  `json:"die2"`
	Total   int  `json:"total"`
	Doubles bool `json:"doubles"`
}

type MovedPayload struct {
	Seat     int  `json:"seat"`
	From     int  `json:"from"`
	To       int  `json:"to"`
	PassedGo bool `json:"passed_go"`
}

// CashChangedPayload is a one-sided transaction with the bank (or pot). Delta is
// signed; Balance is the player's resulting cash.
type CashChangedPayload struct {
	Seat    int    `json:"seat"`
	Delta   int    `json:"delta"`
	Balance int    `json:"balance"`
	Reason  string `json:"reason"`
}

type RentPaidPayload struct {
	From     int `json:"from"`
	To       int `json:"to"`
	Property int `json:"property"`
	Amount   int `json:"amount"`
}

type PropertyPurchasedPayload struct {
	Seat     int `json:"seat"`
	Property int `json:"property"`
	Price    int `json:"price"`
}

type CardDrawnPayload struct {
	Seat   int    `json:"seat"`
	Deck   string `json:"deck"` // "chance" | "community_chest"
	CardID int    `json:"card_id"`
	Text   string `json:"text"`
}

type WentToJailPayload struct {
	Seat   int    `json:"seat"`
	Reason string `json:"reason"` // "go_to_jail_space" | "card" | "three_doubles"
}

type LeftJailPayload struct {
	Seat   int    `json:"seat"`
	Method string `json:"method"` // "paid" | "card" | "doubles" | "forced"
}

type BuildPayload struct {
	Seat     int `json:"seat"`
	Property int `json:"property"`
	Houses   int `json:"houses"` // resulting house count (5 == hotel)
}

type MortgagePayload struct {
	Seat     int `json:"seat"`
	Property int `json:"property"`
	Amount   int `json:"amount"`
}

type AuctionStartedPayload struct {
	Property int `json:"property"`
}

type BidPayload struct {
	Seat     int `json:"seat"`
	Property int `json:"property"`
	Amount   int `json:"amount"`
}

type AuctionPassedPayload struct {
	Seat     int `json:"seat"`
	Property int `json:"property"`
}

type AuctionResultPayload struct {
	Seat     int `json:"seat"` // winning seat, or Bank if unsold
	Property int `json:"property"`
	Amount   int `json:"amount"`
}

type BankruptPayload struct {
	Seat     int    `json:"seat"`
	Creditor int    `json:"creditor"`           // Bank(-1) or a seat
	Amount   int    `json:"amount,omitempty"`   // debt that could not be paid
	Property int    `json:"property,omitempty"` // the square whose rent/action triggered it, or -1
	Reason   string `json:"reason,omitempty"`   // "rent" | "tax" | "card" | "jail_fine" | ...
}

// TradePayload describes a proposed/executed/rejected trade.
type TradePayload struct {
	Proposer  int   `json:"proposer"`
	Target    int   `json:"target"`
	GiveProps []int `json:"give_props"`
	GiveCash  int   `json:"give_cash"`
	GiveCards int   `json:"give_cards,omitempty"`
	WantProps []int `json:"want_props"`
	WantCash  int   `json:"want_cash"`
	WantCards int   `json:"want_cards,omitempty"`
}

type TurnEndedPayload struct {
	Seat int `json:"seat"`
}

type MatchFinishedPayload struct {
	Winner    int   `json:"winner"`     // seat or Tie
	NetWorths []int `json:"net_worths"` // per seat
}

// emit attaches the next sequence number to an event and advances the counter.
// AgentSaysPayload is one line of table talk. `Turn` is the turn in progress when
// it was said, so a replay can slot it back into the right moment.
type AgentSaysPayload struct {
	Turn int    `json:"turn"`
	Seat int    `json:"seat"`
	Text string `json:"text"`
	Kind string `json:"kind"` // "say" | "rationale"
}

func (e *Engine) emit(s *State, t EventType, payload any) Event {
	ev := Event{Seq: s.NextSeq, Type: t, Payload: payload}
	s.NextSeq++
	return ev
}
