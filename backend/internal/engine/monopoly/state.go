// Package monopoly is the deterministic Monopoly rules engine. Like the goofspiel
// engine, it is PURE: no database, no clock, no logging, no ambient randomness.
// Every transition is a function (state, input) -> (state', events, error). All
// randomness (dice, card decks) is derived from the match seed, so a match is
// fully reproducible from its seed + ordered action log — the foundation of
// provably-fair, replayable, trivially-testable play.
//
// The impure shell (timers, persistence, broadcast, matchmaking) lives outside
// this package and wraps these pure transitions, exactly as the match worker
// wraps internal/engine/goofspiel. See docs/architecture/monopoly-engine.md.
package monopoly

// Version identifies the rule set. It is embedded in the match_created event and
// stored on every match so a replay is reproduced with the exact same rules.
// Bump on ANY behavioral change to the engine.
const Version = "monopoly-1.2.0"

// Bank is the sentinel "owner" for unowned property and the sentinel creditor for
// payments that go to / come from the bank. Tie is the sentinel winner for a draw.
const (
	Bank = -1
	Tie  = -1
)

// Turn phases. The phase tells callers (and LegalActions) whose decision is
// pending and which actions are valid right now.
const (
	PhaseRoll          = "roll"           // current player must roll (or act in jail)
	PhaseJail          = "jail"           // current player is in jail and must choose
	PhaseAcquire       = "acquire"        // current player landed on an unowned property
	PhaseAuction       = "auction"        // an auction is open; AuctionState.Current bids
	PhaseResolveDebt   = "resolve_debt"   // a player owes more than their cash; must raise funds or go bankrupt
	PhaseManage        = "manage"         // post-move: build/mortgage/trade, then end turn (or re-roll on doubles)
	PhaseTradeResponse = "trade_response" // a trade was proposed; the target must accept or reject
	PhaseTrade         = "trade"          // open floor at the top of a turn: other players may propose a trade or skip
	PhaseGameOver      = "game_over"
)

// Trade is a proposed player-to-player exchange of properties and cash. The
// proposer gives GiveProps + GiveCash and receives WantProps + WantCash.
// OpenToTable is the Target of an offer made to the WHOLE table rather than to one
// seat: any player who can satisfy it may take it, first come first served.
//
// -1 rather than a separate bool, and never 0, for the reason seat numbering forces
// everywhere in this codebase: seat 0 is a real player, so a zero value must never be
// readable as "everyone". An offer whose Target was accidentally left unset is then a
// concrete offer to seat 0, which the strict validator rejects on its own terms — it
// can never silently become an offer to the table.
const OpenToTable = -1

type Trade struct {
	Proposer int `json:"proposer"`
	// Target is the seat being offered to, or OpenToTable for an open offer.
	Target int `json:"target"`
	GiveProps []int `json:"give_props"`           // proposer -> target
	GiveCash  int   `json:"give_cash"`            // proposer -> target
	GiveCards int   `json:"give_cards,omitempty"` // get-out-of-jail cards proposer -> target
	WantProps []int `json:"want_props"`           // target -> proposer
	WantCash  int   `json:"want_cash"`            // target -> proposer
	WantCards int   `json:"want_cards,omitempty"` // get-out-of-jail cards target -> proposer
}

// Player is one seat's mutable state.
type Player struct {
	Seat      int  `json:"seat"`
	Cash      int  `json:"cash"`
	Position  int  `json:"position"`
	InJail    bool `json:"in_jail"`
	JailTurns int  `json:"jail_turns"` // failed escape attempts so far (0..3)
	JailCards int  `json:"jail_cards"` // "get out of jail free" cards held
	Bankrupt  bool `json:"bankrupt"`
	Doubles   int  `json:"doubles"` // consecutive doubles rolled in the current turn
}

// Holding is the ownership state of one ownable board square. For non-ownable
// squares Owner stays Bank and the rest is ignored.
type Holding struct {
	Owner     int  `json:"owner"`  // Bank(-1) or a seat
	Houses    int  `json:"houses"` // 0..5 (5 == hotel)
	Mortgaged bool `json:"mortgaged"`
}

// AuctionState tracks an open ascending auction. A player who passes is removed
// from InAuction; the last remaining (or the high bidder once all others pass)
// wins the property at HighBid.
type AuctionState struct {
	Property   int    `json:"property"`
	HighBid    int    `json:"high_bid"`
	HighBidder int    `json:"high_bidder"` // Bank(-1) until the first bid
	InAuction  []bool `json:"in_auction"`  // per-seat: still bidding?
	Current    int    `json:"current"`     // seat whose bid/pass is awaited
	// Estate marks an auction of a bankrupt-to-bank estate: when it (and the rest of
	// EstateQueue) closes, the debtor's turn ends rather than returning to PhaseManage.
	Estate bool `json:"estate,omitempty"`
}

