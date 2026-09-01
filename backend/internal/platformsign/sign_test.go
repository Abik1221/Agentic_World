package platformsign

import "testing"

// Cross-repo known-answer vector. Ed25519 is deterministic, so signing a fixed
// message with a fixed seed yields a fixed signature. The Super Admin's
// configbus package pins the SAME constants, guaranteeing the two independent
// implementations are byte-compatible on the wire.
const (
	katSeedB64 = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="
	katPubB64  = "ebVWLo/mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ="
	katSigB64  = "mpEEop+Aw2mYnYMJue69h5Ql8AwRHXvR8KDeOk4KXNcOGacZf+QzajK+6wEpJeTilL8NCqr5EIttLm0pgXKdAQ=="
)

// katMsg equals store.eventSigningInput("evt_kat_1","match.finished",`{"a":1}`,"1720099200000").
var katMsg = []byte("evt_kat_1\nmatch.finished\n{\"a\":1}\n1720099200000")

func TestKnownAnswerVector(t *testing.T) {
	s, err := NewSigner(katSeedB64)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Sign(katMsg); got != katSigB64 {
		t.Fatalf("signature mismatch:\n got  %s\n want %s", got, katSigB64)
	}
	v, err := NewVerifier(katPubB64)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Verify(katMsg, katSigB64) {
		t.Fatal("verifier rejected the known-answer signature")
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	seed, pub, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := NewSigner(seed)
	v, _ := NewVerifier(pub)

	msg := []byte("hello platform bus")
	sig := s.Sign(msg)
	if !v.Verify(msg, sig) {
		t.Fatal("valid signature rejected")
	}
	if v.Verify([]byte("tampered"), sig) {
		t.Fatal("accepted signature over different data")
	}
	if v.Verify(msg, "not-base64!!") {
		t.Fatal("accepted a malformed signature")
	}
}

func TestWrongKeyRejected(t *testing.T) {
	seedA, _, _ := GenerateKeypair()
	_, pubB, _ := GenerateKeypair()
	s, _ := NewSigner(seedA)
	v, _ := NewVerifier(pubB) // mismatched public key

	if v.Verify([]byte("x"), s.Sign([]byte("x"))) {
		t.Fatal("signature verified under the wrong public key")
	}
}

func TestDisabledSignerVerifier(t *testing.T) {
	var s *Signer   // nil => disabled
	var v *Verifier // nil => disabled
	if s.Enabled() || v.Enabled() {
		t.Fatal("nil signer/verifier should report disabled")
	}
	if s.Sign([]byte("x")) != "" {
		t.Fatal("disabled signer should return empty signature")
	}
	if !v.Verify([]byte("x"), "anything") {
		t.Fatal("disabled verifier should accept everything (dev mode)")
	}

	// Constructed from empty strings => also disabled, no error.
	s2, err := NewSigner("")
	if err != nil || s2.Enabled() {
		t.Fatalf("empty seed should yield disabled signer, got err=%v enabled=%v", err, s2.Enabled())
	}
	v2, err := NewVerifier("")
	if err != nil || v2.Enabled() {
		t.Fatalf("empty pub should yield disabled verifier, got err=%v enabled=%v", err, v2.Enabled())
	}
}

func TestBadKeyMaterial(t *testing.T) {
	if _, err := NewSigner("not-base64!!"); err == nil {
		t.Fatal("expected error for non-base64 seed")
	}
	if _, err := NewSigner("YWJj"); err == nil { // "abc" -> 3 bytes, not 32
		t.Fatal("expected error for wrong-length seed")
	}
	if _, err := NewVerifier("YWJj"); err == nil {
		t.Fatal("expected error for wrong-length public key")
	}
}

// TestPublicFromSeedMatchesGenerateKeypair. A derived public key that disagreed with the
// generated one would produce signatures that verify nowhere, and the failure would be found
// by a reader of a published bundle rather than by us.
func TestPublicFromSeedMatchesGenerateKeypair(t *testing.T) {
	seed, pub, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	got, err := PublicFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	if got != pub {
		t.Fatalf("derived %q, generated %q", got, pub)
	}
	// And it must actually verify a real signature.
	s, err := NewSigner(seed)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(got)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("certificate bundle")
	if !v.Verify(msg, s.Sign(msg)) {
		t.Fatal("a signature did not verify under the derived public key")
	}
	if _, err := PublicFromSeed("not-base64!"); err == nil {
		t.Fatal("garbage seed accepted")
	}
	if _, err := PublicFromSeed("c2hvcnQ="); err == nil {
		t.Fatal("short seed accepted")
	}
}
