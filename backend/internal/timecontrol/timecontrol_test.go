package timecontrol

import (
	"testing"
	"time"
)

// TestPaddingCannotBuyAnAdvantage pins the property that makes a budget a budget.
//
// NOTE ON WHAT THIS DOES AND DOES NOT PROVE. It does not close a live exploit — see the
// package doc: internal/deadline's window is already shared across the seats of a match, so
// there is no within-match asymmetry to remove. What this pins is that a DECLARED budget
// behaves like one: time spent is time gone, so the number in the spec is the number an
// agent actually gets, and an entrant cannot quietly enlarge its own allowance.
//
// That property is what lets a time control be published as part of a reproducible method.
// If it ever stops holding — because Window stopped depending on the remaining budget, say —
// the budget is decorative and the spec would be describing something the code does not do.
func TestPaddingCannotBuyAnAdvantage(t *testing.T) {
	c := DefaultControl("goofspiel") // Budget 10m, PerMove 3m, Increment 5s
	padder, err := NewClock(c)
	if err != nil {
		t.Fatal(err)
	}
	honest, err := NewClock(c)
	if err != nil {
		t.Fatal(err)
	}

	// Seven warm-up decisions: the padder burns 90s each, the honest seat answers in 3s.
	// Seven, not eight: at eight the padder is bankrupt and both windows would be pinned to
	// trivial values, which would let a broken Window() pass. Seven leaves the padder alive
	// but squeezed, which is the state that actually discriminates.
	for i := 0; i < 7; i++ {
		padder.Spend(c, 90*time.Second)
		honest.Spend(c, 3*time.Second)
	}

	if padder.Remaining() >= honest.Remaining() {
		t.Fatalf("padding left the padder with %v and the honest seat with %v; "+
			"padding must COST time, not buy it", padder.Remaining(), honest.Remaining())
	}

	// The discriminating assertion. The padder is still in the match, but its next window is
	// now bounded by what it has LEFT, while the honest seat still gets the full per-move
	// cap. A Window() that ignored the remaining budget would hand both seats PerMove here
	// and this comparison would fail to bite — which is precisely how an earlier version of
	// this test passed against a deliberately broken implementation.
	pw, hw := padder.Window(c), honest.Window(c)
	if pw >= hw {
		t.Fatalf("after padding, padder window %v >= honest window %v; the window must be "+
			"bounded by the seat's own remaining budget", pw, hw)
	}
	if hw != c.PerMove {
		t.Fatalf("honest seat window %v, want the full per-move cap %v", hw, c.PerMove)
	}
	if pw > padder.Remaining() {
		t.Fatalf("padder window %v exceeds its remaining budget %v", pw, padder.Remaining())
	}
}

// TestBudgetIsIdenticalRegardlessOfHistory. The control has no per-agent field and no
// dependence on demonstrated latency; a seat cannot inflate anything because there is
// nothing to inflate.
func TestBudgetIsIdenticalRegardlessOfHistory(t *testing.T) {
	c := DefaultControl("goofspiel")
	for _, name := range []string{"fast", "slow", "erratic"} {
		k, err := NewClock(c)
		if err != nil {
			t.Fatal(err)
		}
		if k.Remaining() != c.Budget {
			t.Fatalf("%s: started with %v, want %v", name, k.Remaining(), c.Budget)
		}
	}
}

// TestOverrunIsChargedInFull. An overrun is real wall-clock the table waited. Forgiving it
// would restore a smaller version of the exploit.
func TestOverrunIsChargedInFull(t *testing.T) {
	c := Control{Budget: time.Minute, PerMove: 10 * time.Second, Grace: time.Second}
	k, _ := NewClock(c)
	inTime := k.Spend(c, 30*time.Second) // window was 10s + 1s grace
	if inTime {
		t.Fatal("a 30s decision against an 11s limit was reported in time")
	}
	if k.Spent() != 30*time.Second {
		t.Fatalf("charged %v, want the full 30s", k.Spent())
	}
	if k.Remaining() != 30*time.Second {
		t.Fatalf("remaining %v, want 30s", k.Remaining())
	}
}

// TestIncrementIsNotPaidForLateness. Crediting an overrun would pay an agent for being late.
func TestIncrementIsNotPaidForLateness(t *testing.T) {
	c := Control{Budget: time.Minute, PerMove: 10 * time.Second, Increment: 5 * time.Second}
	late, _ := NewClock(c)
	late.Spend(c, 20*time.Second)
	if got, want := late.Remaining(), 40*time.Second; got != want {
		t.Fatalf("late seat has %v, want %v (no increment)", got, want)
	}
	onTime, _ := NewClock(c)
	onTime.Spend(c, 4*time.Second)
	if got, want := onTime.Remaining(), 61*time.Second; got == want {
		t.Fatalf("increment pushed remaining above Budget: %v", got)
	}
	if onTime.Remaining() != c.Budget {
		t.Fatalf("on-time seat has %v, want the budget ceiling %v", onTime.Remaining(), c.Budget)
	}
}

