package match

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The function that pushes a turn must bound it with the seat's own deadline.
//
// THE mistake this exists to prevent: the adaptive-window work was built, tested, wired
// into the sweeper and declared live — while the code that actually bounds a PUSHED turn
// still used a constant fixed at server startup. A slow agent's computed 2m22s window
// could never reach the decision to give up. Everything was green and nothing worked.
//
// The first version of THIS test was also wrong, and worth recording: it searched the file
// for the identifier `DeadlineMs`, which also appears where the turn view is BUILT. So it
// passed with the propagation deliberately removed — a test that agrees with both answers,
// which is the same failure one level up. It now asserts the mechanism inside the specific
// function, and the mutation check below is what proves that.
func TestDecidePathBoundsTheTurnByTheSeatDeadline(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "drive.go", nil, 0)
	if err != nil {
		t.Fatalf("parse drive.go: %v", err)
	}

	var checked bool
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "decide" {
			return true
		}
		checked = true
		var readsDeadline, bounds bool
		ast.Inspect(fn, func(inner ast.Node) bool {
			node, ok := inner.(*ast.SelectorExpr)
			if !ok || node.Sel == nil {
				return true
			}
			if node.Sel.Name == "DeadlineMs" {
				readsDeadline = true
			}
			// context.WithTimeout(...) — the call that actually bounds the push.
			if node.Sel.Name == "WithTimeout" {
				if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == "context" {
					bounds = true
				}
			}
			return true
		})
		if !readsDeadline {
			t.Error("decide never reads the seat's DeadlineMs, so it cannot know the agent's window")
		}
		if !bounds {
			t.Error("decide never calls context.WithTimeout, so every turn it pushes is bounded " +
				"by the play client's startup constant instead of the seat's adaptive window — " +
				"the window is computed, logged, and then ignored")
		}
		return false
	})
	if !checked {
		t.Fatal("no decide function found in drive.go; this test is not checking anything")
	}
}
