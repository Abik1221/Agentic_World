package mafia

import (
	"testing"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
)

// House seats must not all answer in the same instant.
//
// Every filler seat used to act in one pass of the drive loop, so twelve players spoke and
// voted together, in seat order, inside a single tick. That is the thing that gives a table
// away — a human reads simultaneity as machinery long before they notice anything about the
// moves themselves.
//
// Pinned on the CLOCK rather than through a live table: what matters is that the delays are
// spread, bounded, and reproducible, and driving a real match to assert that would be slow
// and would test the scheduler instead of the rule.

func TestSeatsDoNotAllThinkForTheSameTime(t *testing.T) {
	// The regression. Twelve seats deciding in the same phase must not share a delay.
	seen := map[time.Duration]int{}
	for seat := 1; seat <= 12; seat++ {
		seen[thinkFor("mf_spread", phaseKey{seat: seat, day: 1, phase: mf.PhaseVoting})]++
	}
	if len(seen) < 8 {
		t.Errorf("12 seats produced only %d distinct think times: %v", len(seen), seen)
	}
}

func TestThinkTimeIsReproducible(t *testing.T) {
	// A replay has to pace like the match it replays, and a test timing a phase must be
	// able to predict it. math/rand here would make the driver a source of flakiness.
	k := phaseKey{seat: 4, day: 2, phase: mf.PhaseDiscussion}
	first := thinkFor("mf_stable", k)
	for i := 0; i < 20; i++ {
		if got := thinkFor("mf_stable", k); got != first {
			t.Fatalf("same seat and phase gave %s then %s", first, got)
		}
	}
}

func TestDifferentTablesDoNotShareARhythm(t *testing.T) {
	// Two matches running at once must not pause in lockstep — that would be the same
	// tell one table's worth of simultaneity gives, spread across the platform.
	same := 0
	for seat := 1; seat <= 12; seat++ {
		k := phaseKey{seat: seat, day: 1, phase: mf.PhaseVoting}
		if thinkFor("mf_one", k) == thinkFor("mf_two", k) {
			same++
		}
	}
	if same > 2 {
		t.Errorf("%d of 12 seats paced identically across two matches", same)
	}
}

func TestAPauseNeverOutlastsItsPhase(t *testing.T) {
	// The pause has to make a bot look like it is thinking and never like it timed out.
	// Every phase window is measured in tens of seconds; these stay in single digits.
	const ceiling = 8 * time.Second
	for _, phase := range []string{mf.PhaseNight, mf.PhaseDiscussion, mf.PhaseVoting, "morning"} {
		for seat := 1; seat <= 12; seat++ {
			for day := 1; day <= 5; day++ {
				d := thinkFor("mf_bounds", phaseKey{seat: seat, day: day, phase: phase})
				if d <= 0 || d > ceiling {
					t.Fatalf("%s seat %d day %d: think time %s is outside (0, %s]", phase, seat, day, d, ceiling)
				}
			}
		}
	}
}

func TestALaterPhaseIsANewDecision(t *testing.T) {
	// The same seat asked again on a later day is thinking about something else, so it
	// draws its own pause rather than reusing the one it already spent.
	a := thinkFor("mf_days", phaseKey{seat: 3, day: 1, phase: mf.PhaseVoting})
	b := thinkFor("mf_days", phaseKey{seat: 3, day: 2, phase: mf.PhaseVoting})
	if a == b {
		t.Errorf("day 1 and day 2 both paced %s — the key is not separating them", a)
	}
}
