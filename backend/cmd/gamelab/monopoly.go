package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
)

// Monopoly for the lab agent.
//
// # Why this file had to exist
//
// The lab advertised `supportedGames: ["goofspiel", "mafia", "monopoly"]` and implemented one of
// them. A Monopoly view hit the default branch of handlePlay, which answered `{}` and let the
// platform apply its legal fallback — so every lab Monopoly match was the SERVER playing itself
// while the logs read as though four agents were competing. Nothing errored, which is what made
// it survive: a forfeit and a decision are both a move on the wire.
//
// That is the same shape as the instrumentation bug where the driver was measured instead of the
// match, and it matters most right before a stress run. A load test against fallbacks measures
// the fallback path at full speed and reports it as agent throughput.
//
// # Why it reads the real board
//
// The prices, groups and rents come from mono.Board() rather than a table copied into the lab.
// A private copy is wrong the first time the engine rebalances anything, and it would be wrong
// silently — the agent would keep bidding confident numbers against a board that had moved.
// monopolyView MUST mirror internal/monopoly.MonopolyPushView field for field.
//
// It did not, and the cost was total: this struct read `your_seat` and `legal` while the
// platform sends `seat` and `legal_actions`. Every field silently decoded to its zero value,
// so `len(v.Legal) == 0` was true on every turn, the handler returned `{}` before it ever
// logged, and the platform's safeFallback played BOTH seats of every lab Monopoly match. A
// full match ran to GAME END with zero decision lines in the agent log.
//
// That is precisely the failure this file's header describes and claims to have fixed — it
// was fixed against the OLD wire shape, and the push protocol renamed the fields. The seat
// number is the worst of them: v.YourSeat indexes st.Players, so seat 1 would have evaluated
// the board as if it were seat 0 even had the rest decoded.
//
// monopolyWireTest pins this struct against the platform's own type. Do not rename a field
// here without renaming it there.
type monopolyView struct {
	MatchID string `json:"match_id"`
	// Seat, not your_seat. Indexes State.Players — a wrong value reads someone else's board.
	YourSeat int    `json:"seat"`
	Phase    string `json:"phase"`
	// legal_actions, not legal. An empty list means "no decision to make", so a name that
	// never matches is indistinguishable from a seat that has nothing to do.
	Legal []string    `json:"legal_actions"`
	State *mono.State `json:"state"`
	// Round is the engine's TurnCount, published by the platform so both sides agree on the
	// number the turn proof was minted for. Without it a bound call cannot verify.
	Round int `json:"round"`
	// TurnProof binds a gateway model call to THIS decision.
	TurnProof string `json:"turn_proof"`
}

func (a *labAgent) playMonopoly(w http.ResponseWriter, r *http.Request, raw []byte) {
	var v monopolyView
	if err := json.Unmarshal(raw, &v); err != nil || v.State == nil {
		http.Error(w, "bad view", http.StatusBadRequest)
		return
	}
	if len(v.Legal) == 0 {
		// Nothing is legal for this seat right now. An empty object is correct here and is NOT
		// the silent fallback this file replaced: there is genuinely no decision to make.
		writeJSON(w, map[string]any{})
		return
	}

	// GO DARK, same contract as Goofspiel: hold the connection open rather than erroring, so the
	// shot clock actually expires and the absence forfeit arms. An error would be answered
	// instantly and would exercise the wrong path.
	if GoDarkAfterRound > 0 && v.State.RollSeq >= GoDarkAfterRound &&
		(GoDarkSeat < 0 || GoDarkSeat == v.YourSeat) {
		a.log.Printf("monopoly: seat %d GONE DARK at roll %d", v.YourSeat, v.State.RollSeq)
		<-r.Context().Done()
		return
	}

	think := a.Persona.thinkTime(v.MatchID, v.State.RollSeq, v.YourSeat)
	time.Sleep(think)

	// BOUND PATH: ask the model through the gateway so the decision is completion-bound,
	// making this a real LLM agent rather than a scripted persona. Same contract as Mafia:
	// ANY failure falls back to the rule policy and SAYS SO, because a run that silently
	// played a scripted move while reporting a bound one measures nothing and claims success.
	var act map[string]any
	var why string
	if BindGatewayBase != "" {
		me := v.State.Players[v.YourSeat]
		prompt := fmt.Sprintf(
			"You are seat %d in Monopoly, phase %s, turn %d. You hold $%d.",
			v.YourSeat, v.Phase, v.Round, me.Cash)
		b, err := a.decideGameThroughGateway("monopoly", v.MatchID, v.Round, v.YourSeat,
			v.TurnProof, v.Legal, prompt)
		if err != nil {
			a.log.Printf("monopoly: seat %d BIND FAILED (%v) — falling back to the rule policy, "+
				"so THIS turn is not model-backed", v.YourSeat, err)
		} else {
			act = map[string]any{"kind": b.Kind}
			if b.Property != 0 {
				act["property"] = b.Property
			}
			if b.Amount != 0 {
				act["amount"] = b.Amount
			}
			why = "bound to the model's own " + b.Canon
		}
	}
	if act == nil {
		act, why = monopolyAction(a.Persona, v)
	}
	a.log.Printf("monopoly: seat %d  phase %-8s legal %v  → %s (%s)",
		v.YourSeat, v.Phase, v.Legal, act["kind"], why)

	act["rationale"] = why
	act["usage"] = a.usage(len(raw), think)
	writeJSON(w, monopolyWireMove(act))
}

