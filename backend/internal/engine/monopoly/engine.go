package monopoly

import (
	"errors"
	"strings"
)

// Table-talk limits. One line is capped so a chatty agent cannot flood the log or
// the other seats' context; the retained transcript is capped so State stays bounded.
const (
	MaxChatLen        = 500
	MaxChatHistory    = 80
	ChatKindSay       = "say"
	ChatKindRationale = "rationale"
)

// Engine evaluates Monopoly rules. Like the goofspiel engine it is stateless
// beyond its Config; all game state is passed in and returned, never held. Every
// public method is a pure transition (state, input) -> (state', events, error).
type Engine struct{ cfg Config }

// Engine errors. They are stable so the match layer can map them to API codes.
var (
	ErrFinished          = errors.New("monopoly: match is finished")
	ErrInvalidSeat       = errors.New("monopoly: invalid seat")
	ErrNotYourTurn       = errors.New("monopoly: it is not this seat's decision")
	ErrIllegalAction     = errors.New("monopoly: illegal action for the current phase")
	ErrInsufficientFunds = errors.New("monopoly: insufficient cash")
	ErrInvalidProperty   = errors.New("monopoly: invalid property")
	ErrInvalidBid        = errors.New("monopoly: bid must exceed the current high bid")
	ErrEmptyMessage      = errors.New("monopoly: message text is empty")
)

// Action kinds — the verbs an agent submits via Step.
const (
	ActRoll         = "roll"          // PhaseRoll: roll the dice
	ActBuy          = "buy"           // PhaseAcquire: buy the landed property at list price
	ActDecline      = "decline"       // PhaseAcquire: decline (opens an auction if enabled)
	ActBid          = "bid"           // PhaseAuction: raise the high bid (Action.Amount)
	ActPass         = "pass"          // PhaseAuction: drop out of the auction
	ActBuild        = "build"         // PhaseManage/Debt: build a house/hotel (Action.Property)
	ActSellHouse    = "sell_house"    // PhaseManage/Debt: sell a house/hotel back to the bank
	ActMortgage     = "mortgage"      // PhaseManage/Debt: mortgage a property
	ActUnmortgage   = "unmortgage"    // PhaseManage: lift a mortgage (+10% interest)
	ActPayJail      = "pay_jail"      // PhaseJail: pay $50 then roll
	ActUseJailCard  = "use_jail_card" // PhaseJail: spend a get-out-of-jail-free card then roll
	ActRollJail     = "roll_jail"     // PhaseJail: try to roll doubles to escape
	ActEndTurn      = "end_turn"      // PhaseManage: finish the turn (re-roll if doubles)
	ActBankrupt     = "bankrupt"      // PhaseDebt: give up; liquidate to the creditor
	ActProposeTrade = "propose_trade" // PhaseManage: offer a trade (Action.Trade) to another seat
	ActAcceptTrade  = "accept_trade"  // PhaseTradeResponse: the target accepts
	ActRejectTrade  = "reject_trade"  // PhaseTradeResponse: the target declines
	ActCounterTrade = "counter_trade" // PhaseTradeResponse: the target counters with a new offer (Action.Trade)
	ActSkipTrade    = "skip_trade"    // PhaseTrade: decline to open a trade in the open-floor window
)

// JailFine is the cost to buy out of jail.
const JailFine = 50

// maxTradeCounters bounds a single negotiation so counter-offers can't loop
// forever between two agents. After this many counters, only accept/reject
// remain legal.
const maxTradeCounters = 4

// Action is one agent submission. Property/Amount are used by the actions that
// need them; others ignore them.
type Action struct {
	Kind     string `json:"kind"`
	Property int    `json:"property,omitempty"`
	Amount   int    `json:"amount,omitempty"`
	Trade    *Trade `json:"trade,omitempty"` // only for propose_trade
}

// Config defines a match's parameters. Defaults model a standard 4-player game.
type Config struct {
	Players         int  `json:"players"`           // 2..8
	StartingCash    int  `json:"starting_cash"`     // default 1500
	MaxTurns        int  `json:"max_turns"`         // hard cap on dice rolls; guarantees termination
	GoSalary        int  `json:"go_salary"`         // default 200
	DisableAuctions bool `json:"disable_auctions"`  // declining sends property to auction unless this is set
	FreeParkingPool bool `json:"free_parking_pool"` // house rule: taxes/fines fund a Free Parking jackpot
}

// DefaultMaxTurns bounds an unattended game so property tests always terminate.
const DefaultMaxTurns = 1000

// DefaultConfig returns a standard 4-player game.
func DefaultConfig() Config {
	return Config{Players: 4, StartingCash: 1500, MaxTurns: DefaultMaxTurns, GoSalary: 200}
}

// New builds an engine, normalizing the config (defaults + clamps) so callers
// cannot construct an inconsistent game.
func New(cfg Config) *Engine {
	if cfg.Players == 0 {
		cfg.Players = 4
	}
	if cfg.Players < 2 {
		cfg.Players = 2
	}
	if cfg.Players > 8 {
		cfg.Players = 8
	}
	if cfg.StartingCash <= 0 {
		cfg.StartingCash = 1500
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = DefaultMaxTurns
	}
	if cfg.GoSalary <= 0 {
		cfg.GoSalary = 200
	}
	return &Engine{cfg: cfg}
}

// Config returns the engine's normalized configuration.
func (e *Engine) Config() Config { return e.cfg }

// Init builds the opening state, shuffles the card decks from the seed, and emits
// match_created (with the commit) + the first turn_started. Every seat opens on the
// configured starting cash (tournament tables).
func (e *Engine) Init(seed []byte) (State, []Event) {
	return e.InitWithStacks(seed, nil)
}

// InitWithStacks is Init with a PER-SEAT opening stack — used by cash-game tables where
// each seat's chips come from its own buy-in. startingCash[i] sets seat i's opening
// cash; a missing, zero, or negative entry (or a nil slice) falls back to
// cfg.StartingCash. Deck shuffle and opening events are identical to Init.
func (e *Engine) InitWithStacks(seed []byte, startingCash []int) (State, []Event) {
	players := make([]Player, e.cfg.Players)
	for i := range players {
		cash := e.cfg.StartingCash
		if i < len(startingCash) && startingCash[i] > 0 {
			cash = startingCash[i]
		}
		players[i] = Player{Seat: i, Cash: cash, Position: IdxGo}
	}
	holdings := make([]Holding, BoardSize)
	for i := range holdings {
		holdings[i] = Holding{Owner: Bank}
	}
	s := State{
		Players:         players,
		Holdings:        holdings,
		Current:         0,
		Phase:           PhaseRoll,
		HousesRemaining: 32,
		HotelsRemaining: 12,
		ChanceOrder:     derivedDeckOrder(seed, "chance", len(chanceDeck)),
		CCOrder:         derivedDeckOrder(seed, "community_chest", len(ccDeck)),
		Winner:          Tie,
	}
	evs := []Event{
		e.emit(&s, EvMatchCreated, MatchCreatedPayload{
			Version: Version, Players: e.cfg.Players, StartingCash: e.cfg.StartingCash,
			MaxTurns: e.cfg.MaxTurns, Commit: Commit(seed),
		}),
		e.emit(&s, EvTurnStarted, TurnStartedPayload{Seat: 0, TurnCount: 0}),
	}
	// The opening move is just seat 0's roll — nobody owns anything to trade yet.
	// Every subsequent turn opens with the trade window (see endTurn/enterTurn).
	return s, evs
}

