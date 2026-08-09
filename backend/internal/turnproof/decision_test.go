package turnproof

import "testing"

const (
	dAgent  = "ag_1"
	dMatch  = "m_1"
	dRound  = 4
	dHash   = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	dMove   = "card:7"
	dSecret = "s3cret"
)

func TestDecisionReceiptVerifiesOnlyItsOwnValues(t *testing.T) {
	s := New(dSecret)
	receipt := s.MintDecision(dAgent, dMatch, dRound, dHash, dMove)
	if receipt == "" {
		t.Fatal("no receipt minted with a secret configured")
	}
	if !s.VerifyDecision(dAgent, dMatch, dRound, dHash, dMove, receipt) {
		t.Fatal("a freshly minted receipt does not verify")
	}

	// Every field is load-bearing. If any one of these tampered variants verified, the
	// corresponding substitution would go undetected — and the MOVE case is the whole point.
	for name, check := range map[string]bool{
		"different agent":      s.VerifyDecision("ag_2", dMatch, dRound, dHash, dMove, receipt),
		"different match":      s.VerifyDecision(dAgent, "m_2", dRound, dHash, dMove, receipt),
		"different round":      s.VerifyDecision(dAgent, dMatch, dRound+1, dHash, dMove, receipt),
		"different completion": s.VerifyDecision(dAgent, dMatch, dRound, dHash[:63]+"f", dMove, receipt),
		"SUBSTITUTED MOVE":     s.VerifyDecision(dAgent, dMatch, dRound, dHash, "card:3", receipt),
	} {
		if check {
			t.Fatalf("a receipt verified with a %s — that field is not actually bound", name)
		}
	}
}

// Domain separation between the two artefacts.
//
// Both are HMACs under one secret, so without distinct domain tags a v1 TURN TOKEN for
// (agent, match, round) would be a valid RECEIPT for the empty completion and empty move —
// which is precisely the substitution completion binding exists to stop. The agent already
// holds a turn token, so this is not a theoretical confusion.
func TestATurnTokenIsNotAValidDecisionReceipt(t *testing.T) {
	s := New(dSecret)
	token := s.Mint(dAgent, dMatch, dRound)
	if s.VerifyDecision(dAgent, dMatch, dRound, "", "", token) {
		t.Fatal("a turn token verified as a decision receipt over an empty move")
	}
	if s.VerifyDecision(dAgent, dMatch, dRound, dHash, dMove, token) {
		t.Fatal("a turn token verified as a decision receipt")
	}
	// And the reverse, so neither can stand in for the other.
	receipt := s.MintDecision(dAgent, dMatch, dRound, dHash, dMove)
	if s.Verify(dAgent, dMatch, dRound, receipt) {
		t.Fatal("a decision receipt verified as a turn token")
	}
}

// Field separation. Without an unambiguous separator, ("a","bc") and ("ab","c") could render
// to one message and a receipt for one decision would verify for another.
func TestDecisionFieldsCannotBeShiftedAcrossTheSeparator(t *testing.T) {
	s := New(dSecret)
	receipt := s.MintDecision("ag", "1m", dRound, dHash, dMove)
	if s.VerifyDecision("ag1", "m", dRound, dHash, dMove, receipt) {
		t.Fatal("ids shifted across the separator still verified")
	}
	// The move sits last, so a move containing the separator must not be able to absorb it.
	r2 := s.MintDecision(dAgent, dMatch, dRound, dHash, "card:7")
	if s.VerifyDecision(dAgent, dMatch, dRound, dHash+"|card:7", "", r2) {
		t.Fatal("a hash that swallowed the move boundary still verified")
	}
}

// A disabled signer must offer no receipt at all, rather than a constant something could
// mistake for one. The same failure mode Mint already guards.
func TestDisabledSignerMintsAndVerifiesNoReceipt(t *testing.T) {
	s := New("")
	if got := s.MintDecision(dAgent, dMatch, dRound, dHash, dMove); got != "" {
		t.Fatalf("disabled signer minted %q", got)
	}
	if s.VerifyDecision(dAgent, dMatch, dRound, dHash, dMove, "anything") {
		t.Fatal("disabled signer verified a receipt")
	}
	// Critically, it must not accept the empty receipt either — a disabled deployment would
	// otherwise treat every unattested decision as attested.
	if s.VerifyDecision(dAgent, dMatch, dRound, dHash, dMove, "") {
		t.Fatal("disabled signer verified an EMPTY receipt")
	}
}

func TestDecisionReceiptDiffersAcrossSecrets(t *testing.T) {
	a := New("secret-a").MintDecision(dAgent, dMatch, dRound, dHash, dMove)
	b := New("secret-b").MintDecision(dAgent, dMatch, dRound, dHash, dMove)
	if a == b {
		t.Fatal("two different secrets produced the same receipt")
	}
	if New("secret-b").VerifyDecision(dAgent, dMatch, dRound, dHash, dMove, a) {
		t.Fatal("a receipt minted under one secret verified under another")
	}
}
