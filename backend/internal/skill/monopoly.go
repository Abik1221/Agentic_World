package skill

import (
	"encoding/json"
	"fmt"
	"math"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
)

// Monopoly decision scoring.
//
// # Why this game needed a different shape
//
// Goofspiel has one decision type and a computable equilibrium. Mafia has one decision
// type and a ground truth. Monopoly has neither: it is stochastic, its horizon runs to
// hundreds of turns, and it asks a dozen different questions.
//
// The published reinforcement-learning work on Monopoly (Bonjour et al., arXiv:2103.00683)
// found that its decision distribution is severely SKEWED, and that treating every
// decision alike is actively harmful — their hybrid agent, which handles frequent-complex
// decisions differently from infrequent-simple ones, beat uniform deep RL by 30%.
//
// That is the design constraint here. Most Monopoly decisions carry no choice at all:
// rolling, ending a turn, passing an auction you cannot afford. Scoring those uniformly
// would drown the handful of decisions that actually decide games.
//
// So this scorer is a TAXONOMY. Each decision type is scored on its own economics, and
// the types that cannot be modelled honestly are excluded rather than guessed at:
//
//	buy / decline    scored — expected rent against price
//	build            scored — return on investment, with the three-house inflection
//	auction          scored — the same valuation, against the standing bid
//	jail             scored — game stage decides whether to leave
//	trades           NOT SCORED, deliberately — see ScoreMonopolyDecision
//	roll / end turn  NOT SCORED — forced, no choice existed
//
// # Where the numbers come from
//
// Everything reduces to "what is this square worth", which is rent × how often anyone
// lands on it. The second factor is solved from this engine's own board and card decks by
// Markov chain — see monopoly_board.go. That is what makes the orange group price
// correctly without anyone hardcoding that orange is good.

// horizonTurns is how many more turns each opponent is assumed to take.
//
// A horizon is unavoidable: buying trades cash now for a rent stream later, so the length
// of "later" decides the trade. The engine's MaxTurns (1000) is a termination safety cap,
// not a game length — real Monopoly games run tens of turns per player, and using 1000
// would value every property as effectively infinite and make every purchase correct.
//
// 30 is a deliberate middle estimate. Regret is COMPARATIVE — both branches of a decision
// are valued under the same horizon — so a moderate error moves the two sides together
// and rarely flips an ordering. TestHorizonSensitivity pins that claim down rather than
// leaving it as an assertion.
const horizonTurns = 30.0

// monoView is the persisted Monopoly turn view. The engine ships its whole State in the
// agent view, so a scored decision has complete information: cash, ownership, houses,
// phase, the standing auction bid.
type monoView struct {
	YourSeat int         `json:"your_seat"`
	Phase    string      `json:"phase"`
	State    *mono.State `json:"state"`
}

// ScoreMonopolyDecision scores one decision, or reports that it is not scorable.
//
// The unscorable cases are as important as the scored ones. A decision with no
// alternative is not skill, and a trade's value depends on what it enables several turns
// later — no closed-form model captures that, and a confidently mediocre trade score
// would be worse than none. Both return false, which persists as NULL rather than as a
// zero the P-Index would average in as perfection.
func ScoreMonopolyDecision(raw []byte, action string) (Decision, bool) {
	var v monoView
	if err := json.Unmarshal(raw, &v); err != nil || v.State == nil {
		return Decision{}, false
	}
	st := v.State
	if v.YourSeat < 0 || v.YourSeat >= len(st.Players) {
		return Decision{}, false
	}
	phase := v.Phase
	if phase == "" {
		phase = st.Phase
	}

	switch phase {
	case mono.PhaseAcquire:
		return scoreAcquire(st, v.YourSeat, action)
	case mono.PhaseAuction:
		return scoreAuction(st, v.YourSeat, action)
	case mono.PhaseManage:
		return scoreManage(st, v.YourSeat, action)
	case mono.PhaseJail:
		return scoreJail(st, v.YourSeat, action)
	default:
		// Roll, debt resolution, trade windows, game over. Either forced or not honestly
		// modellable — excluded on purpose.
		return Decision{}, false
	}
}

// ── Valuation ───────────────────────────────────────────────────────────────

