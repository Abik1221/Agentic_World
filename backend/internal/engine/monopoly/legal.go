package monopoly

// legal.go holds shared legality queries used by agents (bots) and the table
// runner to choose only-valid actions. They mirror the engine's own checks in
// doBuild/doSellHouse/doMortgage exactly, so an action they return is guaranteed
// to be accepted by Step. Keeping them here (not duplicated per caller) means the
// rules live in one place.

// firstMortgageable returns the lowest-index property `seat` may mortgage now.
func firstMortgageable(s *State, seat int) (int, bool) {
	for idx := 0; idx < BoardSize; idx++ {
		h := s.Holdings[idx]
		if h.Owner != seat || h.Mortgaged || !space(idx).Ownable() {
			continue
		}
		clean := true
		for _, m := range groupMembers[space(idx).Group] {
			if s.Holdings[m].Houses > 0 {
				clean = false
				break
			}
		}
		if clean {
			return idx, true
		}
	}
	return 0, false
}

// firstSellable returns the lowest-index street whose house/hotel `seat` may sell.
func firstSellable(s *State, seat int) (int, bool) {
	for idx := 0; idx < BoardSize; idx++ {
		h := s.Holdings[idx]
		sp := space(idx)
		if h.Owner != seat || sp.Kind != KindStreet || h.Houses == 0 {
			continue
		}
		if h.Houses != maxHousesInGroup(s, sp.Group) {
			continue
		}
		if h.Houses == 5 && s.HousesRemaining < 4 {
			continue
		}
		return idx, true
	}
	return 0, false
}

// firstBuildable returns the lowest-index street where `seat` may build now.
func firstBuildable(s *State, seat int) (int, bool) {
	for idx := 0; idx < BoardSize; idx++ {
		h := s.Holdings[idx]
		sp := space(idx)
		if h.Owner != seat || sp.Kind != KindStreet || h.Houses >= 5 {
			continue
		}
		if !s.ownsFullGroup(seat, sp.Group) {
			continue
		}
		blocked := false
		for _, m := range groupMembers[sp.Group] {
			if s.Holdings[m].Mortgaged {
				blocked = true
				break
			}
		}
		if blocked || h.Houses != minHousesInGroup(s, sp.Group) {
			continue
		}
		if h.Houses < 4 && s.HousesRemaining <= 0 {
			continue
		}
		if h.Houses == 4 && s.HotelsRemaining <= 0 {
			continue
		}
		if s.Players[seat].Cash < sp.HouseCost {
			continue
		}
		return idx, true
	}
	return 0, false
}

// firstUnmortgageable returns the lowest-index mortgaged property `seat` owns.
func firstUnmortgageable(s *State, seat int) (int, bool) {
	for idx := 0; idx < BoardSize; idx++ {
		h := s.Holdings[idx]
		if h.Owner == seat && h.Mortgaged && space(idx).Ownable() {
			return idx, true
		}
	}
	return 0, false
}

// unmortgageCost is the mortgage value plus 10% interest.
//
// The ONE definition: doUnmortgage charges it and canUnmortgage tests affordability against
// it, so the price an agent is quoted and the price it is charged cannot drift apart.
func unmortgageCost(pos int) int {
	base := space(pos).MortgageValue()
	return base + (base+9)/10
}

// wouldCompleteGroup reports whether buying `pos` would give `seat` the whole
// color group (used by bots to prioritize monopoly-completing purchases).
func wouldCompleteGroup(s State, seat, pos int) bool {
	g := space(pos).Group
	members := groupMembers[g]
	if len(members) == 0 || !space(pos).Ownable() {
		return false
	}
	owned := 0
	for _, m := range members {
		if m == pos {
			continue
		}
		if s.Holdings[m].Owner == seat {
			owned++
		}
	}
	return owned == len(members)-1
}