// Debt records an unpaid obligation that exceeds the debtor's cash. While it is
// set the engine is in PhaseResolveDebt: the debtor must mortgage/sell to raise
// funds (auto-settled once cash >= Amount) or declare bankruptcy.
type Debt struct {
	Debtor   int    `json:"debtor"`
	Creditor int    `json:"creditor"` // Bank(-1) or a seat
	Amount   int    `json:"amount"`
	Property int    `json:"property"` // the property the debt is rent for, or -1
	Reason   string `json:"reason"`   // settlement event reason ("rent","tax","card",...)
}

// State is the complete, serializable game state — the engine's only memory.
// Snapshots and replay reconstruction round-trip through it.
type State struct {
	Players  []Player  `json:"players"`
	Holdings []Holding `json:"holdings"` // length BoardSize, indexed by board square
	Current  int       `json:"current"`  // current player's seat
	Phase    string    `json:"phase"`

	HousesRemaining int `json:"houses_remaining"` // bank supply (starts 32)
	HotelsRemaining int `json:"hotels_remaining"` // bank supply (starts 12)

	LastRoll [2]int `json:"last_roll"` // the two dice of the most recent roll
	RollSeq  int    `json:"roll_seq"`  // count of dice rolls so far (drives deterministic RNG)

	ChanceOrder []int `json:"chance_order"` // seed-shuffled deck (card ids)
	ChanceIdx   int   `json:"chance_idx"`   // next card to draw (wraps)
	CCOrder     []int `json:"cc_order"`     // seed-shuffled deck (card ids)
	CCIdx       int   `json:"cc_idx"`

	Auction      *AuctionState `json:"auction,omitempty"`
	Debt         *Debt         `json:"debt,omitempty"`
	PendingTrade *Trade        `json:"pending_trade,omitempty"`
	// EstateQueue holds the remaining property squares to auction off after a player
	// went bankrupt owing the BANK: the bank auctions the estate to the surviving
	// players (official rule), one property at a time, before the debtor's turn ends.
	EstateQueue   []int `json:"estate_queue,omitempty"`
	TradeCounters int   `json:"trade_counters,omitempty"` // counters so far in the open negotiation
	// Open trade window (PhaseTrade): the seats, in order, still owed a chance to
	// propose a trade before the turn owner rolls. TradeReturn records where a
	// negotiation should resume once it resolves ("trade" window or "manage").
	TradeQueue  []int  `json:"trade_queue,omitempty"`
	TradeReturn string `json:"trade_return,omitempty"`
	// OpenResponders holds the seats still owed a chance at an OPEN offer
	// (PendingTrade.Target == OpenToTable), in seat order, head first. Only the head
	// may act. The queue is built once when the offer opens and contains ONLY seats
	// that could actually satisfy it, so nobody is asked to answer an offer they
	// cannot take.
	//
	// This is what makes "first come first served" deterministic. Resolving an open
	// offer by wall-clock arrival would make the same match replay differently
	// depending on network timing, which would break replay verification — the
	// engine's whole basis for proving a result. Seat order IS the race here: the
	// fast agent wins by being ready when its turn to answer comes.
	OpenResponders []int `json:"open_responders,omitempty"`

	FreeParkingPot int `json:"free_parking_pot,omitempty"` // only used when the house rule is on

	// PendingJailMove carries a jail "forced $50" continuation across a debt: when
	// a player on their third jail turn cannot afford the fine, this holds the dice
	// total to move by once the debt settles (0 == no pending move).
	PendingJailMove int `json:"pending_jail_move,omitempty"`

	TurnCount int  `json:"turn_count"` // total dice rolls taken (bounds the game via MaxTurns)
	Finished  bool `json:"finished"`
	Winner    int  `json:"winner"` // valid only when Finished (seat or Tie)

	NextSeq int `json:"next_seq"` // next event sequence number (gap-free per match)
	// Chat is the public table talk, oldest first, capped at MaxChatHistory. It is
	// part of State so it snapshots with the match and so every agent view can hand
	// each seat what the others have said — a negotiation needs both directions.
	Chat []ChatLine `json:"chat,omitempty"`

	// Timeouts / Asks count, per seat, how often the platform had to act on the seat's
	// behalf versus how often it was asked at all. Settlement uses the ratio to tell a
	// seat that went dark from one that played and could not prove its reasoning was
	// LLM-backed — identical (zero) proof counts, opposite correct outcomes.
	//
	// In State rather than derived from the benchmark tables because settlement must not
	// race the outbox. Absent on states written before this field existed, which reads as
	// "never timed out" — the conservative direction.
	Timeouts map[int]int `json:"timeouts,omitempty"`
	Asks     map[int]int `json:"asks,omitempty"`
}