// monopolyWireMove renames the engine-shaped action to what MonopolyPushMove reads.
//
// The lab builds actions with the engine's own vocabulary — `kind`, matching mono.Action —
// but the push protocol's move field is `action`. Sending `kind` meant Action decoded empty,
// the platform found it not in the legal list, and substituted a safe fallback: the second
// half of the same silent-fallback bug as the view above, and equally invisible because an
// illegal move and a considered one are both just a move on the wire.
//
// Translating here rather than renaming the key throughout keeps monopolyAction and its tests
// speaking the engine's language, which is what they are actually about.
func monopolyWireMove(act map[string]any) map[string]any {
	out := make(map[string]any, len(act))
	for k, v := range act {
		if k == "kind" {
			out["action"] = v
			continue
		}
		out[k] = v
	}
	return out
}

// monopolyAction picks a move. Returns the action object and the rationale the decision
// inspector shows.
//
// Every branch chooses from v.Legal and never invents a verb: the engine is the authority on
// what is playable, and a lab agent that guesses would be testing our guess rather than the
// contract an SDK agent has to satisfy.
func monopolyAction(p persona, v monopolyView) (map[string]any, string) {
	st := v.State
	me := st.Players[v.YourSeat]
	board := mono.Board()
	legal := legalSet(v.Legal)

	switch {
	// ── debt: raise cash or go under ─────────────────────────────────────────
	// Checked FIRST because a seat in debt may also have `build` legal, and building while
	// insolvent is how an agent bankrupts itself with money in its pocket.
	case legal[mono.ActSellHouse] || legal[mono.ActMortgage] || legal[mono.ActBankrupt]:
		owed := 0
		if st.Debt != nil {
			owed = st.Debt.Amount
		}
		if me.Cash >= owed && legal[mono.ActEndTurn] {
			return map[string]any{"kind": mono.ActEndTurn}, "debt already covered"
		}
		// Mortgage the cheapest undeveloped holding first: it raises cash while giving up the
		// least rent, and mortgaging a developed street would waste the houses on it.
		if legal[mono.ActMortgage] {
			if idx, ok := cheapestMortgageable(st, board, v.YourSeat); ok {
				return map[string]any{"kind": mono.ActMortgage, "property": idx},
					"mortgaging the cheapest undeveloped holding to cover the debt"
			}
		}
		if legal[mono.ActSellHouse] {
			if idx, ok := mostDevelopedOwned(st, board, v.YourSeat); ok {
				return map[string]any{"kind": mono.ActSellHouse, "property": idx},
					"selling a house — nothing left to mortgage"
			}
		}
		return map[string]any{"kind": mono.ActBankrupt}, "cannot raise the debt; conceding"

	// ── jail ─────────────────────────────────────────────────────────────────
	case legal[mono.ActUseJailCard]:
		return map[string]any{"kind": mono.ActUseJailCard}, "spending a held jail card — it costs nothing"
	case legal[mono.ActPayJail] || legal[mono.ActRollJail]:
		// Early game, get out and keep buying. Late game, jail is shelter: rent is the main
		// risk once the board is developed, and a jailed player pays none.
		developed := developedCount(st)
		if legal[mono.ActPayJail] && me.Cash > 200 && developed < 8 {
			return map[string]any{"kind": mono.ActPayJail}, "paying out early — board is still open to buy"
		}
		if legal[mono.ActRollJail] {
			return map[string]any{"kind": mono.ActRollJail}, "rolling for doubles; jail is cheap shelter on a developed board"
		}
		return map[string]any{"kind": mono.ActPayJail}, "paying the fine"

	// ── acquire ──────────────────────────────────────────────────────────────
	case legal[mono.ActBuy] || legal[mono.ActDecline]:
		sp := board[me.Position]
		if legal[mono.ActBuy] && wantsToBuy(p, st, board, v.YourSeat, me.Position) {
			return map[string]any{"kind": mono.ActBuy}, "buying " + sp.Name + " at list"
		}
		return map[string]any{"kind": mono.ActDecline}, "declining " + sp.Name + "; cash reserve matters more"

	// ── auction ──────────────────────────────────────────────────────────────
	case legal[mono.ActBid] || legal[mono.ActPass]:
		if st.Auction == nil {
			return map[string]any{"kind": mono.ActPass}, "no auction state; passing"
		}
		sp := board[st.Auction.Property]
		ceiling := bidCeiling(p, st, board, v.YourSeat, st.Auction.Property)
		next := st.Auction.HighBid + 10
		if legal[mono.ActBid] && next <= ceiling && next <= me.Cash {
			// Always a positive amount. An amount-less bid is exactly the mistake that used to
			// come back as "not legal in the current phase", and the lab must not reproduce it.
			return map[string]any{"kind": mono.ActBid, "amount": next},
				"raising on " + sp.Name + " — still under my valuation"
		}
		return map[string]any{"kind": mono.ActPass}, "passing on " + sp.Name + " — bidding past my valuation"

	// ── trade: open floor ────────────────────────────────────────────────────
	case legal[mono.ActSkipTrade]:
		// A proposal needs a full Trade payload; offering `propose_trade` with none is rejected,
		// which is precisely what the first version of this file did via its "first legal verb"
		// fallback. Propose only when a concrete, well-formed offer exists.
		if legal[mono.ActProposeTrade] {
			if tr, why, ok := buildTradeOffer(st, board, v.YourSeat); ok {
				return map[string]any{"kind": mono.ActProposeTrade, "trade": tr}, why
			}
		}
		return map[string]any{"kind": mono.ActSkipTrade}, "no offer worth making this window"

	// ── trade: responding ────────────────────────────────────────────────────
	case legal[mono.ActAcceptTrade] || legal[mono.ActRejectTrade]:
		if st.PendingTrade != nil && tradeIsGood(st, board, v.YourSeat, st.PendingTrade) && legal[mono.ActAcceptTrade] {
			return map[string]any{"kind": mono.ActAcceptTrade}, "accepting — the offer nets me a group I want"
		}
		return map[string]any{"kind": mono.ActRejectTrade}, "rejecting — the offer favours the proposer"

	// ── manage / roll ────────────────────────────────────────────────────────
	case legal[mono.ActBuild]:
		if idx, ok := bestBuild(st, board, v.YourSeat, me.Cash); ok {
			return map[string]any{"kind": mono.ActBuild, "property": idx},
				"building on " + board[idx].Name + " — completed group, cash to spare"
		}
		if legal[mono.ActEndTurn] {
			return map[string]any{"kind": mono.ActEndTurn}, "holding cash rather than over-building"
		}
	}

	// Prefer rolling over ending a turn: a roll is the only action that advances the game, and a
	// lab that ends its turn whenever both are legal produces matches that never resolve.
	if legal[mono.ActRoll] {
		return map[string]any{"kind": mono.ActRoll}, "rolling"
	}
	if legal[mono.ActEndTurn] {
		return map[string]any{"kind": mono.ActEndTurn}, "nothing worth doing this turn"
	}
	// Last resort for a phase this file has not been taught.
	//
	// Deliberately NOT "the first legal verb". That was the original fallback and it produced an
	// ILLEGAL action: in the trade window the first legal verb is propose_trade, which is only
	// valid with a Trade payload attached. A fallback that reaches for a verb needing arguments
	// it cannot supply turns an unknown phase into a rejected move.
	//
	// So: prefer the verbs that are complete on their own — declining, passing, ending — and only
	// then take whatever is left.
	for _, safe := range []string{mono.ActSkipTrade, mono.ActRejectTrade, mono.ActPass,
		mono.ActDecline, mono.ActEndTurn, mono.ActRoll} {
		if legal[safe] {
			return map[string]any{"kind": safe}, "unhandled phase " + v.Phase + "; taking the no-argument action " + safe
		}
	}
	return map[string]any{"kind": v.Legal[0]}, "unhandled phase " + v.Phase + "; no argument-free action available"
}

