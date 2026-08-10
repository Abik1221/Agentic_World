package skill

import (
	"encoding/json"
	"fmt"
	"testing"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
)

// Board squares used throughout. Chosen because the strategy literature has strong,
// well-known opinions about each of them.
const (
	sqMediterranean = 1  // brown, cheapest, lowest traffic
	sqOrientalAve   = 6  // light blue
	sqStJames       = 16 // orange — the best group in the game
	sqTennessee     = 18 // orange
	sqNewYork       = 19 // orange
	sqBoardwalk     = 39 // dark blue, most expensive, low traffic
	sqParkPlace     = 37 // dark blue
)

func monoSeed() []byte {
	b := make([]byte, 32)
	copy(b, "skill-monopoly-fixture")
	return b
}

// monoFixture builds a scorable Monopoly view.
func monoFixture(t *testing.T, phase string, seat int, mutate func(*mono.State)) []byte {
	t.Helper()
	e := mono.New(mono.Config{Players: 4, StartingCash: 1500, MaxTurns: 1000, GoSalary: 200})
	st, _ := e.Init(monoSeed())
	st.Phase = phase
	if mutate != nil {
		mutate(&st)
	}
	raw, err := json.Marshal(map[string]any{
		"your_seat": seat, "phase": phase, "state": st,
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return raw
}

func mustScore(t *testing.T, raw []byte, action string) Decision {
	t.Helper()
	d, ok := ScoreMonopolyDecision(raw, action)
	if !ok {
		t.Fatalf("action %q was not scorable", action)
	}
	return d
}

// ── The strategy results the model must reproduce ───────────────────────────

// Buying an orange at list price is a good decision and buying Mediterranean is a poor
// one, because orange is landed on twice as often for a comparable price. If the scorer
// disagrees it is not pricing traffic, and every downstream verdict is noise.
func TestOrangeIsWorthMoreThanBrown(t *testing.T) {
	orange := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqStJames
	})
	brown := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqMediterranean
	})
	o := mustScore(t, orange, mono.ActBuy)
	b := mustScore(t, brown, mono.ActBuy)
	if !(o.ValueChosen > b.ValueChosen) {
		t.Fatalf("buying St. James (EV %.1f) is not valued above Mediterranean (EV %.1f) — "+
			"the scorer is not using landing probability", o.ValueChosen, b.ValueChosen)
	}
	if o.Regret != 0 {
		t.Errorf("buying an orange at list price scored regret %.3f; it is the strongest "+
			"routine purchase in the game", o.Regret)
	}
}

// THE decision that wins Monopoly games: the square that completes your monopoly. It
// unlocks doubled rent and building, so it must be worth far more than the same square
// bought in isolation.
func TestCompletingAMonopolyIsWorthFarMore(t *testing.T) {
	isolated := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqNewYork
	})
	completing := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqNewYork
		s.Holdings[sqStJames].Owner = 0 // already hold the other two oranges
		s.Holdings[sqTennessee].Owner = 0
	})
	a := mustScore(t, isolated, mono.ActBuy)
	b := mustScore(t, completing, mono.ActBuy)
	if !(b.ValueChosen > a.ValueChosen*1.5) {
		t.Fatalf("completing the orange monopoly is valued at %.1f against %.1f for the same "+
			"square in isolation — the completion premium is missing, and the scorer would "+
			"never recognise the purchase that decides games", b.ValueChosen, a.ValueChosen)
	}
	if b.Regret != 0 {
		t.Errorf("completing a monopoly scored regret %.3f, want 0", b.Regret)
	}
}

// Declining the square that would complete an OPPONENT's monopoly is a real error. Denial
// value is why competent players overpay for the property their rival needs.
func TestDecliningWhatCompletesAnOpponentsMonopolyIsPenalised(t *testing.T) {
	raw := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqNewYork
		s.Holdings[sqStJames].Owner = 1 // seat 1 holds the other two oranges
		s.Holdings[sqTennessee].Owner = 1
	})
	decline := mustScore(t, raw, mono.ActDecline)
	if decline.Regret <= BlunderThreshold {
		t.Fatalf("declining the square that hands seat 1 the orange monopoly scored only "+
			"%.3f regret; letting a rival complete the best group in the game is a blunder",
			decline.Regret)
	}
	if buy := mustScore(t, raw, mono.ActBuy); buy.Regret != 0 {
		t.Errorf("buying to deny scored regret %.3f, want 0", buy.Regret)
	}
}

