package monopoly

// cards.go holds the Chance and Community Chest decks as immutable data plus the
// deterministic resolution of a drawn card. Decks are seed-shuffled (see rng.go)
// and drawn top-to-bottom with wrap-around, so the full sequence of cards is a
// reproducible function of the match seed.

type cardKind string

const (
	ckCollect     cardKind = "collect"      // bank pays Amount
	ckPay         cardKind = "pay"          // pay bank Amount
	ckMoveTo      cardKind = "move_to"      // advance to Dest (collect GO if passed)
	ckGoToJail    cardKind = "go_to_jail"   // straight to jail, no GO
	ckJailCard    cardKind = "jail_card"    // keep a get-out-of-jail-free card
	ckBack3       cardKind = "back3"        // move back three squares (no GO)
	ckNearestUtil cardKind = "nearest_util" // advance to nearest utility
	ckNearestRail cardKind = "nearest_rail" // advance to nearest railroad
	ckRepairs     cardKind = "repairs"      // pay PerHouse/PerHotel for buildings
	ckCollectEach cardKind = "collect_each" // collect Amount from each other player
	ckPayEach     cardKind = "pay_each"     // pay Amount to each other player
)

type card struct {
	Text     string
	Kind     cardKind
	Amount   int
	Dest     int
	PerHouse int
	PerHotel int
}

// chanceDeck is the canonical 16-card Chance deck. Card id == slice index.
var chanceDeck = []card{
	{Text: "Advance to GO. Collect $200.", Kind: ckMoveTo, Dest: 0},
	{Text: "Advance to Illinois Avenue. If you pass GO, collect $200.", Kind: ckMoveTo, Dest: 24},
	{Text: "Advance to St. Charles Place. If you pass GO, collect $200.", Kind: ckMoveTo, Dest: 11},
	{Text: "Advance to the nearest Utility. If owned, pay 10x the dice.", Kind: ckNearestUtil},
	{Text: "Advance to the nearest Railroad. If owned, pay double rent.", Kind: ckNearestRail},
	{Text: "Advance to Reading Railroad. If you pass GO, collect $200.", Kind: ckMoveTo, Dest: 5},
	{Text: "Bank pays you a dividend of $50.", Kind: ckCollect, Amount: 50},
	{Text: "Get Out of Jail Free. Keep this card.", Kind: ckJailCard},
	{Text: "Go back three spaces.", Kind: ckBack3},
	{Text: "Go to Jail. Go directly to Jail. Do not pass GO.", Kind: ckGoToJail},
	{Text: "Make general repairs: $25 per house, $100 per hotel.", Kind: ckRepairs, PerHouse: 25, PerHotel: 100},
	{Text: "Speeding fine. Pay $15.", Kind: ckPay, Amount: 15},
	{Text: "Advance to Boardwalk.", Kind: ckMoveTo, Dest: 39},
	{Text: "You have been elected Chairman of the Board. Pay each player $50.", Kind: ckPayEach, Amount: 50},
	{Text: "Your building loan matures. Collect $150.", Kind: ckCollect, Amount: 150},
	{Text: "You won a crossword competition. Collect $100.", Kind: ckCollect, Amount: 100},
}

// ccDeck is the canonical 16-card Community Chest deck. Card id == slice index.
var ccDeck = []card{
	{Text: "Advance to GO. Collect $200.", Kind: ckMoveTo, Dest: 0},
	{Text: "Bank error in your favor. Collect $200.", Kind: ckCollect, Amount: 200},
	{Text: "Doctor's fee. Pay $50.", Kind: ckPay, Amount: 50},
	{Text: "From sale of stock you get $50.", Kind: ckCollect, Amount: 50},
	{Text: "Get Out of Jail Free. Keep this card.", Kind: ckJailCard},
	{Text: "Go to Jail. Go directly to Jail. Do not pass GO.", Kind: ckGoToJail},
	{Text: "Holiday fund matures. Collect $100.", Kind: ckCollect, Amount: 100},
	{Text: "Income tax refund. Collect $20.", Kind: ckCollect, Amount: 20},
	{Text: "It is your birthday. Collect $10 from every player.", Kind: ckCollectEach, Amount: 10},
	{Text: "Life insurance matures. Collect $100.", Kind: ckCollect, Amount: 100},
	{Text: "Hospital fees. Pay $100.", Kind: ckPay, Amount: 100},
	{Text: "School fees. Pay $50.", Kind: ckPay, Amount: 50},
	{Text: "Receive $25 consultancy fee.", Kind: ckCollect, Amount: 25},
	{Text: "You are assessed for street repairs: $40 per house, $115 per hotel.", Kind: ckRepairs, PerHouse: 40, PerHotel: 115},
	{Text: "You won second prize in a beauty contest. Collect $10.", Kind: ckCollect, Amount: 10},
	{Text: "You inherit $100.", Kind: ckCollect, Amount: 100},
}

// drawChance / drawCommunityChest pull the next card from the seed-shuffled deck,
// advance the wrap-around index, emit card_drawn, and resolve the card's effect.
func (e *Engine) drawChance(s *State, evs []Event, seed []byte) []Event {
	id := s.ChanceOrder[s.ChanceIdx]
	s.ChanceIdx = (s.ChanceIdx + 1) % len(s.ChanceOrder)
	c := chanceDeck[id]
	evs = append(evs, e.emit(s, EvCardDrawn, CardDrawnPayload{Seat: s.Current, Deck: "chance", CardID: id, Text: c.Text}))
	return e.applyCard(s, evs, c, seed)
}

