package match

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platform"
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

// ── the phase warning in the view ─────────────────────────────────────────────

// The warning must be derived from the window ACTUALLY IN FORCE, which is the deadline
// minus the recorded round start. Recomputing it from the policy would consult the agent's
// latency samples again — they have moved on since the round opened — and could produce a
// warning that disagrees with the deadline being enforced.
func TestViewWarningSitsInsideTheWindowActuallyInForce(t *testing.T) {
	s := svcWithWindow(20*time.Second, nil)
	s.clock = platform.FixedClock{T: time.Date(2026, 8, 13, 12, 0, 5, 0, time.UTC)}

	start := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	// 90s in force — deliberately NOT the 20s configured window, so a view that recomputed
	// from config instead of reading the stored pair would produce a different answer.
	dl := start.Add(90 * time.Second)

	v := s.view(Match{
		PublicID: "m_warn", Game: "goofspiel", Status: StatusActive,
		RoundDeadline: &dl, RoundStartedAt: &start,
		Players: []Player{{AgentPublicID: "ag_a", Seat: 0}, {AgentPublicID: "ag_b", Seat: 1}},
	}, "ag_a")

	if v.WarnAt == nil {
		t.Fatal("no warning on a 90s window")
	}
	if !v.WarnAt.After(start) {
		t.Errorf("warning at %v is not after the round start %v", v.WarnAt, start)
	}
	if !v.WarnAt.Before(dl) {
		t.Errorf("warning at %v is not before the deadline %v", v.WarnAt, dl)
	}
	// 20% of 90s is 18s, so the warning lands 18s before the deadline. If the view had used
	// the 20s CONFIG window it would be 4s, which this catches.
	if got, want := dl.Sub(*v.WarnAt), 18*time.Second; got != want {
		t.Errorf("lead = %v, want %v (20%% of the 90s window in force, not of the config)", got, want)
	}
}

// A round already in flight when migration 0089 shipped has no recorded start. The warning
// must be ABSENT rather than reconstructed: inferring the start means subtracting a window
// we do not know, which is the guess that corrupted think-time in the first place.
func TestViewOmitsTheWarningWhenTheRoundStartIsUnknown(t *testing.T) {
	s := svcWithWindow(20*time.Second, nil)
	s.clock = platform.FixedClock{T: time.Date(2026, 8, 13, 12, 0, 5, 0, time.UTC)}
	dl := time.Date(2026, 8, 13, 12, 1, 30, 0, time.UTC)

	v := s.view(Match{
		PublicID: "m_warn_nil", Game: "goofspiel", Status: StatusActive,
		RoundDeadline: &dl, // no RoundStartedAt
		Players:       []Player{{AgentPublicID: "ag_a", Seat: 0}, {AgentPublicID: "ag_b", Seat: 1}},
	}, "ag_a")

	if v.WarnAt != nil {
		t.Errorf("warning %v invented for a round with no recorded start", v.WarnAt)
	}
	if v.WarnInMs != 0 {
		t.Errorf("warn_in_ms = %d with no warning; a caller reading it would fire immediately", v.WarnInMs)
	}
}

// A turn too short to warn about must produce nothing at all — not a warning at the epoch.
func TestViewOmitsTheWarningOnATurnTooShortToUseOne(t *testing.T) {
	s := svcWithWindow(20*time.Second, nil)
	s.clock = platform.FixedClock{T: time.Date(2026, 8, 13, 12, 0, 1, 0, time.UTC)}
	start := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	dl := start.Add(4 * time.Second) // under 2×MinWarnLead

	v := s.view(Match{
		PublicID: "m_warn_short", Game: "goofspiel", Status: StatusActive,
		RoundDeadline: &dl, RoundStartedAt: &start,
		Players: []Player{{AgentPublicID: "ag_a", Seat: 0}, {AgentPublicID: "ag_b", Seat: 1}},
	}, "ag_a")

	if v.WarnAt != nil {
		t.Errorf("warning %v on a 4s window — the notice and the deadline are indistinguishable there", v.WarnAt)
	}
}
