package monopoly

// agent.go provides the "simulated players" that fill every seat a human does not
// occupy. On this platform a person joins as a single user, but Monopoly needs a
// full table — so the sandbox seats deterministic bot agents alongside them. Each
// bot has a name and a personality and plays like a real opponent: buying,
// auctioning, building, managing jail, and raising funds under pressure.
//
// Bots are PURE and DETERMINISTIC: every choice derives from the match seed and
// the seat, so a table replays identically (the same property as the engine).

// Agent decides one action for its seat given the current public game state.
// Monopoly is a perfect-information game apart from future dice, so the agent may
// see the whole State. Implementations MUST return a legal action; the Table also
// guards against bad agents by falling back to a deterministic timeout.
type Agent interface {
	Name() string
	Decide(e *Engine, s State, seat int) Action
}

// Style is a bot personality expressed as 0..100 propensities plus a jail-buyout
// cash threshold. These shape, but never break, legal play.
type Style struct {
	Label       string
	BuyChance   int // propensity to buy an unowned property it lands on
	BuildChance int // propensity to build when able
	BidAggress  int // max bid as a % of list price in auctions
	JailPayCash int // pay the $50 fine to leave jail only if cash exceeds this
}

// Built-in personalities. They make for visibly different opponents at the table.
var (
	StyleTycoon   = Style{Label: "Tycoon", BuyChance: 95, BuildChance: 80, BidAggress: 80, JailPayCash: 200}
	StyleBanker   = Style{Label: "Banker", BuyChance: 70, BuildChance: 45, BidAggress: 50, JailPayCash: 400}
	StyleCautious = Style{Label: "Cautious", BuyChance: 55, BuildChance: 25, BidAggress: 30, JailPayCash: 600}
	StyleWildcard = Style{Label: "Wildcard", BuyChance: 85, BuildChance: 60, BidAggress: 95, JailPayCash: 100}
)

// DefaultStyles is a rotation used to seat a varied table of opponents.
var DefaultStyles = []Style{StyleTycoon, StyleBanker, StyleCautious, StyleWildcard}

// Bot is the default Agent implementation.
type Bot struct {
	name  string
	style Style
	rng   *hashRand
}

// NewBot builds a deterministic bot for a seat. Its randomness is a seed-derived
// stream unique to the seat, so two runs of the same table play identically.
func NewBot(name string, style Style, seed []byte, seat int) *Bot {
	return &Bot{name: name, style: style, rng: newHashRand(seed, "bot:"+itoa(seat))}
}

func (b *Bot) Name() string { return b.name }

// Decide returns a legal action for every phase.
func (b *Bot) Decide(e *Engine, s State, seat int) Action {
	switch s.Phase {
	case PhaseRoll:
		return Action{Kind: ActRoll}

	case PhaseJail:
		p := s.Players[seat]
		if p.JailCards > 0 {
			return Action{Kind: ActUseJailCard}
		}
		if p.Cash >= b.style.JailPayCash {
			return Action{Kind: ActPayJail}
		}
		return Action{Kind: ActRollJail}

	case PhaseAcquire:
		pos := s.Players[seat].Position
		price := space(pos).Price
		if s.Players[seat].Cash >= price &&
			(wouldCompleteGroup(s, seat, pos) || b.rng.Intn(100) < b.style.BuyChance) {
			return Action{Kind: ActBuy}
		}
		return Action{Kind: ActDecline}

	case PhaseAuction:
		au := s.Auction
		price := space(au.Property).Price
		maxBid := price * b.style.BidAggress / 100
		inc := price / 20
		if inc < 1 {
			inc = 1
		}
		next := au.HighBid + inc
		if cash := s.Players[seat].Cash; next > cash {
			next = cash
		}
		if au.HighBidder != seat && next > au.HighBid && next <= maxBid {
			return Action{Kind: ActBid, Amount: next}
		}
		return Action{Kind: ActPass}

	case PhaseResolveDebt:
		if pos, ok := firstSellable(&s, seat); ok {
			return Action{Kind: ActSellHouse, Property: pos}
		}
		if pos, ok := firstMortgageable(&s, seat); ok {
			return Action{Kind: ActMortgage, Property: pos}
		}
		return Action{Kind: ActBankrupt}

	case PhaseManage:
		// Lift a mortgage first when comfortably flush — mortgaged property earns no
		// rent, so a wealthy bot should redeem it.
		if pos, ok := firstUnmortgageable(&s, seat); ok && s.Players[seat].Cash > unmortgageCost(pos)+300 {
			return Action{Kind: ActUnmortgage, Property: pos}
		}
		if pos, ok := firstBuildable(&s, seat); ok &&
			s.Players[seat].Cash >= space(pos).HouseCost*2 &&
			b.rng.Intn(100) < b.style.BuildChance {
			return Action{Kind: ActBuild, Property: pos}
		}
		return Action{Kind: ActEndTurn}

	case PhaseTradeResponse:
		// A trade was proposed to this bot; accept only if it gains list-price value.
		return Action{Kind: tradeResponse(s, seat)}

	case PhaseTrade:
		// Open-floor window: rule-based bots don't originate trades, they skip.
		return Action{Kind: ActSkipTrade}
	}
	return Action{Kind: ActEndTurn}
}

// tradeResponse accepts a proposed trade when the receiving seat comes out ahead on
// list-price value (cash + property prices), otherwise rejects.
func tradeResponse(s State, seat int) string {
	t := s.PendingTrade
	if t == nil || t.Target != seat {
		return ActRejectTrade
	}
	gain := t.GiveCash - t.WantCash
	for _, idx := range t.GiveProps {
		gain += space(idx).Price
	}
	for _, idx := range t.WantProps {
		gain -= space(idx).Price
	}
	if gain >= 0 {
		return ActAcceptTrade
	}
	return ActRejectTrade
}

// itoa is a tiny, allocation-light int->string for RNG labels (avoids pulling in
// strconv for a single use and keeps this package dependency-free).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