// rentAt is the rent a square charges at a given house count, including the
// whole-group rule: an undeveloped street in a completed monopoly charges DOUBLE base.
// That doubling is most of why completing a group matters and must not be dropped.
func rentAt(sq mono.Space, houses int, ownsWholeGroup bool) float64 {
	switch sq.Kind {
	case mono.KindStreet:
		if houses > 0 {
			return float64(sq.Rent[min(houses, 5)])
		}
		if ownsWholeGroup {
			return float64(sq.Rent[0]) * 2
		}
		return float64(sq.Rent[0])
	case mono.KindRailroad:
		// 25/50/100/200 by count owned. Two is the realistic mid-game case and is used as
		// the neutral estimate; the exact count is applied by the caller where known.
		return 50
	case mono.KindUtility:
		// 4× or 10× the dice; mean dice is 7.
		return 7 * 4
	}
	return 0
}

// ownsGroup reports whether `seat` holds every square in a colour group, and how many of
// the group it holds.
func ownsGroup(st *mono.State, seat int, group string) (whole bool, mine, size int) {
	if group == "" {
		return false, 0, 0
	}
	for _, s := range mono.Board() {
		if s.Group != group {
			continue
		}
		size++
		if seat >= 0 && st.Holdings[s.Index].Owner == seat {
			mine++
		}
	}
	return size > 0 && mine == size, mine, size
}

// activeOpponents counts the seats still able to pay rent. Rent income scales with them,
// and a two-player endgame values property very differently from a five-player opening.
func activeOpponents(st *mono.State, seat int) float64 {
	n := 0
	for i := range st.Players {
		if i != seat && !st.Players[i].Bankrupt {
			n++
		}
	}
	return float64(n)
}

// rentStream is the expected rent a square earns its owner over the remaining game.
//
//	E[rent] = P(land) × rent × opponents × turns
//
// P(land) is the solved stationary probability, so a square 6–9 past Jail is correctly
// worth more than one nobody reaches.
func rentStream(st *mono.State, seat, square, houses int, whole bool) float64 {
	board := mono.Board()
	if square < 0 || square >= len(board) {
		return 0
	}
	return LandingProb(square) * rentAt(board[square], houses, whole) *
		activeOpponents(st, seat) * horizonTurns
}

// propertyValue is what owning a square is worth to `seat` right now: its rent stream
// plus the completion premium, less nothing — the price is subtracted by the caller,
// because an auction pays a different price than a list-price purchase.
//
// The completion premium is the whole game. Owning two of three oranges earns base rent;
// owning the third doubles it AND unlocks building, which is where Monopoly's money
// actually is. A model that valued each square independently would rate the third orange
// the same as the first and would never recommend the purchase that wins games.
func propertyValue(st *mono.State, seat, square int) float64 {
	board := mono.Board()
	sq := board[square]

	_, mine, size := ownsGroup(st, seat, sq.Group)
	willOwnWhole := size > 0 && mine+1 == size

	// Rent this square earns on its own, undeveloped.
	base := rentStream(st, seat, square, 0, willOwnWhole)

	// OPTION VALUE — the reason a lone property is worth buying at all.
	//
	// Rent alone says no: an undeveloped street returns a few percent of its price per
	// lap, so valued in isolation almost every purchase in Monopoly is a mistake. The
	// first version of this scorer said exactly that, and it was wrong — the test that
	// caught it asserted only that oranges beat browns, and both came out negative.
	//
	// A property is not bought for its own rent. It is bought as a STEP TOWARD a
	// monopoly, which is where all the money is, and as a block on the opponent taking
	// the same step. So the value of the (k+1)th square in a group is the increase in how
	// close you are to owning the set:
	//
	//	progress(k) = (k/n)²      value = groupValue × [progress(k+1) − progress(k)]
	//
	// Quadratic, because Monopoly's returns are: holding one of three oranges is worth
	// little, holding the third is transformative. It also makes the completion premium
	// fall out of the same formula instead of needing a special case — the last square in
	// a group naturally takes the largest share.
	var optionValue float64
	if size > 0 {
		g := groupValue(st, seat, sq.Group)
		n := float64(size)
		k := float64(mine)
		progress := ((k+1)*(k+1) - k*k) / (n * n)
		optionValue = g * progress
	}
	return base + optionValue
}

// groupValue is what the whole colour group is worth once assembled and developed.
//
// Streets are valued at THREE HOUSES each — the documented return-on-investment
// inflection in Monopoly, where the rent jump per dollar of house is largest, and the
// level a competent player actually builds to — net of what those houses cost.
// Railroads and utilities cannot be built on, so they are valued at full ownership.
func groupValue(st *mono.State, seat int, group string) float64 {
	var total, cost float64
	for _, s := range mono.Board() {
		if s.Group != group {
			continue
		}
		if s.Kind == mono.KindStreet {
			total += rentStream(st, seat, s.Index, 3, true)
			cost += float64(s.HouseCost) * 3
			continue
		}
		total += rentStream(st, seat, s.Index, 0, true)
	}
	return math.Max(0, total-cost)
}

