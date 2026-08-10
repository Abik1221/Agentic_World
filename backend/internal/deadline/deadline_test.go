package deadline

import (
	"testing"
	"time"
)

func gs() Policy { return DefaultPolicy("goofspiel") }

// samplesAt builds n samples all at ms, the simplest way to pin a percentile.
func samplesAt(n int, ms int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = ms
	}
	return out
}

// ── The property the whole design rests on ──────────────────────────────────

// A slow-but-honest agent must get room. This is the case a constant window handles
// badly: a local model or a reasoning model at 80s loses every round to a 45s window
// while being perfectly legitimate.
func TestASlowAgentEarnsALongerWindow(t *testing.T) {
	fast := For(gs(), samplesAt(20, 900))    // sub-second model
	slow := For(gs(), samplesAt(20, 80_000)) // 80s reasoning model
	if !(slow > fast) {
		t.Fatalf("slow agent window %v is not longer than the fast agent's %v", slow, fast)
	}
	if slow <= gs().Base {
		t.Fatalf("an 80s agent got %v, no more than the %v base — it loses every round it "+
			"was legitimately still thinking about", slow, gs().Base)
	}
}

// …but a fast agent must NOT have its window shrunk to its own p95. An agent that usually
// answers in 900ms will occasionally hit a genuinely hard turn, and its own good record
// must not become the thing that cuts it off.
func TestAFastAgentIsNeverPunishedByItsOwnRecord(t *testing.T) {
	got := For(gs(), samplesAt(50, 600))
	if got < gs().Base {
		t.Fatalf("a fast agent's window shrank to %v, below the %v base — one hard turn "+
			"would now be forfeited because it is usually quick", got, gs().Base)
	}
}

// A hung-but-responsive endpoint must never stall a table forever, however slow its
// history says it is.
func TestTheCeilingIsAbsolute(t *testing.T) {
	got := For(gs(), samplesAt(30, 10*60*1000)) // a 10-minute "agent"
	if got > gs().Ceiling {
		t.Fatalf("window %v exceeded the ceiling %v", got, gs().Ceiling)
	}
	if got != gs().Ceiling {
		t.Fatalf("window %v should have been clamped to exactly the ceiling %v", got, gs().Ceiling)
	}
}

// A new agent has no record, and a percentile over three decisions is noise. It must get
// the policy base rather than a window derived from nothing.
func TestSmallSamplesFallBackToTheBase(t *testing.T) {
	for n := 0; n < MinSamples; n++ {
		if got := For(gs(), samplesAt(n, 120_000)); got != gs().Base {
			t.Fatalf("with %d samples the window was %v, want the %v base — a percentile "+
				"over this little data is not evidence of anything", n, got, gs().Base)
		}
	}
	// One past the threshold the agent's own record starts counting.
	if got := For(gs(), samplesAt(MinSamples, 120_000)); got == gs().Base {
		t.Fatalf("at %d samples the window is still the base; the adaptive term never engages",
			MinSamples)
	}
}

// Determinism: a deadline that varies between runs cannot be explained to a developer who
// missed one by a second.
func TestWindowIsDeterministic(t *testing.T) {
	s := []int64{1200, 45_000, 900, 30_000, 2200, 61_000, 800, 15_000, 40_000, 3300}
	first := For(gs(), s)
	for i := 0; i < 25; i++ {
		if got := For(gs(), s); got != first {
			t.Fatalf("run %d produced %v, first run produced %v", i, got, first)
		}
	}
	// And it must not depend on the order samples arrive in.
	shuffled := []int64{61_000, 800, 40_000, 1200, 30_000, 3300, 45_000, 900, 15_000, 2200}
	if got := For(gs(), shuffled); got != first {
		t.Fatalf("reordering the same samples changed the window: %v vs %v", got, first)
	}
}

// ── The liveness gate ───────────────────────────────────────────────────────

