package turnproof

import "testing"

// The number a proof is minted for MUST be the number the agent reports back.
//
// This is the defect these tests exist for, and it is invisible without them: the gateway
// verifies Verify(agent, match, round, proof) where `round` comes from the AGENT's
// X-Pyyol-Turn header. The SDK derives that from the view's `round` field first, falling
// back to `day`. Mint for one number, publish another, and every verification fails
// silently — proofs are produced, none count, and the integrity check stays permanently
// unarmed while looking installed.
//
// So the rule is: whatever a game publishes as `round`, it must mint for the same value.

// Several decisions happen inside one Mafia day, so the turn number has to distinguish
// them. If it did not, one proof would cover a whole day — and because a bound decision is
// recorded once per (match, agent, round), the day would also count as a single decision.
func TestMafiaTurnIsUniquePerDecisionWithinADay(t *testing.T) {
	day := 3
	seen := map[int]string{}
	for _, phase := range []string{"night", "reveal", "discussion", "voting"} {
		n := MafiaTurn(day, phase)
		if prev, clash := seen[n]; clash {
			t.Fatalf("phases %q and %q share turn %d — one proof would cover both", prev, phase, n)
		}
		seen[n] = phase
	}
}

// Turn numbers must increase with the day, so a later day can never collide with an
// earlier one. A collision would let a proof from day 1 satisfy a decision on day 2.
func TestMafiaTurnIncreasesWithTheDay(t *testing.T) {
	prev := -1
	for day := 0; day <= 20; day++ {
		for _, phase := range []string{"night", "reveal", "discussion", "voting", "trial", "defense", "verdict"} {
			n := MafiaTurn(day, phase)
			if n <= prev {
				t.Fatalf("day %d phase %q gave %d, not greater than the previous %d", day, phase, n, prev)
			}
			prev = n
		}
	}
}

// Deterministic: the arena mints at one moment and the gateway verifies at another, in a
// different process. A value that varied between the two would never verify.
func TestMafiaTurnIsDeterministic(t *testing.T) {
	for i := 0; i < 100; i++ {
		if MafiaTurn(7, "voting") != MafiaTurn(7, "voting") {
			t.Fatal("MafiaTurn is not deterministic")
		}
	}
}

// An unknown phase must get its OWN slot rather than silently sharing a known phase's
// number — a new engine phase should be unproven, never mis-attributed.
func TestUnknownPhaseDoesNotCollideWithAKnownOne(t *testing.T) {
	unknown := MafiaTurn(4, "some_new_phase_the_engine_added")
	for _, phase := range []string{"night", "reveal", "discussion", "voting", "trial", "defense", "verdict"} {
		if MafiaTurn(4, phase) == unknown {
			t.Fatalf("an unknown phase collided with %q at turn %d", phase, unknown)
		}
	}
	// And it must still sit inside its own day, not leak into the next one.
	if unknown >= MafiaTurn(5, "night") {
		t.Fatalf("an unknown phase on day 4 (%d) reached into day 5 (%d)", unknown, MafiaTurn(5, "night"))
	}
}

// Never negative, whatever the engine hands over.
func TestMafiaTurnNeverNegative(t *testing.T) {
	for _, day := range []int{-5, -1, 0, 1} {
		if n := MafiaTurn(day, "night"); n < 0 {
			t.Fatalf("day %d gave a negative turn %d", day, n)
		}
	}
}

// A minted proof verifies for the turn it was minted for, and for no other. This is the
// property the published `round` field protects: publish a different number and the
// verification below is what fails in production.
func TestAProofOnlyVerifiesForItsOwnTurn(t *testing.T) {
	s := New("a-test-secret-long-enough-to-sign-with")
	if s == nil {
		t.Skip("signer disabled without a secret")
	}
	const agent, match = "agt_1", "mch_1"
	turn := MafiaTurn(3, "voting")

	tok := s.Mint(agent, match, turn)
	if tok == "" {
		t.Fatal("no token minted")
	}
	if !s.Verify(agent, match, turn, tok) {
		t.Fatal("a freshly minted proof did not verify for its own turn")
	}
	// The exact failure the published round prevents: the agent reporting the bare day (3)
	// while the proof was minted for the phase-folded turn.
	if s.Verify(agent, match, 3, tok) {
		t.Fatal("a proof minted for a phase-folded turn verified against the bare day — " +
			"the two sides would silently disagree and every proof would still 'work'")
	}
	if s.Verify(agent, match, turn+1, tok) {
		t.Fatal("a proof verified for a different turn")
	}
	if s.Verify("agt_other", match, turn, tok) {
		t.Fatal("a proof verified for a different agent")
	}
	if s.Verify(agent, "mch_other", turn, tok) {
		t.Fatal("a proof verified for a different match")
	}
}