// liquidityRisk is the expected cost of being short of cash after a purchase.
//
// Buying is not free even at a fair price: cash is the only thing that stops a rent bill
// bankrupting you, and this engine's timeout default goes straight to BANKRUPT rather
// than mortgaging to survive. A model without this recommends spending to zero, which is
// the most common way a Monopoly agent actually loses.
func liquidityRisk(st *mono.State, seat, spend int) float64 {
	after := float64(st.Players[seat].Cash - spend)
	if after < 0 {
		return math.Inf(1) // cannot afford it at all
	}
	// A typical mid-game rent bill. Below it, the risk of forced bankruptcy rises sharply.
	const dangerCash = 200.0
	if after >= dangerCash {
		return 0
	}
	shortfall := (dangerCash - after) / dangerCash
	// Quadratic: being $20 short is far worse than being $180 short.
	return shortfall * shortfall * dangerCash
}

// ── Per-phase scorers ───────────────────────────────────────────────────────

func scoreAcquire(st *mono.State, seat int, action string) (Decision, bool) {
	square := st.Players[seat].Position
	board := mono.Board()
	if square < 0 || square >= len(board) || board[square].Price <= 0 {
		return Decision{}, false
	}
	price := board[square].Price

	evBuy := propertyValue(st, seat, square) - float64(price) - liquidityRisk(st, seat, price)
	// Declining is not neutral: the square goes to auction, so an opponent may take it —
	// and if it completes THEIR group, that is a real cost. Denial value is what makes a
	// competent agent overpay for the property its rival needs.
	evDecline := -denialCost(st, seat, square)

	return pickTwo(st.TurnCount, action,
		option{mono.ActBuy, evBuy}, option{mono.ActDecline, evDecline},
		fmt.Sprintf("%s at $%d", board[square].Name, price))
}

// denialCost is what letting an opponent have this square is expected to cost.
// Only the opponent closest to completing the group is considered — the others gain far
// less from it, and summing over everyone would wildly overstate the danger.
func denialCost(st *mono.State, seat, square int) float64 {
	sq := mono.Board()[square]
	if sq.Group == "" {
		return 0
	}
	var worst float64
	for opp := range st.Players {
		if opp == seat || st.Players[opp].Bankrupt {
			continue
		}
		_, mine, size := ownsGroup(st, opp, sq.Group)
		if size == 0 || mine+1 != size {
			continue // this square would not complete their group
		}
		if v := propertyValue(st, opp, square); v > worst {
			worst = v
		}
	}
	return worst
}

func scoreAuction(st *mono.State, seat int, action string) (Decision, bool) {
	if st.Auction == nil {
		return Decision{}, false
	}
	square := st.Auction.Property
	board := mono.Board()
	if square < 0 || square >= len(board) {
		return Decision{}, false
	}
	// The cheapest raise that could win it.
	bid := st.Auction.HighBid + 1
	evBid := propertyValue(st, seat, square) - float64(bid) - liquidityRisk(st, seat, bid)
	evPass := -denialCost(st, seat, square)

	return pickTwo(st.TurnCount, action,
		option{mono.ActBid, evBid}, option{mono.ActPass, evPass},
		fmt.Sprintf("%s at $%d", board[square].Name, bid))
}

func scoreManage(st *mono.State, seat int, action string) (Decision, bool) {
	// The only management decision with real economics is BUILDING. Mortgaging and
	// unmortgaging are situational cash management with no clean optimum, and the trade
	// actions are excluded by design — so a manage phase with nothing to build carries no
	// scorable choice.
	best, bestGain := -1, 0.0
	for _, s := range mono.Board() {
		if s.Kind != mono.KindStreet || st.Holdings[s.Index].Owner != seat {
			continue
		}
		whole, _, _ := ownsGroup(st, seat, s.Group)
		if !whole || st.Holdings[s.Index].Mortgaged {
			continue
		}
		h := st.Holdings[s.Index].Houses
		if h >= 5 || st.Players[seat].Cash < s.HouseCost {
			continue
		}
		gain := rentStream(st, seat, s.Index, h+1, true) - rentStream(st, seat, s.Index, h, true) -
			float64(s.HouseCost) - liquidityRisk(st, seat, s.HouseCost)
		if gain > bestGain {
			best, bestGain = s.Index, gain
		}
	}
	if best < 0 {
		return Decision{}, false // nothing buildable: no choice to score
	}
	return pickTwo(st.TurnCount, action,
		option{mono.ActBuild, bestGain}, option{mono.ActEndTurn, 0},
		fmt.Sprintf("house on %s", mono.Board()[best].Name))
}

