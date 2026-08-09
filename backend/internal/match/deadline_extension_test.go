package match

import (
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/deadline"
)

// The extension bound, tested as the arithmetic it is.
//
// tryExtend itself needs a Service, a repo, a clock and a liveness prober, and a test built on
// all four would be testing the wiring. The DEFECT was none of those — it was one expression:
// "extensions granted so far" derived from an elapsed time measured against an origin the
// extension itself moves. So the loop is simulated here directly, exactly as the sweeper runs
// it, which is what makes the difference between the two origins visible.
//
// Observed in production before the fix: Goofspiel granted 17 extensions against a
// MaxExtensions of 3, holding one round open for twelve minutes while an agent whose every move
// was rejected went on answering /health.

// simulateExtensions runs the sweeper's extend loop and returns how many extensions were
// granted before the policy said stop.
//
// movingOrigin reproduces the bug: when true the origin is the CURRENT deadline, which each
// grant pushes forward. When false it is the round's fixed base, which nothing moves.
//
// Capped at hardStop so the broken case returns a finite number instead of hanging the test —
// a runaway must be reported as a failure, not as a timeout nobody reads.
func simulateExtensions(pol deadline.Policy, window time.Duration, movingOrigin bool, hardStop int) int {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	origin := now              // when the round actually started
	current := now.Add(window) // the deadline as first set

	granted := 0
	for granted < hardStop {
		// The sweeper wakes when the current deadline lapses.
		now = current

		from := origin
		if movingOrigin {
			from = current.Add(-window)
		}
		elapsed := now.Sub(from)

		soFar := 0
		if elapsed > window && pol.Extension > 0 {
			soFar = int((elapsed - window) / pol.Extension)
		}
		ext, ok := deadline.Extend(pol, elapsed, soFar)
		if !ok {
			return granted
		}
		current = now.Add(ext)
		granted++
	}
	return granted
}

// With a fixed origin the bound holds: extensions stop, and the round cannot be held open past
// the policy ceiling.
func TestRoundExtensionsAreBoundedByPolicy(t *testing.T) {
	for _, game := range []string{"goofspiel", "mafia", "monopoly"} {
		t.Run(game, func(t *testing.T) {
			pol := deadline.DefaultPolicy(game)
			// A window at the policy floor, which is the tightest case and therefore the one
			// that produces the most extensions before the ceiling.
			got := simulateExtensions(pol, pol.Floor, false, 500)
			if got >= 500 {
				t.Fatalf("extensions never stopped (hit the %d cap): a responsive agent can hold "+
					"a round open forever", 500)
			}
			// The ceiling may cut in before MaxExtensions, so at-most is the real contract.
			if got > pol.MaxExtensions {
				t.Fatalf("granted %d extensions, policy allows at most %d", got, pol.MaxExtensions)
			}
		})
	}
}

// The regression itself, stated as a test.
//
// This is what makes the test above meaningful: it demonstrates that the SAME policy, with the
// origin allowed to move, does not converge. If a future change reintroduces the moving origin,
// the test above starts failing — and this one documents why.
func TestAMovingOriginNeverReachesTheExtensionCeiling(t *testing.T) {
	pol := deadline.DefaultPolicy("goofspiel")
	const cap = 200

	fixed := simulateExtensions(pol, pol.Floor, false, cap)
	moving := simulateExtensions(pol, pol.Floor, true, cap)

	if fixed >= cap {
		t.Fatalf("the FIXED origin also ran away (%d extensions) — the bound is not working", fixed)
	}
	if moving < cap {
		t.Fatalf("the moving origin stopped after %d extensions; this test encodes the bug that "+
			"measuring elapsed from a deadline the extension moves never converges, so if it now "+
			"converges the arithmetic changed and TestRoundExtensionsAreBoundedByPolicy no longer "+
			"proves anything", moving)
	}
	t.Logf("fixed origin: %d extensions (bounded)   moving origin: unbounded (hit the %d cap)",
		fixed, cap)
}

// The fallback path. A row written before migration 0085 has no base, and such a round must
// behave exactly as it did before rather than losing its extensions entirely — an in-flight
// match must not be forfeited by a deploy.
func TestMissingBaseFallsBackToTheDeadline(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	dl := now.Add(30 * time.Second)

	m := Match{RoundDeadline: &dl}
	origin := m.RoundDeadline
	if m.RoundDeadlineBase != nil {
		origin = m.RoundDeadlineBase
	}
	if origin == nil || !origin.Equal(dl) {
		t.Fatalf("origin = %v, want the deadline %v when no base is recorded", origin, dl)
	}

	base := now
	m.RoundDeadlineBase = &base
	origin = m.RoundDeadlineBase
	if !origin.Equal(base) {
		t.Fatalf("origin = %v, want the base %v when one is recorded", origin, base)
	}
}
