package turnproof

import "testing"

func TestRoundTrip(t *testing.T) {
	s := New("platform-secret")
	tok := s.Mint("ag_1", "m_1", 3)
	if tok == "" {
		t.Fatal("no token minted")
	}
	if !s.Verify("ag_1", "m_1", 3, tok) {
		t.Fatal("the platform's own token did not verify")
	}
}

// The whole point: a token is bound to ONE decision. Replaying round 1's token on a
// later turn must not make that turn look LLM-backed — otherwise an agent makes one
// cheap call and coasts.
func TestTokenDoesNotVerifyForAnotherRound(t *testing.T) {
	s := New("platform-secret")
	tok := s.Mint("ag_1", "m_1", 1)
	for _, r := range []int{0, 2, 3, 13} {
		if s.Verify("ag_1", "m_1", r, tok) {
			t.Fatalf("round 1's token verified for round %d", r)
		}
	}
}

// And bound to one agent and one match, so a token cannot be shared between
// colluding agents or reused across a developer's own tables.
func TestTokenIsBoundToAgentAndMatch(t *testing.T) {
	s := New("platform-secret")
	tok := s.Mint("ag_1", "m_1", 5)
	if s.Verify("ag_2", "m_1", 5, tok) {
		t.Fatal("another agent's turn accepted this token")
	}
	if s.Verify("ag_1", "m_2", 5, tok) {
		t.Fatal("another match accepted this token")
	}
}

// Field separation must be unambiguous. Without a separator that cannot appear in an
// id, ("ab","c") and ("a","bc") produce the same message and one match's token
// verifies for another.
func TestFieldsCannotBeConfused(t *testing.T) {
	s := New("platform-secret")
	if s.Verify("a", "bc", 1, s.Mint("ab", "c", 1)) {
		t.Fatal("agent/match boundary is ambiguous — ids can be shifted across it")
	}
}

// An agent must not be able to mint its own proof. Different secret => no.
func TestATokenFromAnotherSecretIsRejected(t *testing.T) {
	real := New("platform-secret")
	forger := New("guessed-secret")
	if real.Verify("ag_1", "m_1", 1, forger.Mint("ag_1", "m_1", 1)) {
		t.Fatal("a token minted with a different secret verified")
	}
}

// Disabled must FAIL CLOSED. If a deployment has no secret, it has no proof to offer
// — it must not treat every call as valid, which would silently disable the control
// it exists to enforce.
func TestDisabledSignerOffersNoProof(t *testing.T) {
	s := New("")
	if s.Enabled() {
		t.Fatal("empty secret should be disabled")
	}
	if s.Mint("ag_1", "m_1", 1) != "" {
		t.Fatal("disabled signer minted a token")
	}
	if s.Verify("ag_1", "m_1", 1, "anything") {
		t.Fatal("disabled signer verified a token — this must fail closed")
	}
	// And an empty token is never valid, even on an enabled signer.
	if New("s").Verify("ag_1", "m_1", 1, "") {
		t.Fatal("empty token verified")
	}
}
