package movesig_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	"github.com/agent-arena/arena/internal/movesig"
)

func keypair(t *testing.T) (pubB64 string, priv ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pub), priv
}

func sign(priv ed25519.PrivateKey, matchID string, round, seat, card int) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, movesig.Message(matchID, round, seat, card)))
}

func TestVerifyAcceptsAuthenticMove(t *testing.T) {
	pub, priv := keypair(t)
	sig := sign(priv, "m_1", 3, 0, 9)
	if !movesig.Verify(pub, "m_1", 3, 0, 9, sig) {
		t.Fatal("a correctly signed move must verify")
	}
}

func TestVerifyRejectsTamperAndReplay(t *testing.T) {
	pub, priv := keypair(t)
	sig := sign(priv, "m_1", 3, 0, 9)

	// A signature is bound to its exact slot + card — nothing else may reuse it.
	cases := []struct {
		name                      string
		match                     string
		round, seat, card         int
	}{
		{"different card", "m_1", 3, 0, 8},
		{"different round", "m_1", 4, 0, 9},
		{"different seat", "m_1", 3, 1, 9},
		{"different match", "m_2", 3, 0, 9},
	}
	for _, c := range cases {
		if movesig.Verify(pub, c.match, c.round, c.seat, c.card, sig) {
			t.Fatalf("%s: signature must NOT verify for a different slot/card", c.name)
		}
	}
}

func TestVerifyRejectsWrongKeyAndGarbage(t *testing.T) {
	_, priv := keypair(t)
	other, _ := keypair(t)
	sig := sign(priv, "m_1", 1, 0, 5)
	if movesig.Verify(other, "m_1", 1, 0, 5, sig) {
		t.Fatal("a different key must not verify")
	}
	if movesig.Verify("not-base64!!", "m_1", 1, 0, 5, sig) {
		t.Fatal("garbage pubkey must not verify")
	}
	if movesig.Verify(other, "m_1", 1, 0, 5, "not-base64!!") {
		t.Fatal("garbage signature must not verify")
	}
}

func TestValidPublicKey(t *testing.T) {
	pub, _ := keypair(t)
	if !movesig.ValidPublicKey(pub) {
		t.Fatal("a real Ed25519 pubkey should be valid")
	}
	if movesig.ValidPublicKey("short") {
		t.Fatal("a malformed key should be invalid")
	}
}

func signAction(priv ed25519.PrivateKey, domain, matchID string, seq, seat int, action string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, movesig.ActionMessage(domain, matchID, seq, seat, action)))
}

func TestVerifyActionRoundTrip(t *testing.T) {
	pub, priv := keypair(t)
	sig := signAction(priv, movesig.DomainMafia, "mf_1", 2, 1, "night|kill|3")
	if !movesig.VerifyAction(movesig.DomainMafia, pub, "mf_1", 2, 1, "night|kill|3", sig) {
		t.Fatal("authentic action move must verify")
	}
}

func TestVerifyActionRejectsTampering(t *testing.T) {
	pub, priv := keypair(t)
	sig := signAction(priv, movesig.DomainMonopoly, "mn_1", 7, 0, "buy|12|0|")
	// Each field is bound: any change must invalidate the signature.
	cases := []struct {
		name                 string
		domain, match, action string
		seq, seat            int
	}{
		{"wrong domain", movesig.DomainMafia, "mn_1", "buy|12|0|", 7, 0},
		{"wrong match", movesig.DomainMonopoly, "mn_2", "buy|12|0|", 7, 0},
		{"wrong seq", movesig.DomainMonopoly, "mn_1", "buy|12|0|", 8, 0},
		{"wrong seat", movesig.DomainMonopoly, "mn_1", "buy|12|0|", 7, 1},
		{"wrong action", movesig.DomainMonopoly, "mn_1", "buy|13|0|", 7, 0},
	}
	for _, c := range cases {
		if movesig.VerifyAction(c.domain, pub, c.match, c.seq, c.seat, c.action, sig) {
			t.Fatalf("%s: a tampered move must NOT verify", c.name)
		}
	}
	// A different keypair's signature must also fail (forgery).
	other, _ := keypair(t)
	if movesig.VerifyAction(movesig.DomainMonopoly, other, "mn_1", 7, 0, "buy|12|0|", sig) {
		t.Fatal("a forged (wrong-key) move must NOT verify")
	}
}