func (e *Engine) drawCommunityChest(s *State, evs []Event, seed []byte) []Event {
	id := s.CCOrder[s.CCIdx]
	s.CCIdx = (s.CCIdx + 1) % len(s.CCOrder)
	c := ccDeck[id]
	evs = append(evs, e.emit(s, EvCardDrawn, CardDrawnPayload{Seat: s.Current, Deck: "community_chest", CardID: id, Text: c.Text}))
	return e.applyCard(s, evs, c, seed)
}

// applyCard executes a drawn card's effect. It may move the player (and resolve
// the new landing), send them to jail, or trigger a payment that opens a debt.
func (e *Engine) applyCard(s *State, evs []Event, c card, seed []byte) []Event {
	seat := s.Current
	switch c.Kind {
	case ckCollect:
		return append(evs, e.credit(s, seat, c.Amount, "card"))
	case ckPay:
		_, ev := e.chargeBank(s, seat, c.Amount, "card")
		return append(evs, ev...)
	case ckJailCard:
		s.Players[seat].JailCards++
		return evs
	case ckGoToJail:
		return e.goToJail(s, evs, seat, "card")
	case ckMoveTo:
		evs = e.moveToIndex(s, evs, seat, c.Dest, true)
		return e.resolveLanding(s, evs, seed)
	case ckBack3:
		from := s.Players[seat].Position
		to := (from - 3 + BoardSize) % BoardSize
		s.Players[seat].Position = to
		evs = append(evs, e.emit(s, EvMoved, MovedPayload{Seat: seat, From: from, To: to, PassedGo: false}))
		return e.resolveLanding(s, evs, seed)
	case ckNearestUtil:
		evs = e.moveToIndex(s, evs, seat, nearestOf(s.Players[seat].Position, GroupUtility), true)
		return e.resolveUtilityCard(s, evs, seed)
	case ckNearestRail:
		evs = e.moveToIndex(s, evs, seat, nearestOf(s.Players[seat].Position, GroupRailroad), true)
		return e.resolveRailroadCard(s, evs, seed)
	case ckRepairs:
		cost := e.repairCost(s, seat, c.PerHouse, c.PerHotel)
		if cost == 0 {
			return evs
		}
		_, ev := e.chargeBank(s, seat, cost, "repairs")
		return append(evs, ev...)
	case ckCollectEach:
		return e.collectFromEach(s, evs, seat, c.Amount)
	case ckPayEach:
		return e.payEach(s, evs, seat, c.Amount)
	}
	return evs
}

// nearestOf returns the lowest-distance forward member of a pseudo-group from pos.
func nearestOf(pos int, group string) int {
	best, bestDist := pos, BoardSize+1
	for _, idx := range groupMembers[group] {
		d := (idx - pos + BoardSize) % BoardSize
		if d == 0 {
			d = BoardSize // standing on it still advances to the next one
		}
		if d < bestDist {
			best, bestDist = idx, d
		}
	}
	return best
}

// repairCost totals the per-building assessment across a seat's holdings.
func (e *Engine) repairCost(s *State, seat, perHouse, perHotel int) int {
	cost := 0
	for idx := 0; idx < BoardSize; idx++ {
		h := s.Holdings[idx]
		if h.Owner != seat {
			continue
		}
		switch {
		case h.Houses == 5:
			cost += perHotel
		case h.Houses > 0:
			cost += h.Houses * perHouse
		}
	}
	return cost
}

// ── Deck movement profile (for analysis, not play) ───────────────────────────

// DeckMovement is how often a deck relocates the player, expressed as card COUNTS.
//
// Exported for board analysis — solving the landing-probability chain needs to know how
// often a draw teleports a player, and that is a property of THIS deck, not of the
// canonical Rand McNally one. Deriving it here rather than transcribing it into the
// analysis keeps the two from drifting: edit a card and every consumer follows.
type DeckMovement struct {
	Size int // cards in the deck
	// MoveTo maps destination square → how many cards send the player there.
	MoveTo      map[int]int
	ToJail      int // straight to jail
	NearestRail int // advance to the nearest railroad
	NearestUtil int // advance to the nearest utility
	Back3       int // move back three squares
	// Stay is cards with no movement effect at all.
	Stay int
}

func movementOf(deck []card) DeckMovement {
	m := DeckMovement{Size: len(deck), MoveTo: map[int]int{}}
	for _, c := range deck {
		switch c.Kind {
		case ckMoveTo:
			m.MoveTo[c.Dest]++
		case ckGoToJail:
			m.ToJail++
		case ckNearestRail:
			m.NearestRail++
		case ckNearestUtil:
			m.NearestUtil++
		case ckBack3:
			m.Back3++
		default:
			m.Stay++
		}
	}
	return m
}

// ChanceMovement / ChestMovement describe the two decks actually in play.
func ChanceMovement() DeckMovement { return movementOf(chanceDeck) }
func ChestMovement() DeckMovement  { return movementOf(ccDeck) }
