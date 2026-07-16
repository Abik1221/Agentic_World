package monopoly

// vocab.go declares Monopoly's PUBLIC, agent-facing vocabulary as enumerable
// slices. The Phase*/Act*/Ev* consts elsewhere are the source of truth for the
// VALUES; these slices are the source of truth for the SET. They are consumed by
// the developer-docs generator (cmd/gamespec) and a drift test
// (internal/gamespec) that fails the build if the docs and these slices disagree.
//
// INVARIANT: when you add or remove a Phase*/Act*/Ev* const, update the matching
// slice here in the same change.

// AllPhases is every phase value an agent may see, in rough turn order.
var AllPhases = []string{
	PhaseRoll,
	PhaseJail,
	PhaseAcquire,
	PhaseAuction,
	PhaseResolveDebt,
	PhaseManage,
	PhaseTradeResponse,
	PhaseGameOver,
}

// AllActions is every action kind an agent may submit (LegalActions returns the
// subset valid for the current phase).
var AllActions = []string{
	ActRoll,
	ActBuy,
	ActDecline,
	ActBid,
	ActPass,
	ActBuild,
	ActSellHouse,
	ActMortgage,
	ActUnmortgage,
	ActPayJail,
	ActUseJailCard,
	ActRollJail,
	ActEndTurn,
	ActBankrupt,
	ActProposeTrade,
	ActAcceptTrade,
	ActRejectTrade,
}

// AllEventTypes is every event `type` the engine emits.
var AllEventTypes = []EventType{
	EvMatchCreated,
	EvTurnStarted,
	EvDiceRolled,
	EvMoved,
	EvCashChanged,
	EvRentPaid,
	EvPropertyPurchased,
	EvCardDrawn,
	EvWentToJail,
	EvLeftJail,
	EvHouseBuilt,
	EvHouseSold,
	EvMortgaged,
	EvUnmortgaged,
	EvAuctionStarted,
	EvBidPlaced,
	EvAuctionPassed,
	EvAuctionWon,
	EvAuctionUnsold,
	EvBankrupt,
	EvTradeProposed,
	EvTradeExecuted,
	EvTradeRejected,
	EvTurnEnded,
	EvMatchFinished,
}