// scoreJail is the one decision where the right answer REVERSES over the course of a game,
// which is why it is worth scoring at all.
//
// Early on the board is unowned: getting out means more squares to buy, and staying in
// wastes the buying phase. Late on the board is developed: jail is the safest square on
// it, and paying $50 to leave shelter and walk into hotels is a real error.
//
// The stage is read from how much of the board is still unowned — a fact in the state
// rather than a turn-count guess, so it is right even in an unusually fast or slow game.
func scoreJail(st *mono.State, seat int, action string) (Decision, bool) {
	board := mono.Board()
	var ownable, owned, developed float64
	for _, s := range board {
		if s.Price <= 0 {
			continue
		}
		ownable++
		if st.Holdings[s.Index].Owner != mono.Bank {
			owned++
		}
		developed += float64(st.Holdings[s.Index].Houses)
	}
	if ownable == 0 {
		return Decision{}, false
	}
	unowned := 1 - owned/ownable

	// Value of being out: a turn of buying opportunity, scaled by what is left to buy.
	out := unowned * 120
	// Cost of being out: the expected rent you walk into, scaled by development.
	danger := developed * 12

	evLeave := out - danger - float64(mono.JailFine) - liquidityRisk(st, seat, mono.JailFine)
	evStay := 0.0

	opts := []option{{mono.ActRollJail, evStay}}
	if st.Players[seat].Cash >= mono.JailFine {
		opts = append(opts, option{mono.ActPayJail, evLeave})
	}
	if st.Players[seat].JailCards > 0 {
		// A free card leaves without the fine — strictly better than paying.
		opts = append(opts, option{mono.ActUseJailCard, evLeave + float64(mono.JailFine)})
	}
	if len(opts) < 2 {
		return Decision{}, false // no cash and no card: staying is forced
	}
	return pickN(st.TurnCount, action, opts, "jail")
}

// ── Shared scoring plumbing ─────────────────────────────────────────────────

type option struct {
	action string
	ev     float64
}

func pickTwo(round int, chosen string, a, b option, what string) (Decision, bool) {
	return pickN(round, chosen, []option{a, b}, what)
}

// pickN turns a set of valued options into a Decision.
//
// An action not among the options is UNSCORABLE rather than worst: it means the scorer
// does not model what the agent did (a mortgage during a manage phase, say), and marking
// that as a blunder would punish an agent for the scorer's own gaps.
func pickN(round int, chosen string, opts []option, what string) (Decision, bool) {
	if len(opts) < 2 {
		return Decision{}, false
	}
	best, worst := opts[0], opts[0]
	var chosenEV float64
	found := false
	for _, o := range opts {
		if o.ev > best.ev || (o.ev == best.ev && o.action < best.action) {
			best = o
		}
		if o.ev < worst.ev {
			worst = o
		}
		if o.action == chosen {
			chosenEV, found = o.ev, true
		}
	}
	if !found {
		return Decision{}, false
	}
	// An infinite EV means "cannot afford"; it must not propagate into arithmetic.
	if math.IsInf(best.ev, 0) || math.IsInf(worst.ev, 0) || math.IsInf(chosenEV, 0) {
		best.ev = finite(best.ev)
		worst.ev = finite(worst.ev)
		chosenEV = finite(chosenEV)
	}

	d := Decision{
		Round: round, Chosen: chosen, Best: best.action,
		ValueChosen: chosenEV, ValueBest: best.ev, ValueWorst: worst.ev,
		Regret: normalize(chosenEV, best.ev, worst.ev),
	}
	switch {
	case d.Regret <= 1e-9:
		d.Why = fmt.Sprintf("best available choice for %s", what)
	case d.Regret > BlunderThreshold:
		d.Why = fmt.Sprintf("%s on %s; %s was worth $%.0f more", chosen, what, best.action, best.ev-chosenEV)
	default:
		d.Why = fmt.Sprintf("%s on %s; %s was slightly better", chosen, what, best.action)
	}
	return d, true
}

// finite maps ±Inf onto a large but usable magnitude so an unaffordable option ranks last
// without turning every downstream subtraction into NaN.
func finite(v float64) float64 {
	switch {
	case math.IsInf(v, 1):
		return 1e9
	case math.IsInf(v, -1):
		return -1e9
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
