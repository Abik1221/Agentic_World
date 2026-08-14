package monopoly

// The housing shortage auction.
//
// # The rule
//
// Official Monopoly: "if there are a limited number of houses and hotels available and two or
// more players wish to buy more than the Bank has, the houses or hotels must be sold at
// auction to the highest bidder." Only 32 houses and 12 hotels exist, and buying them up to
// deny opponents is a real, well-known tactic — so an arena without this rule is missing one
// of the game's genuine strategic layers, not a footnote.
//
// # The problem, and the trigger chosen
//
// The rule fires on SIMULTANEOUS demand, and a sequential engine has no simultaneous moment.
// Any trigger is therefore an interpretation, so this one is stated plainly rather than buried:
//
//	A build is contested when the bank still has at least one of the needed piece, and MORE
//	SEATS COULD LEGALLY BUY THAT PIECE RIGHT NOW THAN THE BANK HAS PIECES TO SELL.
//
// Both halves matter:
//
//   - "at least one" — with none left there is nothing to auction, and the official rule is
//     that players wait for houses to come back to the bank. That path is unchanged.
//   - "could legally buy" is not a guess about intent. It is the rules' own test: the seat owns
//     the full colour group unmortgaged, the square is at the group minimum (even build), and
//     it can afford the price. Nothing here infers what an agent "wants".
//
// It is the NARROWEST honest reading: it fires only when the bank provably cannot give one
// piece to each seat that could buy one. Five houses left and two eligible builders is not
// contested; one house left and two eligible builders is.
//
// # Why the initiator's build price opens the bidding
//
// The seat that tried to build has already demonstrated it will pay list price, so the auction
// opens at exactly that, with the initiator as the standing high bidder. This has three
// properties worth having:
//
//   - Triggering an auction can never cost the initiator anything. If nobody outbids, it buys
//     at list price — precisely what would have happened with no contest at all.
//   - There is no "nobody bid" outcome, so no way to loop: attempt build, auction, all pass,
//     attempt build again, forever.
//   - The auction can only ever RAISE the price, which is the entire point of the rule.
//
// # Why bidders name their square
//
// A house auction sells the PIECE, not a property, and the winner still has to put it
// somewhere legal. Auto-placing it (say, on the winner's lowest-index buildable square) would
// be deterministic but would quietly choose for the agent — a player with two finished groups
// usually wants the expensive one. So each bid carries the square it is for, validated when the
// bid is placed.

// canBuildIgnoringSupply is canBuildOn without the bank-supply clause.
//
// The supply check is what the shortage auction exists to resolve, so eligibility for the
// auction has to be judged without it — otherwise the bank running low would empty the very
// list used to decide the bank is running low.
func canBuildIgnoringSupply(s *State, seat, pos int) bool {
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
	return s.Players[seat].Cash >= sp.HouseCost
}

// needsHotel reports whether building on `pos` consumes a HOTEL rather than a house.
// The fifth building on a square is a hotel, and it draws on a separate supply of twelve.
func needsHotel(s *State, pos int) bool { return s.Holdings[pos].Houses == 4 }

// buildersFor lists every non-bankrupt seat that could legally buy the given piece kind right
// now, ignoring supply, in seat order.
//
// Deliberately counts SEATS, not squares. A player with three buildable squares is still one
// buyer competing for one piece, and counting squares would declare a shortage contested on a
// board where only one player can build at all.
func buildersFor(s *State, hotel bool) []int {
	var out []int
	for seat := range s.Players {
		if s.Players[seat].Bankrupt {
			continue
		}
		for pos := 0; pos < BoardSize; pos++ {
			if canBuildIgnoringSupply(s, seat, pos) && needsHotel(s, pos) == hotel {
				out = append(out, seat)
				break
			}
		}
	}
	return out
}