// noteAsked records that a seat was asked to act, and whether the platform had to
// answer for it. Monopoly asks a seat many times per turn (trade window, roll,
// buy/auction, manage, end turn), so absence is a ratio over decision points rather
// than a count of turns.
func (s *State) noteAsked(seat int, forced bool) {
	if s.Asks == nil {
		s.Asks = map[int]int{}
	}
	s.Asks[seat]++
	if !forced {
		return
	}
	if s.Timeouts == nil {
		s.Timeouts = map[int]int{}
	}
	s.Timeouts[seat]++
}

// SeatWasAbsent reports whether the platform played more of a seat's decisions than
// the agent did. See the note on Timeouts for why settlement needs this.
func (s *State) SeatWasAbsent(seat int) bool {
	asked := s.Asks[seat]
	if asked <= 0 {
		return false
	}
	return s.Timeouts[seat]*2 > asked
}

// ChatLine is one spoken line, retained so later speakers can read it.
type ChatLine struct {
	Turn int    `json:"turn"`
	Seat int    `json:"seat"`
	Text string `json:"text"`
	Kind string `json:"kind"` // "say" | "rationale"
}

func cloneSeatCounts(m map[int]int) map[int]int {
	if m == nil {
		return nil
	}
	out := make(map[int]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// clone returns a deep copy so transitions never mutate the caller's State.
func (s State) clone() State {
	cp := s
	cp.Players = append([]Player(nil), s.Players...)
	cp.Holdings = append([]Holding(nil), s.Holdings...)
	cp.Chat = append([]ChatLine(nil), s.Chat...)
	cp.ChanceOrder = append([]int(nil), s.ChanceOrder...)
	cp.CCOrder = append([]int(nil), s.CCOrder...)
	cp.Timeouts = cloneSeatCounts(s.Timeouts)
	cp.Asks = cloneSeatCounts(s.Asks)
	if s.Auction != nil {
		a := *s.Auction
		a.InAuction = append([]bool(nil), s.Auction.InAuction...)
		cp.Auction = &a
	}
	if s.Debt != nil {
		d := *s.Debt
		cp.Debt = &d
	}
	if s.PendingTrade != nil {
		tr := *s.PendingTrade
		tr.GiveProps = append([]int(nil), s.PendingTrade.GiveProps...)
		tr.WantProps = append([]int(nil), s.PendingTrade.WantProps...)
		cp.PendingTrade = &tr
	}
	cp.TradeQueue = append([]int(nil), s.TradeQueue...)
	cp.OpenResponders = append([]int(nil), s.OpenResponders...)
	cp.EstateQueue = append([]int(nil), s.EstateQueue...)
	return cp
}

// activeSeats returns the seats of all non-bankrupt players, in seat order.
func (s State) activeSeats() []int {
	var out []int
	for i := range s.Players {
		if !s.Players[i].Bankrupt {
			out = append(out, s.Players[i].Seat)
		}
	}
	return out
}

// activeCount returns how many players are still solvent.
func (s State) activeCount() int {
	n := 0
	for i := range s.Players {
		if !s.Players[i].Bankrupt {
			n++
		}
	}
	return n
}

// nextActiveSeat returns the next non-bankrupt seat after `from` (wrapping). If
// no other active seat exists it returns `from`.
func (s State) nextActiveSeat(from int) int {
	n := len(s.Players)
	for step := 1; step <= n; step++ {
		cand := (from + step) % n
		if !s.Players[cand].Bankrupt {
			return cand
		}
	}
	return from
}

// ownedCountInGroup returns how many squares of `group` `owner` holds.
func (s State) ownedCountInGroup(owner int, group string) int {
	n := 0
	for _, idx := range groupMembers[group] {
		if s.Holdings[idx].Owner == owner {
			n++
		}
	}
	return n
}

// ownsFullGroup reports whether `owner` holds every square in `group`.
func (s State) ownsFullGroup(owner int, group string) bool {
	members := groupMembers[group]
	return len(members) > 0 && s.ownedCountInGroup(owner, group) == len(members)
}

// railroadsOwned / utilitiesOwned count an owner's holdings in those pseudo-groups.
func (s State) railroadsOwned(owner int) int { return s.ownedCountInGroup(owner, GroupRailroad) }
func (s State) utilitiesOwned(owner int) int { return s.ownedCountInGroup(owner, GroupUtility) }

// NetWorth is cash + property value + buildings. Mortgaged property counts at its
// mortgage value. It ranks players when the game ends on the turn cap and breaks
// ties; it is also a useful spectator metric.
func (s State) NetWorth(seat int) int {
	total := s.Players[seat].Cash
	for idx := 0; idx < BoardSize; idx++ {
		h := s.Holdings[idx]
		if h.Owner != seat {
			continue
		}
		sp := space(idx)
		if h.Mortgaged {
			total += sp.MortgageValue()
		} else {
			total += sp.Price
		}
		if h.Houses > 0 {
			total += h.Houses * sp.HouseCost // hotel (5) values as 5 * houseCost
		}
	}
	return total
}

// CurrentPlayer returns a pointer-free copy of the current player.
func (s State) CurrentPlayer() Player { return s.Players[s.Current] }
