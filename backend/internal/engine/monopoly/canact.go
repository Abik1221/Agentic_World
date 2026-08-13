package monopoly

// Whether a seat CAN do a thing — asked by LegalActions and enforced by the doers.
//
// # Why these exist
//
// LegalActions used to answer PhaseManage with a fixed list:
//
//	[end_turn build sell_house mortgage unmortgage propose_trade]
//
// regardless of whether the seat owned a colour group, held a mortgage, or had the cash. So
// the engine told an agent `build` was legal on a board where nothing could be built, the
// agent chose it, and doBuild answered ErrIllegalAction.
//
// For a human that is a shrug. For an LLM agent it is a wasted decision AND a wasted model
// call, on a rate-limited key — and the lab's own agent is written on the promise that "the
// engine is the authority on what is playable, and an agent that guesses would be testing our
// guess rather than the contract". PhaseAcquire and PhaseJail already filtered correctly
// (`buy` only with the cash, `pay_jail` only with $50), so the pattern existed; PhaseManage
// simply did not follow it.
//
// # Why they are shared rather than duplicated
//
// The obvious fix — write the conditions again inside LegalActions — recreates the exact bug
// it is fixing, one level up: two copies of a rule drift, and the drift shows up as the engine
// offering a move it then refuses. So each predicate below is the SINGLE statement of its
// rule, called by LegalActions to advertise and by the doer to enforce. They cannot disagree.

// canBuildOn reports whether `seat` may put a house/hotel on `pos` right now.
//
// Every clause is a real Monopoly rule: own the whole colour group, nothing in the group
// mortgaged, build evenly (this square must be at the group's minimum), the bank must have the
// piece, and the seat must be able to pay for it.
func canBuildOn(s *State, seat, pos int) bool {
	if pos < 0 || pos >= BoardSize {
		return false
	}
	sp := space(pos)
	h := s.Holdings[pos]
	if sp.Kind != KindStreet || h.Owner != seat || h.Houses >= 5 {
		return false
	}
	if !s.ownsFullGroup(seat, sp.Group) {
		return false
	}
	for _, idx := range groupMembers[sp.Group] {
		if s.Holdings[idx].Mortgaged {
			return false
		}
	}
	if h.Houses != minHousesInGroup(s, sp.Group) { // even-build rule
		return false
	}
	if h.Houses < 4 {
		if s.HousesRemaining <= 0 {
			return false
		}
	} else if s.HotelsRemaining <= 0 {
		return false
	}
	return s.Players[seat].Cash >= sp.HouseCost
}

// canSellHouseOn reports whether `seat` may sell a house/hotel back from `pos`.
//
// The hotel clause is the one people forget: breaking a hotel needs FOUR houses available in
// the bank to put back on the square, so a hotel can be unsellable during a housing shortage.
func canSellHouseOn(s *State, seat, pos int) bool {
	if pos < 0 || pos >= BoardSize {
		return false
	}
	sp := space(pos)
	h := s.Holdings[pos]
	if sp.Kind != KindStreet || h.Owner != seat || h.Houses == 0 {
		return false
	}
	if h.Houses != maxHousesInGroup(s, sp.Group) { // even-sell rule
		return false
	}
	if h.Houses == 5 && s.HousesRemaining < 4 {
		return false
	}
	return true
}

// canMortgage reports whether `seat` may mortgage `pos`. Every building in the colour group
// must be gone first — the official rule, and the reason a heavily built group cannot be
// mortgaged for a quick rescue.
func canMortgage(s *State, seat, pos int) bool {
	if pos < 0 || pos >= BoardSize {
		return false
	}
	sp := space(pos)
	h := s.Holdings[pos]
	if !sp.Ownable() || h.Owner != seat || h.Mortgaged {
		return false
	}
	for _, idx := range groupMembers[sp.Group] {
		if s.Holdings[idx].Houses > 0 {
			return false
		}
	}
	return true
}

// canUnmortgage reports whether `seat` may lift the mortgage on `pos`, which costs the
// mortgage value plus 10% interest.
func canUnmortgage(s *State, seat, pos int) bool {
	if pos < 0 || pos >= BoardSize {
		return false
	}
	sp := space(pos)
	h := s.Holdings[pos]
	if !sp.Ownable() || h.Owner != seat || !h.Mortgaged {
		return false
	}
	return s.Players[seat].Cash >= unmortgageCost(pos)
}

// anyBuildable / anySellable / anyMortgageable / anyUnmortgageable answer "is this verb worth
// offering at all", which is what LegalActions needs.
func anyBuildable(s *State, seat int) bool {
	for pos := 0; pos < BoardSize; pos++ {
		if canBuildOn(s, seat, pos) {
			return true
		}
	}
	return false
}

func anySellable(s *State, seat int) bool {
	for pos := 0; pos < BoardSize; pos++ {
		if canSellHouseOn(s, seat, pos) {
			return true
		}
	}
	return false
}

func anyMortgageable(s *State, seat int) bool {
	for pos := 0; pos < BoardSize; pos++ {
		if canMortgage(s, seat, pos) {
			return true
		}
	}
	return false
}

func anyUnmortgageable(s *State, seat int) bool {
	for pos := 0; pos < BoardSize; pos++ {
		if canUnmortgage(s, seat, pos) {
			return true
		}
	}
	return false
}

// hasTradePartner reports whether anyone is left to trade with. Offering propose_trade at a
// table where every other seat is bankrupt advertises a move that cannot be completed.
func hasTradePartner(s *State, seat int) bool {
	for i := range s.Players {
		if i != seat && !s.Players[i].Bankrupt {
			return true
		}
	}
	return false
}

// manageActions is the set of property-management verbs `seat` can actually use right now.
//
// Shared by the owner's manage phase, the between-turns window and debt resolution, so a seat
// is offered the same management rights wherever the rules give them — which is the whole
// point of the official "you may build on your turn or between other players' turns" rule.
func manageActions(s *State, seat int) []string {
	acts := make([]string, 0, 4)
	if anyBuildable(s, seat) {
		acts = append(acts, ActBuild)
	}
	if anySellable(s, seat) {
		acts = append(acts, ActSellHouse)
	}
	if anyMortgageable(s, seat) {
		acts = append(acts, ActMortgage)
	}
	if anyUnmortgageable(s, seat) {
		acts = append(acts, ActUnmortgage)
	}
	return acts
}

// canBuildOnIgnoringCash is canBuildOn with the affordability clause removed.
//
// It exists so doBuild can tell two different failures apart. "You cannot afford this house"
// is actionable — sell, mortgage, or wait — while ErrIllegalAction on the same square would
// say only that something was wrong. Reporting insufficient funds for a square that is
// ineligible for some OTHER reason (wrong group, uneven build, no houses in the bank) would
// send an agent off to raise money it could never spend here.
func canBuildOnIgnoringCash(s *State, seat, pos int) bool {
	if pos < 0 || pos >= BoardSize {
		return false
	}
	rich := *s
	players := append([]Player(nil), s.Players...)
	players[seat].Cash = 1 << 30
	rich.Players = players
	return canBuildOn(&rich, seat, pos)
}