func legalSet(l []string) map[string]bool {
	m := make(map[string]bool, len(l))
	for _, k := range l {
		m[k] = true
	}
	return m
}

// wantsToBuy: take anything that completes or extends a group, and otherwise buy while a cash
// buffer survives the purchase. The buffer is what stops an agent owning the board and losing to
// the first rent it lands on.
func wantsToBuy(p persona, st *mono.State, board []mono.Space, seat, pos int) bool {
	sp := board[pos]
	if sp.Price == 0 || st.Players[seat].Cash < sp.Price {
		return false
	}
	reserve := 150
	if p.Style == "aggressive" {
		reserve = 60
	}
	if groupInterest(st, board, seat, sp.Group) > 0 {
		reserve /= 2 // already invested here; completing a group is worth more than the buffer
	}
	return st.Players[seat].Cash-sp.Price >= reserve
}

// bidCeiling values a property at list, plus a premium when it completes a group we already hold
// and a discount when a rival is closer to completing it than we are.
func bidCeiling(p persona, st *mono.State, board []mono.Space, seat, pos int) int {
	sp := board[pos]
	if sp.Price == 0 {
		return 0
	}
	ceiling := sp.Price
	if mine := groupInterest(st, board, seat, sp.Group); mine > 0 {
		ceiling = sp.Price * (100 + 40*mine) / 100
	}
	if p.Style == "aggressive" {
		ceiling = ceiling * 120 / 100
	}
	// Never bid past the cash on hand: the engine rejects it as insufficient funds, and an agent
	// that keeps proposing rejected bids stalls its own auction.
	if cash := st.Players[seat].Cash; ceiling > cash {
		ceiling = cash
	}
	return ceiling
}