// pendingActor returns the seat whose decision the engine is waiting on.
func (e *Engine) pendingActor(s State) int {
	switch s.Phase {
	case PhaseAuction:
		if s.Auction != nil {
			return s.Auction.Current
		}
	case PhaseResolveDebt:
		if s.Debt != nil {
			return s.Debt.Debtor
		}
	case PhaseTradeResponse:
		if s.PendingTrade != nil {
			return s.PendingTrade.Target
		}
	case PhaseTrade:
		if len(s.TradeQueue) > 0 {
			return s.TradeQueue[0]
		}
	}
	return s.Current
}

// Say records one line of public table talk and emits it for spectators.
//
// Deliberately NOT turn-gated: in Monopoly the deal-making happens between turns,
// so any seat may talk at any moment — while another seat is rolling, mid-auction,
// while a trade is pending. Talking is never a move: it cannot roll, buy, bid or
// pass, and it never advances the turn. Bankrupt seats are silenced (they are out
// of the game) and a finished match is closed so the replay stays immutable.
func (e *Engine) Say(s State, seat int, text, kind string) (State, []Event, error) {
	if s.Finished {
		return s, nil, ErrFinished
	}
	if seat < 0 || seat >= len(s.Players) {
		return s, nil, ErrInvalidSeat
	}
	if s.Players[seat].Bankrupt {
		return s, nil, ErrIllegalAction
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
	line := ChatLine{Turn: ns.TurnCount, Seat: seat, Text: text, Kind: kind}
	ns.Chat = append(ns.Chat, line)
	if len(ns.Chat) > MaxChatHistory {
		ns.Chat = append([]ChatLine(nil), ns.Chat[len(ns.Chat)-MaxChatHistory:]...)
	}
	// Identical field set to ChatLine — convert rather than restate it field by field.
	ev := e.emit(&ns, EvAgentSays, AgentSaysPayload(line))
	return ns, []Event{ev}, nil
}

// LegalActions returns the action kinds the given seat may submit right now, or
// nil if it is not that seat's decision (or the match is finished).
func (e *Engine) LegalActions(s State, seat int) []string {
	if s.Finished || seat < 0 || seat >= len(s.Players) {
		return nil
	}
	if seat != e.pendingActor(s) {
		return nil
	}
	switch s.Phase {
	case PhaseRoll:
		return []string{ActRoll}
	case PhaseJail:
		acts := []string{ActRollJail}
		if s.Players[seat].JailCards > 0 {
			acts = append(acts, ActUseJailCard)
		}
		if s.Players[seat].Cash >= JailFine {
			acts = append(acts, ActPayJail)
		}
		return acts
	case PhaseAcquire:
		acts := []string{}
		if s.Players[seat].Cash >= space(s.Players[seat].Position).Price {
			acts = append(acts, ActBuy)
		}
		acts = append(acts, ActDecline)
		return acts
	case PhaseAuction:
		return []string{ActBid, ActPass}
	case PhaseResolveDebt:
		return []string{ActMortgage, ActSellHouse, ActBankrupt}
	case PhaseManage:
		return []string{ActEndTurn, ActBuild, ActSellHouse, ActMortgage, ActUnmortgage, ActProposeTrade}
	case PhaseTradeResponse:
		if s.TradeCounters < maxTradeCounters {
			return []string{ActAcceptTrade, ActRejectTrade, ActCounterTrade}
		}
		return []string{ActAcceptTrade, ActRejectTrade}
	case PhaseTrade:
		return []string{ActProposeTrade, ActSkipTrade}
	}
	return nil
}

// Step applies one action from `seat` and returns the next state + emitted events.
// On any error the ORIGINAL state is returned unchanged (transactional).
func (e *Engine) Step(s State, seat int, a Action, seed []byte) (State, []Event, error) {
	if s.Finished {
		return s, nil, ErrFinished
	}
	if seat < 0 || seat >= len(s.Players) {
		return s, nil, ErrInvalidSeat
	}
	if seat != e.pendingActor(s) {
		return s, nil, ErrNotYourTurn
	}

	ns := s.clone()
	var evs []Event
	var err error
	switch ns.Phase {
	case PhaseRoll:
		evs, err = e.stepRoll(&ns, a, seed)
	case PhaseJail:
		evs, err = e.stepJail(&ns, a, seed)
	case PhaseAcquire:
		evs, err = e.stepAcquire(&ns, a)
	case PhaseAuction:
		evs, err = e.stepAuction(&ns, a)
	case PhaseResolveDebt:
		evs, err = e.stepResolveDebt(&ns, a, seed)
	case PhaseManage:
		evs, err = e.stepManage(&ns, a, seed)
	case PhaseTradeResponse:
		evs, err = e.stepTradeResponse(&ns, a)
	case PhaseTrade:
		evs, err = e.stepTradeWindow(&ns, a)
	default:
		return s, nil, ErrIllegalAction
	}
	if err != nil {
		return s, nil, err
	}
	return ns, evs, nil
}

// Platform timeout-forfeit rule (shared across all 3 games): a turn timeout NEVER
// stalls the match and NEVER rewards silence — the engine applies a deterministic
// default for the missing seat, the match plays on to completion, and a
// non-responding agent loses on the merits. Monopoly has no "do nothing" move (a
// seat on turn must act), so the default is the least-harmful legal action for the
// pending phase (roll / decline-to-buy / pass-auction / end-turn / reject-trade),
// NOT a fabricated abstention. (Mafia, which has a genuine no-op, abstains instead —
// same principle, different realization.)
//
// ForceTimeout submits a deterministic default action for whichever seat the
// engine is waiting on, so a missed decision still advances the game and replays
// identically. It is the impure shell's deadline handler.
func (e *Engine) ForceTimeout(s State, seed []byte) (State, []Event, error) {
	if s.Finished {
		return s, nil, nil
	}
	actor := e.pendingActor(s)
	return e.Step(s, actor, e.defaultAction(s), seed)
}

// defaultAction is the safe, deterministic fallback per phase.
func (e *Engine) defaultAction(s State) Action {
	switch s.Phase {
	case PhaseRoll:
		return Action{Kind: ActRoll}
	case PhaseJail:
		return Action{Kind: ActRollJail}
	case PhaseAcquire:
		return Action{Kind: ActDecline}
	case PhaseAuction:
		return Action{Kind: ActPass}
	case PhaseResolveDebt:
		return Action{Kind: ActBankrupt}
	case PhaseTradeResponse:
		return Action{Kind: ActRejectTrade} // never accept a trade on a timeout
	case PhaseTrade:
		return Action{Kind: ActSkipTrade} // don't open a trade on a timeout
	default:
		return Action{Kind: ActEndTurn}
	}
}

// ── Phase handlers ─────────────────────────────────────────────────────────

func (e *Engine) stepRoll(ns *State, a Action, seed []byte) ([]Event, error) {
	if a.Kind != ActRoll {
		return nil, ErrIllegalAction
	}
	return e.doRoll(ns, seed), nil
}

// doRoll performs a normal (non-jail) dice roll, moves the player, and resolves
// the landing. Three consecutive doubles sends the player to jail without moving.
func (e *Engine) doRoll(ns *State, seed []byte) []Event {
	seat := ns.Current
	d1, d2 := rollDice(seed, ns.RollSeq)
	ns.RollSeq++
	ns.TurnCount++
	ns.LastRoll = [2]int{d1, d2}
	doubles := d1 == d2
	evs := []Event{e.emit(ns, EvDiceRolled, DiceRolledPayload{Seat: seat, Die1: d1, Die2: d2, Total: d1 + d2, Doubles: doubles})}

	if doubles {
		ns.Players[seat].Doubles++
	} else {
		ns.Players[seat].Doubles = 0
	}
	if doubles && ns.Players[seat].Doubles >= 3 {
		evs = e.goToJail(ns, evs, seat, "three_doubles")
		return e.endTurn(ns, evs)
	}

	evs = e.moveBySteps(ns, evs, seat, d1+d2)
	return e.resolveLanding(ns, evs, seed)
}

func (e *Engine) stepJail(ns *State, a Action, seed []byte) ([]Event, error) {
	seat := ns.Current
	p := &ns.Players[seat]
	switch a.Kind {
	case ActUseJailCard:
		if p.JailCards <= 0 {
			return nil, ErrIllegalAction
		}
		p.JailCards--
		p.InJail = false
		p.JailTurns = 0
		evs := []Event{e.emit(ns, EvLeftJail, LeftJailPayload{Seat: seat, Method: "card"})}
		evs = append(evs, e.doRoll(ns, seed)...)
		ns.Players[seat].Doubles = 0 // buying out of jail does not grant a doubles re-roll
		return evs, nil
	case ActPayJail:
		if p.Cash < JailFine {
			return nil, ErrInsufficientFunds
		}
		_, cev := e.chargeBank(ns, seat, JailFine, "jail_fine")
		p.InJail = false
		p.JailTurns = 0
		cev = append(cev, e.emit(ns, EvLeftJail, LeftJailPayload{Seat: seat, Method: "paid"}))
		cev = append(cev, e.doRoll(ns, seed)...)
		ns.Players[seat].Doubles = 0
		return cev, nil
	case ActRollJail:
		return e.doJailRoll(ns, seed), nil
	default:
		return nil, ErrIllegalAction
	}
}

// doJailRoll attempts an escape by doubles. Failure increments the jail counter;
// the third failure forces payment of the fine (or a debt) and then a move.
func (e *Engine) doJailRoll(ns *State, seed []byte) []Event {
	seat := ns.Current
	p := &ns.Players[seat]
	d1, d2 := rollDice(seed, ns.RollSeq)
	ns.RollSeq++
	ns.TurnCount++
	ns.LastRoll = [2]int{d1, d2}
	doubles := d1 == d2
	evs := []Event{e.emit(ns, EvDiceRolled, DiceRolledPayload{Seat: seat, Die1: d1, Die2: d2, Total: d1 + d2, Doubles: doubles})}

	if doubles {
		p.InJail = false
		p.JailTurns = 0
		p.Doubles = 0
		evs = append(evs, e.emit(ns, EvLeftJail, LeftJailPayload{Seat: seat, Method: "doubles"}))
		evs = e.moveBySteps(ns, evs, seat, d1+d2)
		evs = e.resolveLanding(ns, evs, seed)
		ns.Players[seat].Doubles = 0 // escaping jail by doubles does not grant a re-roll
		return evs
	}

	p.JailTurns++
	if p.JailTurns < 3 {
		ns.Phase = PhaseManage // may still manage property, then end the turn (still jailed)
		return evs
	}
	// Third failed attempt: pay the fine, then move by the rolled total.
	if p.Cash >= JailFine {
		_, cev := e.chargeBank(ns, seat, JailFine, "jail_fine")
		evs = append(evs, cev...)
		p.InJail = false
		p.JailTurns = 0
		evs = append(evs, e.emit(ns, EvLeftJail, LeftJailPayload{Seat: seat, Method: "forced"}))
		evs = e.moveBySteps(ns, evs, seat, d1+d2)
		evs = e.resolveLanding(ns, evs, seed)
		ns.Players[seat].Doubles = 0
		return evs
	}
	// Cannot afford the fine: open a debt and remember to move once it settles.
	ns.PendingJailMove = d1 + d2
	ns.Debt = &Debt{Debtor: seat, Creditor: Bank, Amount: JailFine, Property: -1, Reason: "jail_fine"}
	ns.Phase = PhaseResolveDebt
	return evs
}

func (e *Engine) stepAcquire(ns *State, a Action) ([]Event, error) {
	seat := ns.Current
	pos := ns.Players[seat].Position
	sp := space(pos)
	switch a.Kind {
	case ActBuy:
		if ns.Players[seat].Cash < sp.Price {
			return nil, ErrInsufficientFunds
		}
		ns.Players[seat].Cash -= sp.Price
		ns.Holdings[pos] = Holding{Owner: seat}
		ns.Phase = PhaseManage
		return []Event{e.emit(ns, EvPropertyPurchased, PropertyPurchasedPayload{Seat: seat, Property: pos, Price: sp.Price})}, nil
	case ActDecline:
		if e.cfg.DisableAuctions {
			ns.Phase = PhaseManage
			return nil, nil
		}
		return e.startAuction(ns, pos, false), nil
	default:
		return nil, ErrIllegalAction
	}
}

// startAuction opens bidding on `pos` among the non-bankrupt seats. estate marks a
// bankrupt-to-bank estate auction (see closeAuction). The first bidder is the seat
// AFTER ns.Current for an estate auction (the debtor — ns.Current — is bankrupt and
// can't bid), else ns.Current (the seat that declined to buy).
func (e *Engine) startAuction(ns *State, pos int, estate bool) []Event {
	in := make([]bool, len(ns.Players))
	for i := range ns.Players {
		in[i] = !ns.Players[i].Bankrupt
	}
	first := ns.Current
	if estate {
		first = ns.nextActiveSeat(ns.Current)
	}
	ns.Auction = &AuctionState{Property: pos, HighBid: 0, HighBidder: Bank, InAuction: in, Current: first, Estate: estate}
	ns.Phase = PhaseAuction
	return []Event{e.emit(ns, EvAuctionStarted, AuctionStartedPayload{Property: pos})}
}

func (e *Engine) stepAuction(ns *State, a Action) ([]Event, error) {
	au := ns.Auction
	seat := au.Current
	switch a.Kind {
	case ActBid:
		if a.Amount <= au.HighBid {
			return nil, ErrInvalidBid
		}
		if a.Amount > ns.Players[seat].Cash {
			return nil, ErrInsufficientFunds
		}
		au.HighBid = a.Amount
		au.HighBidder = seat
		evs := []Event{e.emit(ns, EvBidPlaced, BidPayload{Seat: seat, Property: au.Property, Amount: a.Amount})}
		e.advanceAuction(ns, &evs)
		return evs, nil
	case ActPass:
		au.InAuction[seat] = false
		evs := []Event{e.emit(ns, EvAuctionPassed, AuctionPassedPayload{Seat: seat, Property: au.Property})}
		e.advanceAuction(ns, &evs)
		return evs, nil
	default:
		return nil, ErrIllegalAction
	}
}

// advanceAuction moves to the next bidder, or closes the auction once at most one
// bidder remains.
func (e *Engine) advanceAuction(ns *State, evs *[]Event) {
	au := ns.Auction
	if countTrue(au.InAuction) <= 1 {
		e.closeAuction(ns, evs)
		return
	}
	n := len(ns.Players)
	for step := 1; step <= n; step++ {
		cand := (au.Current + step) % n
		if au.InAuction[cand] {
			au.Current = cand
			return
		}
	}
	e.closeAuction(ns, evs)
}

func (e *Engine) closeAuction(ns *State, evs *[]Event) {
	au := ns.Auction
	if au.HighBidder != Bank {
		ns.Players[au.HighBidder].Cash -= au.HighBid
		ns.Holdings[au.Property] = Holding{Owner: au.HighBidder}
		*evs = append(*evs, e.emit(ns, EvAuctionWon, AuctionResultPayload{Seat: au.HighBidder, Property: au.Property, Amount: au.HighBid}))
	} else {
		*evs = append(*evs, e.emit(ns, EvAuctionUnsold, AuctionResultPayload{Seat: Bank, Property: au.Property, Amount: 0}))
	}
	estate := au.Estate
	ns.Auction = nil

	// More of a bankrupt estate still to auction → start the next property.
	if len(ns.EstateQueue) > 0 {
		next := ns.EstateQueue[0]
		ns.EstateQueue = ns.EstateQueue[1:]
		*evs = append(*evs, e.startAuction(ns, next, true)...)
		return
	}
	// Estate fully auctioned → the bankrupt debtor's turn is over; advance play.
	if estate {
		*evs = e.endTurn(ns, *evs)
		return
	}
	// Ordinary decline-auction → the seat that landed resumes managing its turn.
	ns.Phase = PhaseManage
}

func (e *Engine) stepResolveDebt(ns *State, a Action, seed []byte) ([]Event, error) {
	if ns.Debt == nil {
		return nil, ErrIllegalAction // defensive: never dereference a missing debt
	}
	switch a.Kind {
	case ActMortgage:
		evs, err := e.doMortgage(ns, a.Property)
		if err != nil {
			return nil, err
		}
		return e.afterRaise(ns, evs, seed), nil
	case ActSellHouse:
		evs, err := e.doSellHouse(ns, a.Property)
		if err != nil {
			return nil, err
		}
		return e.afterRaise(ns, evs, seed), nil
	case ActBankrupt:
		return e.declareBankrupt(ns), nil
	default:
		return nil, ErrIllegalAction
	}
}

// afterRaise auto-settles the open debt if the debtor now has enough cash.
func (e *Engine) afterRaise(ns *State, evs []Event, seed []byte) []Event {
	d := ns.Debt
	if d != nil && ns.Players[d.Debtor].Cash >= d.Amount {
		return e.settleDebt(ns, evs, seed)
	}
	return evs
}

func (e *Engine) stepManage(ns *State, a Action, seed []byte) ([]Event, error) {
	switch a.Kind {
	case ActBuild:
		return e.doBuild(ns, a.Property)
	case ActSellHouse:
		return e.doSellHouse(ns, a.Property)
	case ActMortgage:
		return e.doMortgage(ns, a.Property)
	case ActUnmortgage:
		return e.doUnmortgage(ns, a.Property)
	case ActProposeTrade:
		return e.proposeTrade(ns, a.Trade)
	case ActEndTurn:
		return e.endTurn(ns, nil), nil
	default:
		return nil, ErrIllegalAction
	}
}

// proposeTrade validates an offer from the current player and, if sound, opens a
// PhaseTradeResponse for the target to accept or reject.
func (e *Engine) proposeTrade(ns *State, tr *Trade) ([]Event, error) {
	if tr == nil {
		return nil, ErrIllegalAction
	}
	t := *tr // copy; normalize proposer to the current player
	t.Proposer = ns.Current
	if err := e.validateTrade(ns, t); err != nil {
		return nil, err
	}
	t.GiveProps = append([]int(nil), tr.GiveProps...)
	t.WantProps = append([]int(nil), tr.WantProps...)
	ns.PendingTrade = &t
	ns.Phase = PhaseTradeResponse
	ns.TradeCounters = 0         // fresh negotiation
	ns.TradeReturn = PhaseManage // proposed from the owner's manage phase
	return []Event{e.emit(ns, EvTradeProposed, tradePayload(t))}, nil
}

// validateTrade enforces the trade rules: distinct solvent parties, real ownership
// of every offered property, no buildings on any traded group, and sufficient cash.
func (e *Engine) validateTrade(ns *State, t Trade) error {
	if t.Target < 0 || t.Target >= len(ns.Players) || t.Target == t.Proposer {
		return ErrIllegalAction
	}
	if ns.Players[t.Proposer].Bankrupt || ns.Players[t.Target].Bankrupt {
		return ErrIllegalAction
	}
	if len(t.GiveProps) == 0 && len(t.WantProps) == 0 && t.GiveCash == 0 && t.WantCash == 0 &&
		t.GiveCards == 0 && t.WantCards == 0 {
		return ErrIllegalAction // empty trade
	}
	if t.GiveCash < 0 || t.WantCash < 0 || t.GiveCards < 0 || t.WantCards < 0 {
		return ErrIllegalAction
	}
	if err := e.checkTradeSide(ns, t.GiveProps, t.Proposer); err != nil {
		return err
	}
	if err := e.checkTradeSide(ns, t.WantProps, t.Target); err != nil {
		return err
	}
	if ns.Players[t.Proposer].Cash < t.GiveCash || ns.Players[t.Target].Cash < t.WantCash {
		return ErrInsufficientFunds
	}
	// Each side must actually hold the jail cards it is offering.
	if ns.Players[t.Proposer].JailCards < t.GiveCards || ns.Players[t.Target].JailCards < t.WantCards {
		return ErrIllegalAction
	}
	return nil
}

// checkTradeSide verifies `owner` holds each property and that no property in any
// involved color group carries buildings (you must sell houses before trading).
func (e *Engine) checkTradeSide(ns *State, props []int, owner int) error {
	seen := map[int]bool{}
	for _, idx := range props {
		if idx < 0 || idx >= BoardSize || seen[idx] {
			return ErrInvalidProperty
		}
		seen[idx] = true
		sp := space(idx)
		if !sp.Ownable() || ns.Holdings[idx].Owner != owner {
			return ErrInvalidProperty
		}
		for _, m := range groupMembers[sp.Group] {
			if ns.Holdings[m].Houses > 0 {
				return ErrIllegalAction // can't trade a property whose group has buildings
			}
		}
	}
	return nil
}

func (e *Engine) stepTradeResponse(ns *State, a Action) ([]Event, error) {
	t := ns.PendingTrade
	if t == nil {
		return nil, ErrIllegalAction
	}
	switch a.Kind {
	case ActRejectTrade:
		rejected := tradePayload(*t)
		ns.PendingTrade = nil
		e.resumeAfterTrade(ns)
		return []Event{e.emit(ns, EvTradeRejected, rejected)}, nil
	case ActAcceptTrade:
		// Re-validate at execution time (state may have shifted is impossible here,
		// but this keeps acceptance self-contained and safe).
		if err := e.validateTrade(ns, *t); err != nil {
			ns.PendingTrade = nil
			e.resumeAfterTrade(ns)
			return nil, err
		}
		interest := e.executeTrade(ns, *t) // may bill mortgage-transfer interest (lower seq)
		executed := tradePayload(*t)
		ns.PendingTrade = nil
		e.resumeAfterTrade(ns) // control returns to the window or the turn owner
		return append(interest, e.emit(ns, EvTradeExecuted, executed)), nil
	case ActCounterTrade:
		// The responder (current PendingTrade.Target) makes a return offer to the
		// original proposer. Roles swap: the counter becomes the new pending offer
		// and the ORIGINAL proposer must now respond. Current (the turn owner) is
		// untouched, so the turn still returns to them once this resolves.
		if ns.TradeCounters >= maxTradeCounters {
			return nil, ErrIllegalAction
		}
		if a.Trade == nil {
			return nil, ErrIllegalAction
		}
		c := *a.Trade
		c.Proposer = t.Target // the seat countering (the previous responder)
		c.Target = t.Proposer // back to whoever last offered
		if err := e.validateTrade(ns, c); err != nil {
			return nil, err
		}
		c.GiveProps = append([]int(nil), a.Trade.GiveProps...)
		c.WantProps = append([]int(nil), a.Trade.WantProps...)
		ns.PendingTrade = &c
		ns.Phase = PhaseTradeResponse // stays open; pendingActor is now c.Target
		ns.TradeCounters++
		return []Event{e.emit(ns, EvTradeProposed, tradePayload(c))}, nil
	default:
		return nil, ErrIllegalAction
	}
}

// ── Open trade window (PhaseTrade) ──────────────────────────────────────────
// At the top of each turn, every OTHER active player gets one chance — in seat
// order — to open a trade with anyone (or skip). This is how a player trades on
// a turn that isn't their own. The turn owner (ns.Current) is never in the queue;
// they trade during their own manage phase. Once the queue drains, the owner
// rolls (or handles jail). Skips emit nothing, so a table where nobody uses the
// window plays out — and replays — exactly as if the window did not exist.

// otherActiveSeats lists the non-bankrupt seats other than `cur`, in seat order.
func otherActiveSeats(s *State, cur int) []int {
	var out []int
	for i := range s.Players {
		if i != cur && !s.Players[i].Bankrupt {
			out = append(out, i)
		}
	}
	return out
}

// enterTurn is called when a fresh turn begins for ns.Current. If any other
// player is still in the game, it opens the trade window for them; otherwise it
// goes straight to play (roll or jail).
func (e *Engine) enterTurn(ns *State) {
	others := otherActiveSeats(ns, ns.Current)
	if len(others) > 0 {
		ns.TradeQueue = others
		ns.TradeReturn = ""
		ns.Phase = PhaseTrade
		return
	}
	e.enterPlay(ns)
}

// enterPlay closes any window and drops the turn owner into their own play phase.
func (e *Engine) enterPlay(ns *State) {
	ns.TradeQueue = nil
	ns.TradeReturn = ""
	if ns.Players[ns.Current].InJail {
		ns.Phase = PhaseJail
	} else {
		ns.Phase = PhaseRoll
	}
}

// stepTradeWindow handles the open-floor window: the pending player either opens
// a trade (which runs the normal accept/reject/counter negotiation and then
// returns here) or skips, advancing to the next player — and to play once the
// queue is empty.
func (e *Engine) stepTradeWindow(ns *State, a Action) ([]Event, error) {
	if len(ns.TradeQueue) == 0 {
		e.enterPlay(ns)
		return nil, nil
	}
	proposer := ns.TradeQueue[0]
	switch a.Kind {
	case ActSkipTrade:
		ns.TradeQueue = ns.TradeQueue[1:]
		if len(ns.TradeQueue) == 0 {
			e.enterPlay(ns)
		}
		return nil, nil
	case ActProposeTrade:
		if a.Trade == nil {
			return nil, ErrIllegalAction
		}
		t := *a.Trade
		t.Proposer = proposer
		if err := e.validateTrade(ns, t); err != nil {
			return nil, err
		}
		t.GiveProps = append([]int(nil), a.Trade.GiveProps...)
		t.WantProps = append([]int(nil), a.Trade.WantProps...)
		ns.PendingTrade = &t
		ns.Phase = PhaseTradeResponse
		ns.TradeCounters = 0
		ns.TradeReturn = PhaseTrade // resume the window after this negotiation
		return []Event{e.emit(ns, EvTradeProposed, tradePayload(t))}, nil
	default:
		return nil, ErrIllegalAction
	}
}

// resumeAfterTrade returns control after a negotiation resolves. From a manage
// proposal it goes back to the owner's manage phase; from a window proposal it
// pops that proposer and continues the window (or begins play when it drains).
func (e *Engine) resumeAfterTrade(ns *State) {
	ret := ns.TradeReturn
	ns.TradeCounters = 0
	ns.TradeReturn = ""
	if ret == PhaseTrade {
		if len(ns.TradeQueue) > 0 {
			ns.TradeQueue = ns.TradeQueue[1:]
		}
		if len(ns.TradeQueue) > 0 {
			ns.Phase = PhaseTrade
		} else {
			e.enterPlay(ns)
		}
		return
	}
	ns.Phase = PhaseManage
}

// executeTrade swaps the agreed properties and nets the cash. Mortgaged properties
// carry their mortgage to the new owner, who owes the bank 10% interest for assuming
// it (official rule). Returns the interest cash-change events, if any.
func (e *Engine) executeTrade(ns *State, t Trade) []Event {
	for _, idx := range t.GiveProps {
		ns.Holdings[idx].Owner = t.Target
	}
	for _, idx := range t.WantProps {
		ns.Holdings[idx].Owner = t.Proposer
	}
	ns.Players[t.Proposer].Cash += t.WantCash - t.GiveCash
	ns.Players[t.Target].Cash += t.GiveCash - t.WantCash
	// Get-out-of-jail-free cards change hands too.
	ns.Players[t.Proposer].JailCards += t.WantCards - t.GiveCards
	ns.Players[t.Target].JailCards += t.GiveCards - t.WantCards
	// Mortgage-transfer interest: the RECEIVER of each mortgaged property pays 10%.
	var evs []Event
	for _, idx := range t.GiveProps { // received by Target
		evs = append(evs, e.chargeTransferInterest(ns, t.Target, idx)...)
	}
	for _, idx := range t.WantProps { // received by Proposer
		evs = append(evs, e.chargeTransferInterest(ns, t.Proposer, idx)...)
	}
	return evs
}

// mortgageInterest is the 10% bank interest due when a mortgaged property changes
// hands, rounded up to match the unmortgage cost (doUnmortgage / unmortgageCost).
func mortgageInterest(pos int) int {
	base := space(pos).MortgageValue()
	return (base + 9) / 10
}

// chargeTransferInterest bills `seat` the mortgage-transfer interest for assuming the
// (mortgaged) property at pos — the official rule on any transfer, trade or bankruptcy
// estate. Deducted from cash, floored at available cash so the interest alone can never
// push a receiver negative; emits a cash-change event only when something is charged.
// No-op for an unmortgaged property.
func (e *Engine) chargeTransferInterest(ns *State, seat, pos int) []Event {
	if !ns.Holdings[pos].Mortgaged {
		return nil
	}
	due := mortgageInterest(pos)
	if due > ns.Players[seat].Cash {
		due = ns.Players[seat].Cash
	}
	if due <= 0 {
		return nil
	}
	ns.Players[seat].Cash -= due
	return []Event{e.emit(ns, EvCashChanged, CashChangedPayload{Seat: seat, Delta: -due, Balance: ns.Players[seat].Cash, Reason: "mortgage_interest"})}
}

func tradePayload(t Trade) TradePayload {
	return TradePayload{
		Proposer: t.Proposer, Target: t.Target,
		GiveProps: append([]int(nil), t.GiveProps...), GiveCash: t.GiveCash, GiveCards: t.GiveCards,
		WantProps: append([]int(nil), t.WantProps...), WantCash: t.WantCash, WantCards: t.WantCards,
	}
}

// ── Movement & landing ─────────────────────────────────────────────────────

func (e *Engine) moveBySteps(ns *State, evs []Event, seat, steps int) []Event {
	p := &ns.Players[seat]
	from := p.Position
	passed := from+steps >= BoardSize
	p.Position = (from + steps) % BoardSize
	evs = append(evs, e.emit(ns, EvMoved, MovedPayload{Seat: seat, From: from, To: p.Position, PassedGo: passed}))
	if passed {
		evs = append(evs, e.credit(ns, seat, e.cfg.GoSalary, "go_salary"))
	}
	return evs
}

func (e *Engine) moveToIndex(ns *State, evs []Event, seat, dest int, collectGo bool) []Event {
	p := &ns.Players[seat]
	from := p.Position
	steps := (dest - from + BoardSize) % BoardSize
	passed := collectGo && steps != 0 && from+steps >= BoardSize
	p.Position = dest
	evs = append(evs, e.emit(ns, EvMoved, MovedPayload{Seat: seat, From: from, To: dest, PassedGo: passed}))
	if passed {
		evs = append(evs, e.credit(ns, seat, e.cfg.GoSalary, "go_salary"))
	}
	return evs
}

// resolveLanding applies the rules of the square the current player now occupies.
// It sets the next phase (PhaseManage, PhaseAcquire, or PhaseResolveDebt) or, for
// "go to jail", jails the player.
func (e *Engine) resolveLanding(ns *State, evs []Event, seed []byte) []Event {
	seat := ns.Current
	pos := ns.Players[seat].Position
	sp := space(pos)
	switch sp.Kind {
	case KindGo, KindJail:
		ns.Phase = PhaseManage
		return evs
	case KindFreeParking:
		if e.cfg.FreeParkingPool && ns.FreeParkingPot > 0 {
			amt := ns.FreeParkingPot
			ns.FreeParkingPot = 0
			evs = append(evs, e.credit(ns, seat, amt, "free_parking"))
		}
		ns.Phase = PhaseManage
		return evs
	case KindGoToJail:
		return e.goToJail(ns, evs, seat, "go_to_jail_space")
	case KindTax:
		paid, cev := e.chargeBank(ns, seat, sp.Tax, "tax")
		evs = append(evs, cev...)
		if e.cfg.FreeParkingPool && paid {
			ns.FreeParkingPot += sp.Tax
		}
		if ns.Phase != PhaseResolveDebt {
			ns.Phase = PhaseManage
		}
		return evs
	case KindChance:
		ns.Phase = PhaseManage
		return e.drawChance(ns, evs, seed)
	case KindCommunityChest:
		ns.Phase = PhaseManage
		return e.drawCommunityChest(ns, evs, seed)
	case KindStreet, KindRailroad, KindUtility:
		return e.resolveProperty(ns, evs, seat, pos, ns.LastRoll[0]+ns.LastRoll[1])
	}
	ns.Phase = PhaseManage
	return evs
}

func (e *Engine) resolveProperty(ns *State, evs []Event, seat, pos, diceTotal int) []Event {
	h := ns.Holdings[pos]
	if h.Owner == Bank {
		ns.Phase = PhaseAcquire
		return evs
	}
	if h.Owner == seat || h.Mortgaged {
		ns.Phase = PhaseManage
		return evs
	}
	rent := e.rentFor(ns, pos, diceTotal)
	cev, _ := e.charge(ns, seat, h.Owner, rent, "rent", pos)
	evs = append(evs, cev...)
	if ns.Phase != PhaseResolveDebt {
		ns.Phase = PhaseManage
	}
	return evs
}

// rentFor computes the rent owed for landing on an owned, unmortgaged square.
func (e *Engine) rentFor(ns *State, pos, diceTotal int) int {
	sp := space(pos)
	h := ns.Holdings[pos]
	switch sp.Kind {
	case KindStreet:
		if h.Houses == 0 {
			if ns.ownsFullGroup(h.Owner, sp.Group) {
				return sp.Rent[0] * 2
			}
			return sp.Rent[0]
		}
		return sp.Rent[h.Houses]
	case KindRailroad:
		return railroadRentTable[ns.railroadsOwned(h.Owner)]
	case KindUtility:
		mult := 4
		if ns.utilitiesOwned(h.Owner) == 2 {
			mult = 10
		}
		return mult * diceTotal
	}
	return 0
}

// resolveUtilityCard / resolveRailroadCard handle the special rents triggered by
// the "advance to nearest ..." cards (10x a fresh roll; double railroad rent).
func (e *Engine) resolveUtilityCard(ns *State, evs []Event, seed []byte) []Event {
	seat := ns.Current
	pos := ns.Players[seat].Position
	h := ns.Holdings[pos]
	if h.Owner == Bank {
		ns.Phase = PhaseAcquire
		return evs
	}
	if h.Owner == seat || h.Mortgaged {
		ns.Phase = PhaseManage
		return evs
	}
	d1, d2 := rollDice(seed, ns.RollSeq)
	ns.RollSeq++
	evs = append(evs, e.emit(ns, EvDiceRolled, DiceRolledPayload{Seat: seat, Die1: d1, Die2: d2, Total: d1 + d2, Doubles: d1 == d2}))
	cev, _ := e.charge(ns, seat, h.Owner, 10*(d1+d2), "rent", pos)
	evs = append(evs, cev...)
	if ns.Phase != PhaseResolveDebt {
		ns.Phase = PhaseManage
	}
	return evs
}

func (e *Engine) resolveRailroadCard(ns *State, evs []Event, seed []byte) []Event {
	seat := ns.Current
	pos := ns.Players[seat].Position
	h := ns.Holdings[pos]
	if h.Owner == Bank {
		ns.Phase = PhaseAcquire
		return evs
	}
	if h.Owner == seat || h.Mortgaged {
		ns.Phase = PhaseManage
		return evs
	}
	rent := 2 * railroadRentTable[ns.railroadsOwned(h.Owner)]
	cev, _ := e.charge(ns, seat, h.Owner, rent, "rent", pos)
	evs = append(evs, cev...)
	if ns.Phase != PhaseResolveDebt {
		ns.Phase = PhaseManage
	}
	return evs
}

func (e *Engine) goToJail(ns *State, evs []Event, seat int, reason string) []Event {
	p := &ns.Players[seat]
	p.Position = IdxJail
	p.InJail = true
	p.JailTurns = 0
	p.Doubles = 0
	ns.Phase = PhaseManage // turn ends from here (LegalActions still allows managing)
	return append(evs, e.emit(ns, EvWentToJail, WentToJailPayload{Seat: seat, Reason: reason}))
}

// ── Money ──────────────────────────────────────────────────────────────────

func (e *Engine) credit(ns *State, seat, amount int, reason string) Event {
	ns.Players[seat].Cash += amount
	return e.emit(ns, EvCashChanged, CashChangedPayload{Seat: seat, Delta: amount, Balance: ns.Players[seat].Cash, Reason: reason})
}

// charge moves `amount` from payer to creditor (Bank == the bank). If the payer
// cannot cover it from cash, a Debt is opened and the engine enters
// PhaseResolveDebt; the payment event is emitted later by settleDebt.
func (e *Engine) charge(ns *State, payer, creditor, amount int, reason string, property int) ([]Event, bool) {
	if amount <= 0 {
		return nil, true
	}
	if ns.Players[payer].Cash >= amount {
		ns.Players[payer].Cash -= amount
		if creditor == Bank {
			return []Event{e.emit(ns, EvCashChanged, CashChangedPayload{Seat: payer, Delta: -amount, Balance: ns.Players[payer].Cash, Reason: reason})}, true
		}
		ns.Players[creditor].Cash += amount
		return []Event{e.emit(ns, EvRentPaid, RentPaidPayload{From: payer, To: creditor, Property: property, Amount: amount})}, true
	}
	ns.Debt = &Debt{Debtor: payer, Creditor: creditor, Amount: amount, Property: property, Reason: reason}
	ns.Phase = PhaseResolveDebt
	return nil, false
}

func (e *Engine) chargeBank(ns *State, seat, amount int, reason string) (bool, []Event) {
	evs, paid := e.charge(ns, seat, Bank, amount, reason, -1)
	return paid, evs
}

func (e *Engine) collectFromEach(ns *State, evs []Event, seat, amount int) []Event {
	for _, o := range ns.activeSeats() {
		if o == seat {
			continue
		}
		pay := amount
		if ns.Players[o].Cash < pay {
			pay = ns.Players[o].Cash
		}
		if pay <= 0 {
			continue
		}
		ns.Players[o].Cash -= pay
		ns.Players[seat].Cash += pay
		evs = append(evs, e.emit(ns, EvRentPaid, RentPaidPayload{From: o, To: seat, Property: -1, Amount: pay}))
	}
	return evs
}

func (e *Engine) payEach(ns *State, evs []Event, seat, amount int) []Event {
	others := 0
	for _, o := range ns.activeSeats() {
		if o != seat {
			others++
		}
	}
	total := amount * others
	if ns.Players[seat].Cash >= total {
		for _, o := range ns.activeSeats() {
			if o == seat {
				continue
			}
			ns.Players[seat].Cash -= amount
			ns.Players[o].Cash += amount
			evs = append(evs, e.emit(ns, EvRentPaid, RentPaidPayload{From: seat, To: o, Property: -1, Amount: amount}))
		}
		return evs
	}
	// Shortfall: route to a single bank debt (documented simplification).
	ns.Debt = &Debt{Debtor: seat, Creditor: Bank, Amount: total, Property: -1, Reason: "card_pay_each"}
	ns.Phase = PhaseResolveDebt
	return evs
}

// settleDebt pays the open debt in full (the debtor is known to have the cash),
// then resumes any pending continuation (a jail forced-move) or returns to manage.
func (e *Engine) settleDebt(ns *State, evs []Event, seed []byte) []Event {
	d := ns.Debt
	payer := d.Debtor
	ns.Players[payer].Cash -= d.Amount
	if d.Creditor == Bank {
		evs = append(evs, e.emit(ns, EvCashChanged, CashChangedPayload{Seat: payer, Delta: -d.Amount, Balance: ns.Players[payer].Cash, Reason: d.Reason}))
		if e.cfg.FreeParkingPool && d.Reason == "tax" {
			ns.FreeParkingPot += d.Amount
		}
	} else {
		ns.Players[d.Creditor].Cash += d.Amount
		evs = append(evs, e.emit(ns, EvRentPaid, RentPaidPayload{From: payer, To: d.Creditor, Property: d.Property, Amount: d.Amount}))
	}
	ns.Debt = nil
	// Clear the (now-settled) debt phase BEFORE running any continuation, so
	// resolveLanding starts from a clean phase and only re-enters resolve_debt if a
	// brand-new charge cannot be met. Without this, a stale resolve_debt phase would
	// survive a successful rent payment, leaving Phase=resolve_debt with a nil Debt.
	ns.Phase = PhaseManage

	if ns.PendingJailMove > 0 {
		steps := ns.PendingJailMove
		ns.PendingJailMove = 0
		p := &ns.Players[payer]
		p.InJail = false
		p.JailTurns = 0
		evs = append(evs, e.emit(ns, EvLeftJail, LeftJailPayload{Seat: payer, Method: "forced"}))
		evs = e.moveBySteps(ns, evs, payer, steps)
		evs = e.resolveLanding(ns, evs, seed) // sets manage / acquire / resolve_debt as appropriate
		ns.Players[payer].Doubles = 0
	}
	return evs
}

// declareBankrupt liquidates the debtor: buildings are sold to the bank for half
// value, then all cash + properties + jail cards transfer to the creditor. If the
// creditor is the BANK, the debtor's properties are auctioned to the surviving players
// (official rule) before the turn ends, rather than silently returning to the bank. The
// seat is eliminated; the game ends if only one player remains.
func (e *Engine) declareBankrupt(ns *State) []Event {
	d := ns.Debt
	debtor := d.Debtor
	creditor := d.Creditor
	var evs []Event
	var estate []int // debtor's properties to auction when the creditor is the bank

	// 1. Sell all buildings back to the bank for half value.
	for idx := 0; idx < BoardSize; idx++ {
		h := ns.Holdings[idx]
		if h.Owner != debtor || h.Houses == 0 {
			continue
		}
		sp := space(idx)
		if h.Houses == 5 {
			ns.HotelsRemaining++
		} else {
			ns.HousesRemaining += h.Houses
		}
		refund := (sp.HouseCost / 2) * h.Houses
		ns.Players[debtor].Cash += refund
		h.Houses = 0
		ns.Holdings[idx] = h
		if refund > 0 {
			evs = append(evs, e.emit(ns, EvCashChanged, CashChangedPayload{Seat: debtor, Delta: refund, Balance: ns.Players[debtor].Cash, Reason: "liquidate"}))
		}
	}

	// 2. Hand over remaining cash.
	cash := ns.Players[debtor].Cash
	ns.Players[debtor].Cash = 0
	if creditor != Bank && cash > 0 {
		ns.Players[creditor].Cash += cash
		evs = append(evs, e.emit(ns, EvCashChanged, CashChangedPayload{Seat: creditor, Delta: cash, Balance: ns.Players[creditor].Cash, Reason: "bankruptcy_estate"}))
	}

	// 3. Transfer properties. To a player-creditor: mortgages carry over and the
	// creditor owes the bank 10% interest for assuming each mortgaged property
	// (official rule). To the bank: the property returns unimproved and is queued for
	// auction to the survivors (in ascending board order, deterministically).
	for idx := 0; idx < BoardSize; idx++ {
		h := ns.Holdings[idx]
		if h.Owner != debtor {
			continue
		}
		if creditor != Bank {
			h.Owner = creditor
			ns.Holdings[idx] = h
			evs = append(evs, e.chargeTransferInterest(ns, creditor, idx)...)
		} else {
			ns.Holdings[idx] = Holding{Owner: Bank}
			estate = append(estate, idx)
		}
	}

	// 4. Jail cards and elimination.
	if creditor != Bank {
		ns.Players[creditor].JailCards += ns.Players[debtor].JailCards
	}
	ns.Players[debtor].JailCards = 0
	ns.Players[debtor].Bankrupt = true
	ns.Players[debtor].InJail = false
	ns.Debt = nil
	ns.PendingJailMove = 0
	// Surface WHY the seat left the market (captured from d before Debt was
	// cleared): the unpayable amount, the triggering square, and the reason.
	// This flows to spectators' logs and, via the push-play /event stream, to
	// the other agents so they can reason about eliminations.
	evs = append(evs, e.emit(ns, EvBankrupt, BankruptPayload{
		Seat: debtor, Creditor: creditor,
		Amount: d.Amount, Property: d.Property, Reason: d.Reason,
	}))

	if ns.activeCount() <= 1 {
		return e.finish(ns, evs)
	}
	// Bank-creditor estate → auction it to the survivors, then end the turn once the
	// queue drains (closeAuction handles the sequencing + the final endTurn).
	if creditor == Bank && len(estate) > 0 {
		ns.EstateQueue = estate[1:]
		return append(evs, e.startAuction(ns, estate[0], true)...)
	}
	return e.endTurn(ns, evs)
}

// ── Build / sell / mortgage ────────────────────────────────────────────────

func (e *Engine) doBuild(ns *State, pos int) ([]Event, error) {
	if pos < 0 || pos >= BoardSize {
		return nil, ErrInvalidProperty
	}
	sp := space(pos)
	h := ns.Holdings[pos]
	seat := ns.Current
	if sp.Kind != KindStreet || h.Owner != seat || h.Houses >= 5 {
		return nil, ErrIllegalAction
	}
	if !ns.ownsFullGroup(seat, sp.Group) {
		return nil, ErrIllegalAction
	}
	for _, idx := range groupMembers[sp.Group] {
		if ns.Holdings[idx].Mortgaged {
			return nil, ErrIllegalAction
		}
	}
	if h.Houses != minHousesInGroup(ns, sp.Group) { // even-build rule
		return nil, ErrIllegalAction
	}
	if h.Houses < 4 {
		if ns.HousesRemaining <= 0 {
			return nil, ErrIllegalAction
		}
	} else if ns.HotelsRemaining <= 0 {
		return nil, ErrIllegalAction
	}
	if ns.Players[seat].Cash < sp.HouseCost {
		return nil, ErrInsufficientFunds
	}
	ns.Players[seat].Cash -= sp.HouseCost
	if h.Houses < 4 {
		ns.HousesRemaining--
	} else {
		ns.HousesRemaining += 4 // four houses return to the bank when a hotel is built
		ns.HotelsRemaining--
	}
	h.Houses++
	ns.Holdings[pos] = h
	return []Event{
		e.emit(ns, EvCashChanged, CashChangedPayload{Seat: seat, Delta: -sp.HouseCost, Balance: ns.Players[seat].Cash, Reason: "build"}),
		e.emit(ns, EvHouseBuilt, BuildPayload{Seat: seat, Property: pos, Houses: h.Houses}),
	}, nil
}

func (e *Engine) doSellHouse(ns *State, pos int) ([]Event, error) {
	if pos < 0 || pos >= BoardSize {
		return nil, ErrInvalidProperty
	}
	sp := space(pos)
	h := ns.Holdings[pos]
	seat := ns.Current
	if sp.Kind != KindStreet || h.Owner != seat || h.Houses == 0 {
		return nil, ErrIllegalAction
	}
	if h.Houses != maxHousesInGroup(ns, sp.Group) { // even-sell rule
		return nil, ErrIllegalAction
	}
	refund := sp.HouseCost / 2
	if h.Houses == 5 {
		if ns.HousesRemaining < 4 {
			return nil, ErrIllegalAction // cannot break the hotel without four houses in the bank
		}
		ns.HousesRemaining -= 4
		ns.HotelsRemaining++
		h.Houses = 4
	} else {
		ns.HousesRemaining++
		h.Houses--
	}
	ns.Holdings[pos] = h
	ns.Players[seat].Cash += refund
	return []Event{
		e.emit(ns, EvCashChanged, CashChangedPayload{Seat: seat, Delta: refund, Balance: ns.Players[seat].Cash, Reason: "sell_house"}),
		e.emit(ns, EvHouseSold, BuildPayload{Seat: seat, Property: pos, Houses: h.Houses}),
	}, nil
}

func (e *Engine) doMortgage(ns *State, pos int) ([]Event, error) {
	if pos < 0 || pos >= BoardSize {
		return nil, ErrInvalidProperty
	}
	sp := space(pos)
	h := ns.Holdings[pos]
	seat := ns.Current
	if !sp.Ownable() || h.Owner != seat || h.Mortgaged {
		return nil, ErrIllegalAction
	}
	for _, idx := range groupMembers[sp.Group] { // no buildings anywhere in the group
		if ns.Holdings[idx].Houses > 0 {
			return nil, ErrIllegalAction
		}
	}
	amt := sp.MortgageValue()
	h.Mortgaged = true
	ns.Holdings[pos] = h
	ns.Players[seat].Cash += amt
	return []Event{
		e.emit(ns, EvCashChanged, CashChangedPayload{Seat: seat, Delta: amt, Balance: ns.Players[seat].Cash, Reason: "mortgage"}),
		e.emit(ns, EvMortgaged, MortgagePayload{Seat: seat, Property: pos, Amount: amt}),
	}, nil
}

func (e *Engine) doUnmortgage(ns *State, pos int) ([]Event, error) {
	if pos < 0 || pos >= BoardSize {
		return nil, ErrInvalidProperty
	}
	sp := space(pos)
	h := ns.Holdings[pos]
	seat := ns.Current
	if !sp.Ownable() || h.Owner != seat || !h.Mortgaged {
		return nil, ErrIllegalAction
	}
	base := sp.MortgageValue()
	cost := base + (base+9)/10 // mortgage value + 10% interest, rounded up
	if ns.Players[seat].Cash < cost {
		return nil, ErrInsufficientFunds
	}
	h.Mortgaged = false
	ns.Holdings[pos] = h
	ns.Players[seat].Cash -= cost
	return []Event{
		e.emit(ns, EvCashChanged, CashChangedPayload{Seat: seat, Delta: -cost, Balance: ns.Players[seat].Cash, Reason: "unmortgage"}),
		e.emit(ns, EvUnmortgaged, MortgagePayload{Seat: seat, Property: pos, Amount: cost}),
	}, nil
}

// ── Turn lifecycle ─────────────────────────────────────────────────────────

// endTurn closes the current turn: it re-rolls for the same player on doubles,
// otherwise advances to the next solvent player. The turn cap ends the game.
func (e *Engine) endTurn(ns *State, evs []Event) []Event {
	cur := ns.Current
	evs = append(evs, e.emit(ns, EvTurnEnded, TurnEndedPayload{Seat: cur}))

	if ns.TurnCount >= e.cfg.MaxTurns {
		return e.finish(ns, evs)
	}

	p := &ns.Players[cur]
	if !p.Bankrupt && !p.InJail && p.Doubles > 0 && p.Doubles < 3 {
		ns.Phase = PhaseRoll
		return append(evs, e.emit(ns, EvTurnStarted, TurnStartedPayload{Seat: cur, TurnCount: ns.TurnCount}))
	}

	p.Doubles = 0
	next := ns.nextActiveSeat(cur)
	ns.Current = next
	evs = append(evs, e.emit(ns, EvTurnStarted, TurnStartedPayload{Seat: next, TurnCount: ns.TurnCount}))
	e.enterTurn(ns) // open the trade window for the other players, then play
	return evs
}

// finish ends the match, ranking by survival then net worth.
func (e *Engine) finish(ns *State, evs []Event) []Event {
	ns.Finished = true
	ns.Phase = PhaseGameOver
	nets := make([]int, len(ns.Players))
	for i := range ns.Players {
		nets[i] = ns.NetWorth(i)
	}
	active := ns.activeSeats()
	winner := Tie
	if len(active) == 1 {
		winner = active[0]
	} else if len(active) > 1 {
		best, bestNet, tie := -1, -1, false
		for _, seat := range active {
			switch {
			case nets[seat] > bestNet:
				best, bestNet, tie = seat, nets[seat], false
			case nets[seat] == bestNet:
				tie = true
			}
		}
		if !tie {
			winner = best
		}
	}
	ns.Winner = winner
	return append(evs, e.emit(ns, EvMatchFinished, MatchFinishedPayload{Winner: winner, NetWorths: nets}))
}

// ── Small helpers ──────────────────────────────────────────────────────────

func minHousesInGroup(ns *State, group string) int {
	min := 6
	for _, idx := range groupMembers[group] {
		if h := ns.Holdings[idx].Houses; h < min {
			min = h
		}
	}
	return min
}

func maxHousesInGroup(ns *State, group string) int {
	max := 0
	for _, idx := range groupMembers[group] {
		if h := ns.Holdings[idx].Houses; h > max {
			max = h
		}
	}
	return max
}

func countTrue(bs []bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}
