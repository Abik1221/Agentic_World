package match

import (
	"testing"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// The absence tally must come from the ENGINE's timeout, never from a driver playing the
// fallback card itself.
//
// This is the bug a live stress run found, and it silently disabled the whole absence
// rule. Both drivers computed the lowest legal card when a push failed and submitted it
// through the ordinary move path. The resulting board is identical — same card, same
// score — so nothing looked wrong. But State.Timeouts is incremented only by
// ForceTimeout, so it stayed at zero, seatWasAbsent always answered false, and the
// forfeit could never arm on the hosted-endpoint path that real agents actually use.
//
// Observed before the fix: a 13-round match with an agent dark from round 4 finished
// with timeouts [0,0].
func TestOnlyTheEngineTimeoutRecordsAbsence(t *testing.T) {
	e := gs.New(gs.Config{Rounds: 13})
	st, _ := e.Init(seedBytes())

	// What a driver used to do: work out the lowest card and seal it as a normal move.
	lowest := st.Hands[0][0]
	for _, c := range st.Hands[0] {
		if c < lowest {
			lowest = c
		}
	}
	viaAct, _, err := e.Seal(st, 0, lowest)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if viaAct.Timeouts[0] != 0 {
		t.Fatalf("a normal move recorded a timeout (%d); the tally would over-count real play",
			viaAct.Timeouts[0])
	}

	// What it must do instead.
	viaTimeout, _, err := e.ForceTimeout(st, 0)
	if err != nil {
		t.Fatalf("ForceTimeout: %v", err)
	}
	if viaTimeout.Timeouts[0] != 1 {
		t.Fatalf("the engine timeout recorded %d misses, want 1", viaTimeout.Timeouts[0])
	}

	// The two produce the SAME board — which is exactly why the bug was invisible. If a
	// future change makes the fallback card differ, that is a separate problem, but the
	// tally must remain the only thing that distinguishes them.
	if *viaAct.Sealed[0] != *viaTimeout.Sealed[0] {
		t.Fatalf("fallback card differs: act sealed %d, timeout sealed %d",
			*viaAct.Sealed[0], *viaTimeout.Sealed[0])
	}
	if viaAct.Timeouts == viaTimeout.Timeouts {
		t.Fatal("the two paths are indistinguishable in the tally, so absence can never be detected")
	}
}

// A seat that goes dark for most of a match must read as absent; one that misses a round
// here and there must not. This is the join between the engine tally and the settlement
// rule that consumes it.
func TestDarkSeatAccumulatesEnoughTimeoutsToCountAsAbsent(t *testing.T) {
	e := gs.New(gs.Config{Rounds: 13})
	st, _ := e.Init(seedBytes())

	rounds := 0
	for r := 0; r < 13 && !st.Finished; r++ {
		// Seat 0 is dark from round 4; seat 1 always answers.
		if r >= 3 {
			ns, _, err := e.ForceTimeout(st, 0)
			if err != nil {
				t.Fatalf("round %d ForceTimeout: %v", r, err)
			}
			st = ns
		} else {
			ns, _, err := e.Seal(st, 0, st.Hands[0][0])
			if err != nil {
				t.Fatalf("round %d Seal: %v", r, err)
			}
			st = ns
		}
		ns, _, err := e.Seal(st, 1, st.Hands[1][0])
		if err != nil {
			t.Fatalf("round %d opponent Seal: %v", r, err)
		}
		st = ns
		// Sealing does not advance the round — the service's commit calls Resolve. A test
		// that skips it just re-seals the same round forever.
		ns, _, err = e.Resolve(st)
		if err != nil {
			t.Fatalf("round %d Resolve: %v", r, err)
		}
		st = ns
		rounds++
	}

	if st.Timeouts[0] == 0 {
		t.Fatal("a seat dark for ten rounds recorded no timeouts at all")
	}
	if st.Timeouts[1] != 0 {
		t.Fatalf("the seat that answered every round recorded %d timeouts", st.Timeouts[1])
	}
	// And the settlement rule must agree that this is absence.
	if !seatWasAbsent(st.Timeouts[0], rounds) {
		t.Fatalf("seat 0 missed %d of %d rounds and is still not judged absent — the forfeit "+
			"cannot arm", st.Timeouts[0], rounds)
	}
	if seatWasAbsent(st.Timeouts[1], rounds) {
		t.Fatal("the seat that played every round was judged absent")
	}
}

func seedBytes() []byte {
	b := make([]byte, 32)
	copy(b, "drive-timeout-regression")
	return b
}
