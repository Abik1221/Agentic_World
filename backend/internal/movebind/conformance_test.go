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

type bindingDoc struct {
	BindingVersion string        `json:"binding_version"`
	Cases          []bindingCase `json:"cases"`
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