// groupInterest counts how many squares of a colour group this seat already owns.
func groupInterest(st *mono.State, board []mono.Space, seat int, group string) int {
	if group == "" {
		return 0
	}
	n := 0
	for i, sp := range board {
		if sp.Group == group && i < len(st.Holdings) && st.Holdings[i].Owner == seat {
			n++
		}
	}
	return n
}

// bestBuild picks the square to develop: the completed, unmortgaged group with the fewest houses,
// so development spreads evenly instead of stacking one street (which the even-build rule forbids
// anyway, and which would have the engine reject the move).
func bestBuild(st *mono.State, board []mono.Space, seat, cash int) (int, bool) {
	best, bestHouses := -1, 99
	for i, sp := range board {
		if sp.Kind != mono.KindStreet || i >= len(st.Holdings) {
			continue
		}
		h := st.Holdings[i]
		if h.Owner != seat || h.Mortgaged || h.Houses >= 5 {
			continue
		}
		if groupInterest(st, board, seat, sp.Group) < groupSize(board, sp.Group) {
			continue // partial group: cannot build, and asking would be rejected
		}
		if cash-sp.HouseCost < 100 {
			continue // keep a rent buffer; a house is worthless if the next square bankrupts us
		}
		if h.Houses < bestHouses {
			best, bestHouses = i, h.Houses
		}
	}
	return best, best >= 0
}