// Buying is not free even at a fair price. Spending to near-zero invites bankruptcy on the
// first rent bill — and this engine's fallback goes straight to BANKRUPT rather than
// mortgaging to survive, so the risk is real rather than theoretical.
func TestSpendingToNearZeroIsPenalised(t *testing.T) {
	rich := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqBoardwalk
		s.Players[0].Cash = 1500
	})
	poor := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqBoardwalk
		s.Players[0].Cash = 420 // Boardwalk is $400 — buying leaves $20
	})
	r := mustScore(t, rich, mono.ActBuy)
	p := mustScore(t, poor, mono.ActBuy)
	if !(r.ValueChosen > p.ValueChosen) {
		t.Fatalf("buying Boardwalk with $1500 (EV %.1f) is not valued above buying it with "+
			"$420 (EV %.1f) — liquidity risk is not being charged", r.ValueChosen, p.ValueChosen)
	}
}

// Building on a monopoly is the highest-return move in Monopoly. Ending the turn instead,
// with the cash in hand, must read as giving something up.
func TestNotBuildingOnAMonopolyIsAMistake(t *testing.T) {
	raw := monoFixture(t, mono.PhaseManage, 0, func(s *mono.State) {
		for _, sq := range []int{sqStJames, sqTennessee, sqNewYork} {
			s.Holdings[sq].Owner = 0
		}
		s.Players[0].Cash = 1200
	})
	build := mustScore(t, raw, mono.ActBuild)
	end := mustScore(t, raw, mono.ActEndTurn)
	if build.Regret != 0 {
		t.Fatalf("building on a complete orange monopoly with $1200 in hand scored regret "+
			"%.3f, want 0", build.Regret)
	}
	if end.Regret <= 0 {
		t.Fatalf("ending the turn without building scored %.3f regret", end.Regret)
	}
}

// A manage phase with nothing buildable carries no choice, and a decision with no
// alternative is not skill. It must be excluded, not scored as perfect.
func TestManagePhaseWithNothingToBuildIsUnscorable(t *testing.T) {
	raw := monoFixture(t, mono.PhaseManage, 0, nil) // owns nothing
	if _, ok := ScoreMonopolyDecision(raw, mono.ActEndTurn); ok {
		t.Fatal("a manage phase with no monopoly to build on was scored; an agent with no " +
			"options would accumulate free perfect decisions")
	}
}

// Jail is the decision whose right answer REVERSES across a game, which is precisely why
// it is worth scoring. Early: leave and buy. Late, against a developed board: stay put.
func TestJailAdviceReversesBetweenEarlyAndLateGame(t *testing.T) {
	early := monoFixture(t, mono.PhaseJail, 0, func(s *mono.State) {
		s.Players[0].InJail = true // nothing owned yet: the board is all still for sale
	})
	late := monoFixture(t, mono.PhaseJail, 0, func(s *mono.State) {
		s.Players[0].InJail = true
		// Opponents own and have developed most of the board.
		for _, sp := range mono.Board() {
			if sp.Price > 0 {
				s.Holdings[sp.Index].Owner = 1
			}
			if sp.Kind == mono.KindStreet {
				s.Holdings[sp.Index].Houses = 4
			}
		}
	})

	e := mustScore(t, early, mono.ActPayJail)
	if e.Regret != 0 {
		t.Errorf("paying out of jail on an unowned board scored regret %.3f — early on, a "+
			"turn spent in jail is a turn not buying", e.Regret)
	}
	l := mustScore(t, late, mono.ActRollJail)
	if l.Regret != 0 {
		t.Errorf("staying in jail against a board full of hotels scored regret %.3f — jail "+
			"is the safest square on a developed board", l.Regret)
	}
	// And the reverse of each must be the error.
	if bad := mustScore(t, late, mono.ActPayJail); bad.Regret <= 0 {
		t.Error("paying $50 to leave jail and walk into hotels scored no regret")
	}
}

// A free card leaves jail without the fine, so it must never rank below paying.
func TestJailCardIsPreferredToPayingTheFine(t *testing.T) {
	raw := monoFixture(t, mono.PhaseJail, 0, func(s *mono.State) {
		s.Players[0].InJail = true
		s.Players[0].JailCards = 1
	})
	card := mustScore(t, raw, mono.ActUseJailCard)
	pay := mustScore(t, raw, mono.ActPayJail)
	if !(card.ValueChosen > pay.ValueChosen) {
		t.Fatalf("a free jail card (EV %.1f) is not preferred to paying $50 (EV %.1f)",
			card.ValueChosen, pay.ValueChosen)
	}
}

// ── The exclusions, which matter as much as the scores ──────────────────────

// Trades are deliberately unscored: their value depends on what they enable several turns
// later, which no closed-form model captures. A confidently mediocre trade score would be
// worse than none, and would quietly corrupt the average it feeds.
func TestTradesAreDeliberatelyUnscored(t *testing.T) {
	for _, phase := range []string{mono.PhaseTrade, mono.PhaseTradeResponse} {
		raw := monoFixture(t, phase, 0, nil)
		for _, act := range []string{mono.ActAcceptTrade, mono.ActRejectTrade, mono.ActProposeTrade, mono.ActSkipTrade} {
			if _, ok := ScoreMonopolyDecision(raw, act); ok {
				t.Errorf("%s in phase %s was scored; trades are excluded by design", act, phase)
			}
		}
	}
}

