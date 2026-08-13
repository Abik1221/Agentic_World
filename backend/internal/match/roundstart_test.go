package match

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"
)

// These pin the two defects behind migration 0089: a round after the first ran on the
// static window, and think-time was reconstructed from a deadline that moves.

// commit() sets the deadline for every round after the first, and it used the CONFIGURED
// constant while every other round-opening path used the adaptive window. An agent that
// had earned a longer window got it once and then silently dropped back to the default for
// rounds 2..N — exactly the failure the adaptive window exists to prevent.
//
// Asserted on the AST of the function itself, following the precedent in
// deadline_propagation_test.go. The obvious test — call s.moveWindow and check the answer —
// is worthless here: moveWindow was always correct, and that test stays green with the bug
// fully restored. What had to be pinned is that commit CALLS it. (The first version of this
// test made exactly that mistake and is preserved below as
// TestMoveWindowPrefersTheSlowerSeat, which is a real but different assertion.)
func TestCommitOpensTheRoundWithTheAdaptiveWindow(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service.go: %v", err)
	}

	var checked, callsMoveWindow, usesStaticConfig bool
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "commit" {
			return true
		}
		checked = true
		ast.Inspect(fn, func(inner ast.Node) bool {
			switch node := inner.(type) {
			case *ast.CallExpr:
				if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil &&
					sel.Sel.Name == "moveWindow" {
					callsMoveWindow = true
				}
			case *ast.SelectorExpr:
				// s.cfg.MoveWindow — the static constant this fix removed.
				if node.Sel != nil && node.Sel.Name == "MoveWindow" {
					if inner2, ok := node.X.(*ast.SelectorExpr); ok && inner2.Sel != nil &&
						inner2.Sel.Name == "cfg" {
						usesStaticConfig = true
					}
				}
			}
			return true
		})
		return false
	})

	if !checked {
		t.Fatal("commit() not found in service.go — this guard is silently testing nothing")
	}
	if !callsMoveWindow {
		t.Error("commit() does not call moveWindow: rounds after the first are not adaptive")
	}
	if usesStaticConfig {
		t.Error("commit() still reads cfg.MoveWindow — the static window is back")
	}
}

// A real assertion, but NOT a guard on the bug above: moveWindow was already correct when
// rounds 2..N were running on the static constant, so this passes either way. Kept because
// the shared-deadline rule it pins is worth pinning.
func TestMoveWindowPrefersTheSlowerSeat(t *testing.T) {
	const base = 20 * time.Second
	const earned = 90 * time.Second

	w := &stubWindows{byAgent: map[string]time.Duration{"ag_slow": earned, "ag_fast": 5 * time.Second}}
	s := svcWithWindow(base, w)

	m := Match{Players: []Player{
		{AgentPublicID: "ag_slow", Seat: 0},
		{AgentPublicID: "ag_fast", Seat: 1},
	}}

	// This is the call commit() makes. Before the fix it was s.cfg.MoveWindow, which is
	// `base` and would ignore the provider entirely.
	got := s.moveWindow(context.Background(), m.agentIDs()...)
	if got != earned {
		t.Fatalf("round-2 window = %v, want the earned %v (static default is %v)", got, earned, base)
	}
}

func TestAgentIDsSkipsEmptySeats(t *testing.T) {
	// An unseated or bot row with no agent id must not be handed to the window provider as
	// an empty string — moveWindow already ignores those, but passing them means a lookup
	// per round for a key that cannot exist.
	m := Match{Players: []Player{
		{AgentPublicID: "ag_a"},
		{AgentPublicID: ""},
		{AgentPublicID: "ag_b"},
	}}
	got := m.agentIDs()
	if len(got) != 2 || got[0] != "ag_a" || got[1] != "ag_b" {
		t.Fatalf("agentIDs() = %v, want [ag_a ag_b]", got)
	}
}

func TestRoundStartPrefersTheRecordedValue(t *testing.T) {
	s := svcWithWindow(20*time.Second, nil)
	recorded := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	// A deadline that disagrees with the recorded start — which is exactly what an
	// extension produces. The recorded value must win.
	deadline := recorded.Add(5 * time.Minute)

	got, ok := s.roundStart(Match{RoundStartedAt: &recorded, RoundDeadline: &deadline})
	if !ok {
		t.Fatal("no round start despite a recorded value")
	}
	if !got.Equal(recorded) {
		t.Fatalf("round start = %v, want the recorded %v", got, recorded)
	}
}

// The measurement error that mattered most. An extension pushes RoundDeadline forward, so
// deriving the start from it made the round look like it began LATER than it did, and the
// agent's response time came out smaller than reality. That number feeds the timing profile
// used to decide whether a human is playing by hand — so it was masking slowness in a
// fraud control.
func TestAnExtensionNoLongerShrinksTheMeasuredThinkTime(t *testing.T) {
	const window = 60 * time.Second
	s := svcWithWindow(window, nil)

	start := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	originalDeadline := start.Add(window)
	extendedDeadline := originalDeadline.Add(90 * time.Second) // two liveness-gated grants
	answeredAt := start.Add(140 * time.Second)                 // the agent really took 140s

	withRecord, ok := s.roundStart(Match{RoundStartedAt: &start, RoundDeadline: &extendedDeadline})
	if !ok {
		t.Fatal("no round start")
	}
	measured := answeredAt.Sub(withRecord)
	if measured != 140*time.Second {
		t.Fatalf("measured think-time %v, want the true 140s", measured)
	}

	// And the shape of the old bug, for the record: reconstructing from the EXTENDED
	// deadline understates the think time badly.
	reconstructed := answeredAt.Sub(extendedDeadline.Add(-window))
	if reconstructed >= measured {
		t.Fatalf("reconstruction %v did not understate the true %v — the test no longer "+
			"demonstrates the bug it was written for", reconstructed, measured)
	}
}

func TestRoundStartFallsBackForRowsPredatingTheColumn(t *testing.T) {
	// Nil RoundStartedAt is a round that was already in flight when 0089 shipped. The old
	// reconstruction is worse, but it is exactly the previous behaviour — which is the
	// right thing to degrade to.
	const window = 30 * time.Second
	s := svcWithWindow(window, nil)
	deadline := time.Date(2026, 8, 13, 9, 1, 0, 0, time.UTC)

	got, ok := s.roundStart(Match{RoundDeadline: &deadline})
	if !ok {
		t.Fatal("no round start from the fallback")
	}
	if want := deadline.Add(-window); !got.Equal(want) {
		t.Fatalf("fallback start = %v, want %v", got, want)
	}
}

// The bug this shape prevents: returning a zero Time instead of ok=false would be silently
// converted into a think-time of decades and poured into the sample set that decides
// whether an agent is a human.
func TestNoDeadlineAndNoRecordReportsUnknownRatherThanZero(t *testing.T) {
	s := svcWithWindow(30*time.Second, nil)
	got, ok := s.roundStart(Match{})
	if ok {
		t.Fatalf("claimed a round start of %v with nothing to anchor to", got)
	}
	if !got.IsZero() {
		t.Errorf("expected the zero Time alongside ok=false, got %v", got)
	}
}
