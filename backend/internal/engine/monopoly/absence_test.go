package monopoly

import (
	"fmt"
	"testing"
)

// absentAgent never decides anything. Every one of its turns is answered by the
// engine's own deterministic default, exactly as the sweeper does for an agent whose
// endpoint stopped responding.
type absentAgent struct{ seat int }

func (a absentAgent) Name() string { return fmt.Sprintf("absent-%d", a.seat) }
func (a absentAgent) Decide(e *Engine, s State, _ int) Action {
	d := e.defaultAction(s)
	d.Forced = true
	return d
}

func seedFor(tag string) []byte {
	b := make([]byte, 32)
	copy(b, tag)
	return b
}

// Monopoly needs no special absence rule, and this is the evidence for that claim.
//
// Goofspiel and Mafia punish absence structurally: the fallback plays your WORST card,
// or casts no vote at all. The worry with Monopoly was that its fallback might be
// survivable or even profitable — "buy a monopoly, walk away, collect rent" — which
// would make going absent a strategy rather than a failure, and the arena's rule is
// that absence is the absent agent's own fault.
//
// It is not survivable, because the default action set compounds against the seat:
//
//   - PhaseAcquire → decline, and PhaseAuction → pass, so the portfolio is frozen at
//     whatever it held when it went dark and can never grow.
//   - PhaseManage → end turn, so it never builds. Rent on an undeveloped monopoly is
//     a rounding error next to a hotel, so its income stops growing while everyone
//     else's compounds.
//   - PhaseResolveDebt → BANKRUPT, not mortgage-and-survive. The first rent bill it
//     cannot cover in cash ends it, while a present agent would sell houses and live.
//
// Run over many seeds so the conclusion is about the rules and not about one lucky
// dice sequence.
func TestAbsentSeatDoesNotWinMonopoly(t *testing.T) {
	const games = 40
	wins, bankruptcies := 0, 0

	for i := 0; i < games; i++ {
		seed := seedFor(fmt.Sprintf("absence-%03d", i))
		cfg := Config{Players: 4, StartingCash: 1500, MaxTurns: DefaultMaxTurns, GoSalary: 200}

		agents := []Agent{
			absentAgent{seat: 0}, // the seat that walked away
			NewBot("tycoon", StyleTycoon, seed, 1),
			NewBot("banker", StyleBanker, seed, 2),
			NewBot("cautious", StyleCautious, seed, 3),
		}
		tbl := NewTable(cfg, seed, agents)
		tbl.PlayOut()

		st := tbl.State()
		if !st.Finished {
			t.Fatalf("seed %d: table never finished", i)
		}
		if st.Winner == 0 {
			wins++
		}
		if st.Players[0].Bankrupt {
			bankruptcies++
		}
	}

	// The claim is not "it is impossible to win while absent" — dice are dice, and a
	// hard guarantee would need a forfeit rule we deliberately did not add. The claim is
	// that absence is a losing line, so walking away is never the profitable play.
	if wins*4 > games {
		t.Fatalf("an absent seat won %d/%d games — better than its 1-in-4 share. Monopoly's "+
			"fallback is not self-punishing after all, and the game needs an explicit "+
			"absence forfeit like the one this test was written to show is unnecessary",
			wins, games)
	}
	if bankruptcies*2 < games {
		t.Errorf("an absent seat went bankrupt in only %d/%d games; the default action set "+
			"is meant to compound against it", bankruptcies, games)
	}
	t.Logf("absent seat: won %d/%d, went bankrupt %d/%d", wins, games, bankruptcies, games)
}

// Attendance must be recorded from the forced flag, not inferred from the action kind:
// "end turn" is a perfectly normal thing for a present agent to choose.
func TestForcedFlagDrivesAbsenceNotActionKind(t *testing.T) {
	seed := seedFor("forced-flag")
	cfg := Config{Players: 2, StartingCash: 1500, MaxTurns: 40, GoSalary: 200}
	e := New(cfg)
	s, _ := e.Init(seed)

	// A seat that deliberately plays the same action the fallback would is PRESENT.
	seat := e.pendingActor(s)
	voluntary := e.defaultAction(s) // no Forced flag — this is the agent's own choice
	ns, _, err := e.Step(s, seat, voluntary, seed)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if ns.Timeouts[seat] != 0 {
		t.Fatalf("a deliberate action was counted as a timeout (%d) — an agent that plays "+
			"conservatively would be treated as absent and could lose its stake for it",
			ns.Timeouts[seat])
	}
	if ns.Asks[seat] != 1 {
		t.Fatalf("asks=%d want 1", ns.Asks[seat])
	}
	if ns.SeatWasAbsent(seat) {
		t.Fatal("a present seat was judged absent")
	}
}

// And the forced path must actually mark it, or settlement can never tell the two apart.
func TestForceTimeoutMarksTheSeatAbsent(t *testing.T) {
	seed := seedFor("force-marks")
	cfg := Config{Players: 2, StartingCash: 1500, MaxTurns: 40, GoSalary: 200}
	e := New(cfg)
	s, _ := e.Init(seed)

	seat := e.pendingActor(s)
	ns, _, err := e.ForceTimeout(s, seed)
	if err != nil {
		t.Fatalf("ForceTimeout: %v", err)
	}
	if ns.Timeouts[seat] != 1 {
		t.Fatalf("timeouts=%d want 1 — a forced turn was not recorded, so an absent seat "+
			"looks identical to a scripted one at settlement", ns.Timeouts[seat])
	}
	if !ns.SeatWasAbsent(seat) {
		t.Fatal("one forced turn out of one ask is not being read as absence")
	}
}
