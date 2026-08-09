package movebind

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Cross-language conformance for completion binding, driven by shared fixtures.
//
// The expectations live in sdk/conformance/move_binding.json and are read by this test, the
// Python SDK's test_move_binding.py and the JS SDK's move-binding.test.ts.
//
// The point is drift. Three implementations of this reduction exist and nothing in any of them
// forces the three to agree; each language's own tests would keep passing while they diverged.
// And a divergence here does not surface as a visible bug — it surfaces as an HONEST TURN
// BEING REJECTED, because the gateway reduced the model's answer one way and the match
// compared it against the same move reduced another way.
//
// This side matters most. The gateway is the authoritative observer: its reduction is the one
// stored, attested by an HMAC, and defended if a developer disputes a rejection.

type bindingCase struct {
	Name     string          `json:"name"`
	Why      string          `json:"why"`
	Game     string          `json:"game"`
	Tool     string          `json:"tool"`
	Response json.RawMessage `json:"response"`
	// ExpectMove is nil when NOTHING may be bound. A pointer rather than a string so the
	// fixture can distinguish "binds nothing" from "binds the empty move" — collapsing those
	// would make the most important cases in the file unassertable.
	ExpectMove *string `json:"expect_move"`
}

// planCase is a completion that decided a RANGE of rounds. ExpectPlan nil means nothing may
// be bound, for the same reason ExpectMove uses a pointer: "binds nothing" and "binds an
// empty plan" are different answers and the security cases assert the first.
type planCase struct {
	Name        string          `json:"name"`
	Why         string          `json:"why"`
	Game        string          `json:"game"`
	Tool        string          `json:"tool"`
	ProvenRound int             `json:"proven_round"`
	Response    json.RawMessage `json:"response"`
	ExpectPlan  []struct {
		Round int    `json:"round"`
		Move  string `json:"move"`
	} `json:"expect_plan"`
}

type bindingDoc struct {
	BindingVersion string        `json:"binding_version"`
	Cases          []bindingCase `json:"cases"`
	PlanCases      []planCase    `json:"plan_cases"`
}

func loadBindingConformance(t *testing.T) bindingDoc {
	t.Helper()
	path := filepath.Join("..", "..", "..", "sdk", "conformance", "move_binding.json")
	b, err := os.ReadFile(path)
	if err != nil {
		// Fails loudly rather than skipping. A conformance suite that quietly finds no
		// fixtures and reports "passed" is the exact failure it exists to prevent: the three
		// implementations would drift with nothing objecting.
		t.Fatalf("read shared move-binding fixtures at %s: %v\n"+
			"These fixtures are shared with the Python and JS SDKs and live in the sibling "+
			"sdk/ directory. Run tests from a full checkout of the repository.", path, err)
	}
	var doc bindingDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(doc.Cases) == 0 {
		t.Fatalf("%s contains no cases", path)
	}
	return doc
}

func TestMoveBindingConformance(t *testing.T) {
	doc := loadBindingConformance(t)
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			// The fixture names the tool explicitly AND the game, so this also pins that the
			// two agree — a game whose ToolFor disagreed with the fixture would mean the SDKs
			// are being told to send a tool the gateway does not look for.
			if got := ToolFor(c.Game); got != c.Tool {
				t.Fatalf("ToolFor(%q) = %q, fixture expects %q", c.Game, got, c.Tool)
			}
			tc, ok := Extract(c.Response, c.Tool)
			var move string
			if ok {
				move, ok = Canon(c.Game, tc)
			}
			if c.ExpectMove == nil {
				if ok {
					t.Fatalf("bound %q, but this response must bind NOTHING.\nwhy: %s", move, c.Why)
				}
				return
			}
			if !ok {
				t.Fatalf("bound nothing, want %q.\nwhy: %s", *c.ExpectMove, c.Why)
			}
			if move != *c.ExpectMove {
				t.Fatalf("bound %q, want %q.\nwhy: %s", move, *c.ExpectMove, c.Why)
			}
		})
	}
}

// The RANGE half of the same contract: one completion that decided several rounds.
//
// Held in the shared fixture for exactly the reason the single-move cases are. The gateway
// writes a bound row per round in the span and the match compares each one against what the
// agent submits for that round — so if the SDK builds a plan the gateway reduces differently,
// the platform rejects an honest turn in the middle of a batched sequence, which is both the
// worst failure and the hardest to debug from either side.
func TestMoveBindingPlanConformance(t *testing.T) {
	doc := loadBindingConformance(t)
	if len(doc.PlanCases) == 0 {
		t.Fatal("no plan_cases in the shared fixture: the range-binding contract is unpinned")
	}
	for _, c := range doc.PlanCases {
		t.Run(c.Name, func(t *testing.T) {
			if got := ToolFor(c.Game); got != c.Tool {
				t.Fatalf("ToolFor(%q) = %q, fixture expects %q", c.Game, got, c.Tool)
			}
			tc, ok := Extract(c.Response, c.Tool)
			var got []RoundMove
			if ok {
				got, ok = CanonPlan(c.Game, tc, c.ProvenRound)
			}
			if c.ExpectPlan == nil {
				if ok {
					t.Fatalf("bound %v, but this response must bind NOTHING.\nwhy: %s", got, c.Why)
				}
				return
			}
			if !ok {
				t.Fatalf("bound nothing, want %d rounds.\nwhy: %s", len(c.ExpectPlan), c.Why)
			}
			if len(got) != len(c.ExpectPlan) {
				t.Fatalf("covered %d rounds %v, want %d.\nwhy: %s",
					len(got), got, len(c.ExpectPlan), c.Why)
			}
			for i, want := range c.ExpectPlan {
				if got[i].Round != want.Round || got[i].Move != want.Move {
					t.Errorf("round[%d] = {%d %q}, want {%d %q}.\nwhy: %s",
						i, got[i].Round, got[i].Move, want.Round, want.Move, c.Why)
				}
			}
		})
	}
}