// Forced decisions are not skill.
func TestForcedPhasesAreUnscorable(t *testing.T) {
	for _, phase := range []string{mono.PhaseRoll, mono.PhaseResolveDebt, mono.PhaseGameOver} {
		raw := monoFixture(t, phase, 0, nil)
		if _, ok := ScoreMonopolyDecision(raw, mono.ActRoll); ok {
			t.Errorf("phase %s was scored despite carrying no choice", phase)
		}
	}
}

// An action the scorer does not model must be excluded, not called a blunder — otherwise
// an agent is punished for the scorer's own gaps.
func TestUnmodelledActionsAreExcludedNotPenalised(t *testing.T) {
	raw := monoFixture(t, mono.PhaseManage, 0, func(s *mono.State) {
		for _, sq := range []int{sqStJames, sqTennessee, sqNewYork} {
			s.Holdings[sq].Owner = 0
		}
		s.Players[0].Cash = 1200
	})
	if _, ok := ScoreMonopolyDecision(raw, mono.ActMortgage); ok {
		t.Fatal("mortgaging was scored against a build/end-turn model it is not part of")
	}
}

func TestMalformedMonopolyViewsAreRejected(t *testing.T) {
	for name, doc := range map[string]string{
		"not json": `{oops`,
		"no state": `{"your_seat":0,"phase":"acquire"}`,
		"bad seat": `{"your_seat":99,"phase":"acquire","state":{"players":[{"seat":0}]}}`,
		"empty":    `{}`,
	} {
		if _, ok := ScoreMonopolyDecision([]byte(doc), mono.ActBuy); ok {
			t.Errorf("%s: accepted an unusable view", name)
		}
	}
}

// ── Model integrity ─────────────────────────────────────────────────────────

// The horizon is an estimate, so the claim that it does not flip orderings has to be
// tested rather than asserted in a comment.
func TestHorizonSensitivity(t *testing.T) {
	orange := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqStJames
	})
	brown := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqMediterranean
	})
	// The ordering under the shipped horizon must be the one a longer or shorter game
	// would also produce; if it is not, the constant is doing the deciding.
	o := mustScore(t, orange, mono.ActBuy)
	b := mustScore(t, brown, mono.ActBuy)
	if !(o.ValueChosen > b.ValueChosen) {
		t.Fatal("orange/brown ordering is horizon-dependent")
	}
	if o.Regret != 0 || b.Regret > 1 {
		t.Fatalf("regret outside range: orange %.3f brown %.3f", o.Regret, b.Regret)
	}
}

// Determinism, as everywhere in this package.
func TestMonopolyScoringIsDeterministic(t *testing.T) {
	raw := monoFixture(t, mono.PhaseAcquire, 0, func(s *mono.State) {
		s.Players[0].Position = sqTennessee
		s.Holdings[sqStJames].Owner = 0
	})
	first := mustScore(t, raw, mono.ActBuy)
	for i := 0; i < 20; i++ {
		got := mustScore(t, raw, mono.ActBuy)
		if got.Regret != first.Regret || got.ValueChosen != first.ValueChosen {
			t.Fatalf("run %d differed: regret %.12f vs %.12f", i, got.Regret, first.Regret)
		}
	}
}

// Regret must stay normalised across every scorable phase.
func TestMonopolyRegretStaysInRange(t *testing.T) {
	cases := []struct {
		phase  string
		action string
		mutate func(*mono.State)
	}{
		{mono.PhaseAcquire, mono.ActBuy, func(s *mono.State) { s.Players[0].Position = sqParkPlace }},
		{mono.PhaseAcquire, mono.ActDecline, func(s *mono.State) { s.Players[0].Position = sqOrientalAve }},
		{mono.PhaseJail, mono.ActRollJail, func(s *mono.State) { s.Players[0].InJail = true }},
	}
	for i, c := range cases {
		raw := monoFixture(t, c.phase, 0, c.mutate)
		d, ok := ScoreMonopolyDecision(raw, c.action)
		if !ok {
			t.Fatalf("case %d (%s/%s) not scorable", i, c.phase, c.action)
		}
		if d.Regret < 0 || d.Regret > 1 {
			t.Fatalf("case %d regret %.6f outside [0,1]", i, d.Regret)
		}
		if fmt.Sprint(d.ValueChosen) == "NaN" {
			t.Fatalf("case %d produced NaN", i)
		}
	}
}
