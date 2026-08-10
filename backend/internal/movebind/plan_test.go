package movebind

import "testing"

// Range bindings: one completion covering several rounds.
//
// The metric these exist to fix: coverage counted CALLS, so an agent whose single call planned
// three rounds scored ~33% on real staked tables while playing entirely model-backed. Phase 4
// rewards exactly that batching as cost optimisation, so the two rules pulled in opposite
// directions and no threshold reconciled them.

func plan(items ...map[string]any) map[string]any {
	list := make([]any, 0, len(items))
	for _, it := range items {
		list = append(list, it)
	}
	return map[string]any{"plan": list}
}

func TestABatchedCompletionCoversEveryRoundItDecided(t *testing.T) {
	tc := ToolCall{Name: ToolGoofspiel, Args: plan(
		map[string]any{"round": 4.0, "card": 7.0},
		map[string]any{"round": 5.0, "card": 2.0},
		map[string]any{"round": 6.0, "card": 9.0},
	)}
	got, ok := CanonPlan(GameGoofspiel, tc, 4)
	if !ok {
		t.Fatal("a three-round plan bound nothing")
	}
	want := []RoundMove{{4, "card:7"}, {5, "card:2"}, {6, "card:9"}}
	if len(got) != len(want) {
		t.Fatalf("covered %d rounds %v, want 3 — this IS the batching fix: one call, three "+
			"model-backed decisions", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("round %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A plain single-round call must behave exactly as before. Every agent in production today
// sends this shape, so a regression here is a platform-wide outage rather than a lost feature.
func TestASingleRoundCallStillBindsItsOwnRound(t *testing.T) {
	tc := ToolCall{Name: ToolGoofspiel, Args: map[string]any{"card": 3.0}}
	got, ok := CanonPlan(GameGoofspiel, tc, 9)
	if !ok || len(got) != 1 {
		t.Fatalf("CanonPlan(single) = %v, %v; want one round", got, ok)
	}
	if got[0].Round != 9 || got[0].Move != "card:3" {
		t.Errorf("got %+v, want {9 card:3} — a call with no plan binds the round its proof "+
			"attests, which is the pre-existing behaviour", got[0])
	}
}

// THE security property. A span may reach forward, because every forward round is enforced
// when it is submitted. It must never reach BACKWARD: those moves are already sealed, so a
// backward claim is coverage that nothing will ever check.
func TestASpanCannotReachBackwardsIntoRoundsAlreadyPlayed(t *testing.T) {
	tc := ToolCall{Name: ToolGoofspiel, Args: plan(
		map[string]any{"round": 1.0, "card": 1.0}, // already played, unbound — must not count
		map[string]any{"round": 2.0, "card": 2.0}, // ditto
		map[string]any{"round": 8.0, "card": 8.0}, // the proven round
		map[string]any{"round": 9.0, "card": 9.0}, // forward: allowed, and will be enforced
	)}
	got, ok := CanonPlan(GameGoofspiel, tc, 8)
	if !ok {
		t.Fatal("bound nothing")
	}
	if len(got) != 2 {
		t.Fatalf("covered %v, want only rounds 8 and 9 — a call proven for round 8 must not "+
			"retroactively claim rounds 1 and 2, which were played unbound and can no longer "+
			"be checked against anything", got)
	}
	for _, rm := range got {
		if rm.Round < 8 {
			t.Errorf("round %d is before the proven round", rm.Round)
		}
	}
}

// Two moves for one slot has no honest reading, and choosing either would be the platform
// guessing which one the agent meant. Bind nothing instead — absence never rejects, so the
// agent simply plays unbound.
func TestAPlanThatNamesARoundTwiceBindsNothing(t *testing.T) {
	tc := ToolCall{Name: ToolGoofspiel, Args: plan(
		map[string]any{"round": 4.0, "card": 7.0},
		map[string]any{"round": 4.0, "card": 8.0},
	)}
	if got, ok := CanonPlan(GameGoofspiel, tc, 4); ok {
		t.Errorf("CanonPlan = %v, want nothing bound for a contradictory plan", got)
	}
}

// The plan is attacker-supplied, so its length must be bounded before it becomes that many
// database writes.
func TestAnOversizedSpanIsRefused(t *testing.T) {
	items := make([]map[string]any, 0, MaxSpanRounds+1)
	for i := 0; i <= MaxSpanRounds; i++ {
		items = append(items, map[string]any{"round": float64(i + 1), "card": 1.0})
	}
	if got, ok := CanonPlan(GameGoofspiel, ToolCall{Name: ToolGoofspiel, Args: plan(items...)}, 1); ok {
		t.Errorf("bound %d rounds, want refusal above MaxSpanRounds=%d", len(got), MaxSpanRounds)
	}
}

// One malformed entry must not discard the rounds the model did decide honestly.
func TestAMalformedEntryDoesNotVoidTheRestOfThePlan(t *testing.T) {
	tc := ToolCall{Name: ToolGoofspiel, Args: plan(
		map[string]any{"round": 4.0, "card": 7.0},
		map[string]any{"card": 5.0},                // no round: unplaceable
		map[string]any{"round": 6.0},               // no card: not a move
		map[string]any{"round": 7.0, "card": 11.0}, // fine
	)}
	got, ok := CanonPlan(GameGoofspiel, tc, 4)
	if !ok || len(got) != 2 {
		t.Fatalf("CanonPlan = %v (ok=%v), want rounds 4 and 7 to survive", got, ok)
	}
}

// Range binding is not Goofspiel-only. Mafia's "no target" convention has to survive it —
// seat 0 is a real player, so an absent target must stay distinguishable from targeting them.
func TestASpanKeepsMafiaNoTargetDistinctFromSeatZero(t *testing.T) {
	tc := ToolCall{Name: ToolMafia, Args: plan(
		map[string]any{"round": 2.0, "kind": "kill", "target": 0.0},
		map[string]any{"round": 3.0, "kind": "abstain"},
	)}
	got, ok := CanonPlan(GameMafia, tc, 2)
	if !ok || len(got) != 2 {
		t.Fatalf("CanonPlan(mafia) = %v (ok=%v)", got, ok)
	}
	if got[0].Move != "kill:0" {
		t.Errorf("round 2 = %q, want kill:0 — seat 0 is a real player", got[0].Move)
	}
	if got[1].Move != "abstain:none" {
		t.Errorf("round 3 = %q, want abstain:none — an absent target is NOT seat 0", got[1].Move)
	}
}

// A plan for the wrong game's tool binds nothing, same as a single call does.
func TestAPlanOnTheWrongToolBindsNothing(t *testing.T) {
	tc := ToolCall{Name: ToolMafia, Args: plan(map[string]any{"round": 1.0, "card": 4.0})}
	if got, ok := CanonPlan(GameGoofspiel, tc, 1); ok {
		t.Errorf("CanonPlan = %v, want nothing: this is not Goofspiel's move tool", got)
	}
}
