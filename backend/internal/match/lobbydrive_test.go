package match

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Every path that makes a match ACTIVE must also start driving it.
//
// The lobby route did not, and the consequence was that a whole staked surface silently
// did nothing: an agent created a table, an opponent joined, both stakes went into escrow,
// and then neither agent was ever asked to play. The sweeper force-timed-out every round
// and the match resolved on fallbacks alone. Observed on a real staked table — seven
// rounds in, timeouts [7,7], not one agent ever asked to think.
//
// Only CreatePairedActive called maybeDrive, so queue-matched games worked and lobby games
// did not. That split is invisible in any test that drives one path, which is exactly why
// this test looks at ALL of them.
//
// Structural rather than behavioural on purpose: maybeDrive spawns a goroutine and returns
// nothing, so there is no return value to assert on and a behavioural test would have to
// stand up a driver, a gateway and a resolver to observe a side effect. What actually
// needs guarding is simpler and more durable — that no future activation path forgets the
// call. A new one shows up here as a failure with the name of the function that forgot.
func TestEveryActivationPathStartsTheDriver(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service.go: %v", err)
	}

	// Functions that transition a match into play. Each must call maybeDrive.
	activators := map[string]bool{
		"CreatePaired": false, // queue / matchmaking (repo call inside is CreatePairedActive)
		"Join":         false, // lobby — the one that was missing it
	}

	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			return true
		}
		if _, watched := activators[fn.Name.Name]; !watched {
			return true
		}
		ast.Inspect(fn, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel != nil && sel.Sel.Name == "maybeDrive" {
				activators[fn.Name.Name] = true
			}
			return true
		})
		return true
	})

	for name, drives := range activators {
		if !drives {
			t.Errorf("%s activates a match but never calls maybeDrive — agents on that path "+
				"are staked and then never asked to play; every round resolves on the "+
				"sweeper's fallback", name)
		}
	}
}

// A sanity check on the test itself: if maybeDrive is ever renamed, the scan above would
// quietly find nothing and pass. Assert the method still exists under that name.
func TestMaybeDriveStillExists(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "drive.go", nil, 0)
	if err != nil {
		t.Fatalf("parse drive.go: %v", err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == "maybeDrive" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("maybeDrive no longer exists in drive.go, so the activation scan above " +
			"cannot detect anything and would pass vacuously")
	}
}
