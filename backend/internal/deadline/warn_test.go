package deadline

import (
	"testing"
	"time"
)

// The warning exists so an agent (and a watching human) learns the turn is nearly over
// while there is still time to act. Every test here pins a way it could fail to do that.

func TestWarnLeadScalesWithTheWindow(t *testing.T) {
	// The whole reason this is a fraction. A fixed lead cannot serve both ends of a range
	// that runs from a 10s floor to a 3m ceiling.
	short := WarnLead(20 * time.Second)
	long := WarnLead(2 * time.Minute)
	if !(short < long) {
		t.Fatalf("lead did not grow with the window: 20s→%v, 2m→%v", short, long)
	}
}

func TestWarnLeadIsBoundedAtBothEnds(t *testing.T) {
	for _, w := range []time.Duration{
		6 * time.Second, 10 * time.Second, 45 * time.Second,
		time.Minute, 2 * time.Minute, 3 * time.Minute, time.Hour,
	} {
		lead := WarnLead(w)
		if lead == 0 {
			continue // explicitly "do not warn"; covered separately
		}
		if lead < MinWarnLead {
			t.Errorf("window %v: lead %v is under MinWarnLead %v — too late to act on", w, lead, MinWarnLead)
		}
		if lead > MaxWarnLead {
			t.Errorf("window %v: lead %v is over MaxWarnLead %v — 'nearly up' with a third left", w, lead, MaxWarnLead)
		}
	}
}

func TestWarnNeverLandsInTheFirstHalfOfTheTurn(t *testing.T) {
	// A warning that arrives before halfway reads as the deadline itself, and an agent
	// that believes it has to answer then throws away half of a window it was granted.
	for _, w := range []time.Duration{6 * time.Second, 8 * time.Second, 10 * time.Second, 30 * time.Second} {
		lead := WarnLead(w)
		if lead > w/2 {
			t.Errorf("window %v: lead %v is more than half the window", w, lead)
		}
	}
}

func TestNoWarningOnAWindowTooShortToUseOne(t *testing.T) {
	// Below 2×MinWarnLead the notice and the deadline are indistinguishable, and it costs
	// a round trip the agent can least afford on its tightest turn.
	for _, w := range []time.Duration{0, time.Second, 3 * time.Second, 5*time.Second + 999*time.Millisecond} {
		if lead := WarnLead(w); lead != 0 {
			t.Errorf("window %v: got lead %v, want 0 (skip)", w, lead)
		}
	}
	// And the boundary is inclusive on the useful side.
	if lead := WarnLead(2 * MinWarnLead); lead == 0 {
		t.Errorf("window %v: warning suppressed at exactly 2×MinWarnLead", 2*MinWarnLead)
	}
}

func TestEveryShippedPolicyBaseGetsAWarning(t *testing.T) {
	// A live check against the real policies rather than invented numbers: if a base is
	// ever lowered below the useful threshold, this fails instead of silently going quiet.
	for _, game := range []string{"goofspiel", "mafia", "monopoly"} {
		p := DefaultPolicy(game)
		if lead := WarnLead(p.Base); lead <= 0 {
			t.Errorf("%s: base window %v produces no warning", game, p.Base)
		}
		// The floor is the tightest window the game can hand out. It is allowed to be too
		// short to warn on, but if it warns, the lead must still be usable.
		if lead := WarnLead(p.Floor); lead != 0 && lead < MinWarnLead {
			t.Errorf("%s: floor window %v produces an unusable lead %v", game, p.Floor, lead)
		}
	}
}

func TestWarnAtIsDerivedFromTheSameStartAndWindowAsTheDeadline(t *testing.T) {
	// Taking its own clock reading is how a warning ends up landing AFTER the deadline it
	// is warning about.
	start := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	window := 60 * time.Second
	deadline := start.Add(window)

	at, ok := WarnAt(start, window)
	if !ok {
		t.Fatal("no warning for a 60s window")
	}
	if !at.Before(deadline) {
		t.Errorf("warning at %v is not before the deadline %v", at, deadline)
	}
	if got, want := deadline.Sub(at), WarnLead(window); got != want {
		t.Errorf("lead from WarnAt = %v, WarnLead says %v — the two disagree", got, want)
	}
}

func TestWarnAtReportsNoWarningRatherThanTheZeroTime(t *testing.T) {
	// The bug this pins: a caller that ignores ok and uses the zero Time would fire the
	// warning immediately, turning the quietest case into the loudest one.
	at, ok := WarnAt(time.Now(), 2*time.Second)
	if ok {
		t.Fatalf("got a warning for a 2s window at %v", at)
	}
	if !at.IsZero() {
		t.Errorf("expected the zero Time alongside ok=false, got %v", at)
	}
}

func TestWarnLeadIsDeterministic(t *testing.T) {
	// Same reasoning as For(): a warning that varies run to run cannot be explained to a
	// developer who missed one.
	for i := 0; i < 100; i++ {
		if got, want := WarnLead(47*time.Second), WarnLead(47*time.Second); got != want {
			t.Fatalf("WarnLead not deterministic: %v vs %v", got, want)
		}
	}
}