// buildIsContested reports whether a build on `pos` must go to auction, and who may bid.
//
// Returns false with a nil list when the build should proceed normally — which is the common
// case and stays completely unaffected by any of this.
func buildIsContested(s *State, pos int) (bool, []int) {
	hotel := needsHotel(s, pos)
	supply := s.HousesRemaining
	if hotel {
		supply = s.HotelsRemaining
	}
	if supply < 1 {
		// Nothing to auction. Officially players wait for pieces to return to the bank, and
		// canBuildOn already refuses the build, so this path is unchanged.
		return false, nil
	}
	rivals := buildersFor(s, hotel)
	if len(rivals) <= supply {
		// The bank can serve everyone who could buy. No contest, no auction.
		return false, nil
	}
	return true, rivals
}

// startHouseAuction opens bidding for one scarce house or hotel.
//
// The initiator is already the high bidder at list price (see the file comment), so bidding
// starts with the NEXT eligible seat: asking the initiator to outbid itself would be a wasted
// decision, and on a metered key a wasted model call.
func (e *Engine) startHouseAuction(ns *State, initiator, pos int, rivals []int, ret string) []Event {
	in := make([]bool, len(ns.Players))
	for _, seat := range rivals {
		in[seat] = true
	}
	targets := make([]int, len(ns.Players))
	for i := range targets {
		targets[i] = -1
	}
	targets[initiator] = pos

	first := initiator
	for step := 1; step <= len(ns.Players); step++ {
		cand := (initiator + step) % len(ns.Players)
		if in[cand] {
			first = cand
			break
		}
	}

	ns.Auction = &AuctionState{
		Property:   -1, // no property changes hands; the piece does
		HighBid:    space(pos).HouseCost,
		HighBidder: initiator,
		InAuction:  in,
		Current:    first,
		House:      true,
		Hotel:      needsHotel(ns, pos),
		Targets:    targets,
		Return:     ret,
	}
	ns.Phase = PhaseAuction
	return []Event{e.emit(ns, EvHouseAuctionStarted, HouseAuctionPayload{
		Seat: initiator, Property: pos, Hotel: ns.Auction.Hotel,
		Bidders: append([]int(nil), rivals...), OpeningBid: ns.Auction.HighBid,
	})}
}

// closeHouseAuction hands the piece to the winner and puts it on the square they named.
//
// The winner always exists: the initiator's list-price bid stands from the moment the auction
// opens, so there is no unsold branch to handle.
func (e *Engine) closeHouseAuction(ns *State, evs *[]Event) {
	au := ns.Auction
	seat := au.HighBidder
	pos := au.Targets[seat]
	ns.Auction = nil

	// Re-validated at close rather than trusted from bid time. Cash can only have risen
	// during an auction (mortgaging is the sole management action allowed), and nothing else
	// moves, so this should always hold — but placing a building from a stale target would
	// corrupt the board, which is worse than refusing a bid.
	if pos < 0 || !canBuildIgnoringSupply(ns, seat, pos) || ns.Players[seat].Cash < au.HighBid {
		*evs = append(*evs, e.emit(ns, EvAuctionUnsold, AuctionResultPayload{Seat: Bank, Property: pos, Amount: 0}))
		ns.Phase = au.Return
		return
	}

	h := ns.Holdings[pos]
	ns.Players[seat].Cash -= au.HighBid
	if h.Houses < 4 {
		ns.HousesRemaining--
	} else {
		ns.HousesRemaining += 4 // the four houses go back when a hotel replaces them
		ns.HotelsRemaining--
	}
	h.Houses++
	ns.Holdings[pos] = h

	*evs = append(*evs,
		e.emit(ns, EvCashChanged, CashChangedPayload{Seat: seat, Delta: -au.HighBid, Balance: ns.Players[seat].Cash, Reason: "house_auction"}),
		e.emit(ns, EvHouseBuilt, BuildPayload{Seat: seat, Property: pos, Houses: h.Houses}),
		e.emit(ns, EvAuctionWon, AuctionResultPayload{Seat: seat, Property: pos, Amount: au.HighBid}),
	)
	ns.Phase = au.Return
}