// TestIncrementCannotAccumulateAboveBudget. Otherwise a seat that always answers instantly
// banks time against one that thinks — a slow drift back toward the original unfairness.
func TestIncrementCannotAccumulateAboveBudget(t *testing.T) {
	c := Control{Budget: time.Minute, PerMove: 10 * time.Second, Increment: 5 * time.Second}
	k, _ := NewClock(c)
	for i := 0; i < 50; i++ {
		k.Spend(c, time.Millisecond)
	}
	if k.Remaining() > c.Budget {
		t.Fatalf("remaining %v exceeded budget %v after 50 fast moves", k.Remaining(), c.Budget)
	}
}

// TestExhaustedClockReturnsZeroWindow. The caller must read 0 as "fall back now"; a dead
// seat cannot stall the table past its own budget.
func TestExhaustedClockReturnsZeroWindow(t *testing.T) {
	c := Control{Budget: 10 * time.Second, PerMove: 10 * time.Second}
	k, _ := NewClock(c)
	k.Spend(c, 10*time.Second)
	if !k.Expired() {
		t.Fatal("clock should be expired")
	}
	if w := k.Window(c); w != 0 {
		t.Fatalf("expired clock returned window %v, want 0", w)
	}
}

// TestPerMoveCapBindsBeforeBudget. One seat must not be able to spend its whole budget on a
// single turn while a 12-seat table waits.
func TestPerMoveCapBindsBeforeBudget(t *testing.T) {
	c := DefaultControl("mafia")
	k, _ := NewClock(c)
	if w := k.Window(c); w != c.PerMove {
		t.Fatalf("first window %v, want the per-move cap %v", w, c.PerMove)
	}
}

// TestGraceIsNotSpendableTime. Grace absorbs jitter the agent did not cause. If it appeared
// in Window the agent could plan to spend it, which would make it thinking time by another
// name and reopen the asymmetry.
func TestGraceIsNotSpendableTime(t *testing.T) {
	c := Control{Budget: time.Minute, PerMove: 10 * time.Second, Grace: 3 * time.Second}
	k, _ := NewClock(c)
	if w := k.Window(c); w != 10*time.Second {
		t.Fatalf("Window returned %v; grace must not be advertised as spendable", w)
	}
	askedAt := time.Unix(0, 0)
	if got, want := k.Deadline(c, askedAt), askedAt.Add(13*time.Second); !got.Equal(want) {
		t.Fatalf("Deadline %v, want %v (window + grace)", got, want)
	}
}

func TestValidateRejectsBadControls(t *testing.T) {
	cases := map[string]Control{
		"zero budget":       {Budget: 0, PerMove: time.Second},
		"zero per-move":     {Budget: time.Minute, PerMove: 0},
		"negative incr":     {Budget: time.Minute, PerMove: time.Second, Increment: -1},
		"negative grace":    {Budget: time.Minute, PerMove: time.Second, Grace: -1},
		"per-move > budget": {Budget: time.Second, PerMove: time.Minute},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := c.Validate(); err == nil {
				t.Fatal("want error, got nil")
			}
			if _, err := NewClock(c); err == nil {
				t.Fatal("NewClock accepted an invalid control")
			}
		})
	}
}

// TestShippedControlsAreValid. A default that fails its own validation would surface as a
// broken match rather than a config error.
func TestShippedControlsAreValid(t *testing.T) {
	for _, g := range []string{"goofspiel", "mafia", "monopoly", "unknown-game"} {
		if err := DefaultControl(g).Validate(); err != nil {
			t.Errorf("DefaultControl(%q) invalid: %v", g, err)
		}
	}
}

// TestMonopolyIsFlaggedForCalibration. Its budget is a placeholder, not a measurement, and a
// certified result must not be published from a guessed time control.
func TestMonopolyIsFlaggedForCalibration(t *testing.T) {
	if !NeedsCalibration("monopoly") {
		t.Fatal("monopoly's placeholder budget must be flagged")
	}
	for _, g := range []string{"goofspiel", "mafia"} {
		if NeedsCalibration(g) {
			t.Errorf("%s is derived from decisions x base and should not be flagged", g)
		}
	}
}

// TestDeterminism. Same inputs, same arithmetic, forever.
func TestDeterminism(t *testing.T) {
	c := DefaultControl("goofspiel")
	run := func() (time.Duration, int) {
		k, _ := NewClock(c)
		for _, d := range []time.Duration{time.Second, 45 * time.Second, 200 * time.Second, 0} {
			k.Spend(c, d)
		}
		return k.Remaining(), k.Decisions()
	}
	r0, d0 := run()
	for i := 0; i < 25; i++ {
		if r, d := run(); r != r0 || d != d0 {
			t.Fatalf("run %d gave (%v,%d), want (%v,%d)", i, r, d, r0, d0)
		}
	}
}