// An agent confirmed alive at its deadline is genuinely still thinking, so it earns more
// time — that is what stops a legitimate slow model losing a round it was winning.
func TestAliveAgentEarnsExtensions(t *testing.T) {
	p := gs()
	granted := 0
	elapsed := p.Base
	for i := 0; i < 10; i++ {
		ext, ok := Extend(p, elapsed, granted)
		if !ok {
			break
		}
		granted++
		elapsed += ext
	}
	if granted == 0 {
		t.Fatal("a live agent was granted no extension at all")
	}
	if granted > p.MaxExtensions {
		t.Fatalf("granted %d extensions, more than the %d cap", granted, p.MaxExtensions)
	}
	if elapsed > p.Ceiling {
		t.Fatalf("extensions carried the total to %v, past the %v ceiling", elapsed, p.Ceiling)
	}
}

// The cap must actually bind. Without it a responsive-but-hung endpoint stalls the table
// forever, which is the failure the whole liveness gate exists to prevent.
func TestExtensionsAreBounded(t *testing.T) {
	p := gs()
	if _, ok := Extend(p, p.Base, p.MaxExtensions); ok {
		t.Fatal("an extension was granted past MaxExtensions")
	}
	if _, ok := Extend(p, p.Ceiling, 0); ok {
		t.Fatal("an extension was granted at the ceiling")
	}
	if _, ok := Extend(p, p.Ceiling+time.Minute, 0); ok {
		t.Fatal("an extension was granted past the ceiling")
	}
}

// The last extension must be trimmed to the ceiling rather than either overshooting it or
// being dropped entirely — a cliff at the end would silently discard usable seconds.
func TestFinalExtensionIsTrimmedToTheCeiling(t *testing.T) {
	p := gs()
	justUnder := p.Ceiling - 5*time.Second
	ext, ok := Extend(p, justUnder, 0)
	if !ok {
		t.Fatal("no extension granted 5s short of the ceiling")
	}
	if ext != 5*time.Second {
		t.Fatalf("granted %v, want exactly the 5s remaining to the ceiling", ext)
	}
}

// ── Per-game policy ─────────────────────────────────────────────────────────

// Mafia's night and voting phases were 30s, which is too tight for a reasoning model
// deciding who to kill — it was quietly converting real decisions into abstains, and an
// abstain is exactly what the absence machinery treats as a missed turn.
func TestMafiaWindowIsNoLongerTooTightForAReasoningModel(t *testing.T) {
	p := DefaultPolicy("mafia")
	if p.Base < 45*time.Second {
		t.Fatalf("mafia base is %v; a reasoning model routinely needs longer and would "+
			"abstain through phases it was actually deciding", p.Base)
	}
	// Still bounded: Mafia is concurrent, so a long phase taxes every seat at the table.
	if p.Ceiling > 3*time.Minute {
		t.Fatalf("mafia ceiling %v is too generous for a 12-seat concurrent phase", p.Ceiling)
	}
}

// Every shipped policy must be internally coherent, or a game silently gets a window that
// cannot be satisfied.
func TestEveryPolicyIsCoherent(t *testing.T) {
	for _, game := range []string{"goofspiel", "mafia", "monopoly", "unknown-game"} {
		p := DefaultPolicy(game)
		switch {
		case p.Floor > p.Base:
			t.Errorf("%s: floor %v above base %v", game, p.Floor, p.Base)
		case p.Base > p.Ceiling:
			t.Errorf("%s: base %v above ceiling %v", game, p.Base, p.Ceiling)
		case p.Headroom < 1:
			t.Errorf("%s: headroom %.2f would cut off the tail a percentile already excludes",
				game, p.Headroom)
		case p.MaxExtensions < 1:
			t.Errorf("%s: no extensions, so a live slow agent can never earn time", game)
		}
		// And it must produce a usable window for both a new and an established agent.
		if got := For(p, nil); got < p.Floor || got > p.Ceiling {
			t.Errorf("%s: new-agent window %v outside [%v, %v]", game, got, p.Floor, p.Ceiling)
		}
	}
}

// A zero policy must not produce a zero window — that would forfeit every turn instantly.
func TestZeroPolicyIsSafe(t *testing.T) {
	if got := For(Policy{}, nil); got <= 0 {
		t.Fatalf("zero policy produced a %v window; every decision would be forfeited on arrival", got)
	}
}
