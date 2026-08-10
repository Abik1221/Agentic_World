package match

import (
	"context"
	"testing"
)

// The arena's absence rule, stated once as a test.
//
// An agent that starts a staked match and then goes dark must LOSE it — not void it.
// Voiding was the shipped behaviour and it got the incentives exactly backwards:
//
//   - the absent agent got its stake back, so walking away was free;
//   - the opponent, who showed up and paid for real inference, had its win cancelled.
//
// The integrity void exists to stop a scripted agent taking money off developers who
// pay for models. A seat that never answered is not that: it proved nothing for the
// obvious reason. So absence is exempt from the gate, the match settles, the winner is
// paid, and the absent seat forfeits its stake.
func TestAbsentSeatForfeitsInsteadOfVoidingTheMatch(t *testing.T) {
	const rounds = 13
	s := &Service{}
	// The opponent proved every decision; the absent seat proved none — which is what
	// zero-proof looks like whether the agent cheated or simply never replied.
	s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_present": 13, "ag_absent": 0}}, 0)
	m := matchWith("ag_present", "ag_absent")

	// Seat 1 was played by the platform for 12 of 13 rounds.
	failed, agent := s.rankedIntegrityFailed(context.Background(), m, rounds, [2]int{0, 12})
	if failed {
		t.Fatalf("the match was VOIDED over an absent seat (%q). The winner's stake is refunded "+
			"to the agent that walked away, and the agent that showed up and paid for inference "+
			"loses its win", agent)
	}
}

// The exemption must be narrow. A seat that ANSWERED every turn and still proved nothing
// is the case the void was built for, and it must keep working — otherwise "absence"
// becomes a blanket amnesty for scripted agents.
func TestPresentButUnprovenSeatStillVoids(t *testing.T) {
	s := &Service{}
	s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_llm": 13, "ag_script": 0}}, 0)
	m := matchWith("ag_llm", "ag_script")

	// No timeouts anywhere: the script was present for every single round.
	failed, agent := s.rankedIntegrityFailed(context.Background(), m, 13, [2]int{0, 0})
	if !failed {
		t.Fatal("a seat that answered every round and proved nothing was allowed to settle — " +
			"this is the scripted-agent case the gate exists to catch")
	}
	if agent != "ag_script" {
		t.Fatalf("blamed %q, want ag_script", agent)
	}
}

// One bad round is not absence. A model that times out occasionally is normal, and
// stripping integrity protection off the whole match for it would let a scripted agent
// buy amnesty by deliberately missing a single turn.
func TestOccasionalTimeoutIsNotAbsence(t *testing.T) {
	s := &Service{}
	s.SetIntegrityCheck(fakeIntegrity{bound: map[string]int{"ag_llm": 13, "ag_script": 0}}, 0)
	m := matchWith("ag_llm", "ag_script")

	failed, _ := s.rankedIntegrityFailed(context.Background(), m, 13, [2]int{0, 3})
	if !failed {
		t.Fatal("3 timeouts out of 13 counted as absence; a scripted agent could buy the " +
			"exemption by skipping a couple of turns on purpose")
	}
}

// The threshold itself, pinned directly so the boundary cannot drift unnoticed.
func TestSeatWasAbsentBoundary(t *testing.T) {
	cases := []struct {
		name            string
		timeouts, asked int
		want            bool
	}{
		{"never asked", 0, 0, false},
		{"present throughout", 0, 13, false},
		{"one miss", 1, 13, false},
		{"exactly half is NOT absent", 6, 12, false},
		{"just over half", 7, 12, true},
		{"gone entirely", 13, 13, true},
		// A short match must not be judged more harshly than a long one.
		{"one of two", 1, 2, false},
		{"two of two", 2, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := seatWasAbsent(tc.timeouts, tc.asked); got != tc.want {
				t.Fatalf("seatWasAbsent(%d, %d)=%v want %v", tc.timeouts, tc.asked, got, tc.want)
			}
		})
	}
}
