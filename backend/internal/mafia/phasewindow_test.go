package mafia

import (
	"testing"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
)

// Mafia's pacing is per phase on purpose: night is short (roles act in secret and in
// parallel), discussion is the long one (it IS the game), voting is tighter (the
// arguing is over). A single flat window either rushes the debate or leaves the table
// asleep.
//
// Wiring PhaseWindow from the global MoveWindow silently flattened all five to the
// Goofspiel budget — a 75s discussion ran for 20s, and twelve agents each needing one
// LLM call could not all speak in time. Nothing failed; the game just got worse.
func TestUnsetPhaseWindowKeepsTheEnginesPerPhaseClock(t *testing.T) {
	s := &Service{} // PhaseWindow zero, as the default config now supplies

	for _, phase := range []string{mf.PhaseNight, mf.PhaseMorning, mf.PhaseDiscussion, mf.PhaseVoting} {
		if got, want := s.phaseWindow(phase), mf.PhaseDuration(phase); got != want {
			t.Fatalf("phase %q window = %v, want the engine's %v", phase, got, want)
		}
	}

	// And the phases must genuinely differ — equal values would mean the per-phase
	// clock had been flattened somewhere upstream.
	if s.phaseWindow(mf.PhaseDiscussion) <= s.phaseWindow(mf.PhaseVoting) {
		t.Fatal("discussion should be longer than voting")
	}
	if s.phaseWindow(mf.PhaseMorning) >= s.phaseWindow(mf.PhaseNight) {
		t.Fatal("morning is an announcement beat and should be shorter than night")
	}
}

// An operator override is still honoured — it just has to be deliberate now.
func TestAnExplicitPhaseWindowStillOverridesEveryPhase(t *testing.T) {
	s := &Service{cfg: Config{PhaseWindow: 90 * time.Second}}
	for _, phase := range []string{mf.PhaseNight, mf.PhaseDiscussion, mf.PhaseVoting} {
		if got := s.phaseWindow(phase); got != 90*time.Second {
			t.Fatalf("explicit override ignored for %q: %v", phase, got)
		}
	}
}