func groupSize(board []mono.Space, group string) int {
	n := 0
	for _, sp := range board {
		if sp.Group == group {
			n++
		}
	}
	return n
}

// cheapestMortgageable: raise cash with the smallest loss of rent.
func cheapestMortgageable(st *mono.State, board []mono.Space, seat int) (int, bool) {
	best, bestPrice := -1, 1<<30
	for i, sp := range board {
		if i >= len(st.Holdings) || !sp.Ownable() {
			continue
		}
		h := st.Holdings[i]
		if h.Owner != seat || h.Mortgaged || h.Houses > 0 {
			continue // houses must be sold before a street can be mortgaged
		}
		if sp.Price < bestPrice {
			best, bestPrice = i, sp.Price
		}
	}
	return best, best >= 0
}

func mostDevelopedOwned(st *mono.State, board []mono.Space, seat int) (int, bool) {
	best, bestHouses := -1, 0
	for i := range board {
		if i >= len(st.Holdings) {
			continue
		}
		if h := st.Holdings[i]; h.Owner == seat && h.Houses > bestHouses {
			best, bestHouses = i, h.Houses
		}
	}
	return best, best >= 0
}

// developedCount is a rough measure of how far along the board is, used to decide whether jail is
// a cost or a shelter.
func developedCount(st *mono.State) int {
	n := 0
	for _, h := range st.Holdings {
		n += h.Houses
	}
	return n
}

// buildTradeOffer constructs a complete, well-formed offer, or reports that none is worth making.
//
// Returns ok=false rather than a half-filled Trade: the engine validates the payload, and an
// offer naming properties the proposer does not own is a rejected move, not a bad negotiation.
func buildTradeOffer(st *mono.State, board []mono.Space, seat int) (mono.Trade, string, bool) {
	me := st.Players[seat]
	if me.Cash < 200 {
		return mono.Trade{}, "", false // no cash to sweeten anything
	}
	// Look for a square that would COMPLETE a group for us and is owned by someone else.
	for i, sp := range board {
		if sp.Kind != mono.KindStreet || i >= len(st.Holdings) {
			continue
		}
		h := st.Holdings[i]
		if h.Owner == seat || h.Owner == mono.Bank || h.Houses > 0 {
			continue
		}
		if owner := h.Owner; owner >= 0 && owner < len(st.Players) && !st.Players[owner].Bankrupt {
			held := groupInterest(st, board, seat, sp.Group)
			if held == 0 || held+1 < groupSize(board, sp.Group) {
				continue // only chase the square that finishes the set
			}
			offer := sp.Price * 3 / 2
			if offer > me.Cash-100 {
				continue // would leave nothing to develop the group we just completed
			}
			return mono.Trade{
				Proposer: seat, Target: h.Owner,
				GiveCash: offer, WantProps: []int{i},
			}, "offering " + itoa(offer) + " for " + sp.Name + " — it completes my group", true
		}
	}
	return mono.Trade{}, "", false
}

// tradeIsGood accepts only offers that leave us better off: cash above the list price of what we
// give up, or a square that completes one of our own groups.
func tradeIsGood(st *mono.State, board []mono.Space, seat int, tr *mono.Trade) bool {
	if tr.Target != seat {
		return false
	}
	giveValue := 0
	for _, i := range tr.WantProps {
		if i >= 0 && i < len(board) {
			giveValue += board[i].Price
			if groupInterest(st, board, seat, board[i].Group) >= groupSize(board, board[i].Group) {
				return false // never break up a completed group
			}
		}
	}
	gainValue := tr.GiveCash
	for _, i := range tr.GiveProps {
		if i >= 0 && i < len(board) {
			gainValue += board[i].Price
		}
	}
	return gainValue > giveValue*5/4 // needs a clear premium, not a coin flip
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
