package auth

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testSecret = "a-test-signing-key-long-enough-to-be-realistic"

func TestNonceRoundTrip(t *testing.T) {
	n := NewNonceIssuer(testSecret)
	if !n.Enabled() {
		t.Fatal("issuer with a secret reports disabled")
	}
	got, err := n.Issue()
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := n.Verify(got); err != nil {
		t.Fatalf("Verify(freshly issued) = %v, want nil", err)
	}
}

// Every nonce must be distinct, or it is not a nonce — a fixed value would let one captured
// (nonce, token) pair be replayed forever.
func TestNoncesAreUnique(t *testing.T) {
	n := NewNonceIssuer(testSecret)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		v, err := n.Issue()
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if seen[v] {
			t.Fatalf("Issue returned a duplicate on iteration %d", i)
		}
		seen[v] = true
	}
}

// The MAC is the whole mechanism: a nonce an attacker can mint is no protection at all.
func TestForgedNonceIsRejected(t *testing.T) {
	n := NewNonceIssuer(testSecret)
	good, err := n.Issue()
	if err != nil {
		t.Fatal(err)
	}
	i := strings.LastIndexByte(good, '.')
	body := good[:i]

	for name, bad := range map[string]string{
		"empty":           "",
		"no separator":    "justastring",
		"body only":       body,
		"unsigned":        body + ".",
		"wrong signature": body + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"not base64":      body + ".!!!!",
		"tampered body":   strings.Replace(body, body[:4], "zzzz", 1) + good[i:],
		"another secret":  mustIssue(t, NewNonceIssuer("a-completely-different-signing-key-value")),
	} {
		if err := n.Verify(bad); err == nil {
			t.Errorf("Verify(%s) accepted a nonce it should have rejected", name)
		}
	}
}

// Extending the expiry must not be possible: it travels in the clear so it can be read
// without a lookup, but it is inside the MAC so it cannot be edited.
func TestExpiryCannotBeExtended(t *testing.T) {
	n := NewNonceIssuer(testSecret)
	good, err := n.Issue()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(good, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected nonce shape %q", good)
	}
	far := time.Now().Add(100 * time.Hour).Unix()
	tampered := parts[0] + "." + itoa64(far) + "." + parts[2]
	if err := n.Verify(tampered); err == nil {
		t.Error("a nonce with a rewritten expiry was accepted")
	}
}

// An expired nonce is rejected — that bound is the point of the mechanism.
func TestExpiredNonceIsRejected(t *testing.T) {
	n := NewNonceIssuer(testSecret)
	// Mint one directly with a past expiry, signed correctly, so this tests the expiry
	// check and not the MAC.
	body := "AAAAAAAAAAAAAAAAAAAAAA." + itoa64(time.Now().Add(-time.Second).Unix())
	expired := body + "." + b64(n.mac(body))
	if err := n.Verify(expired); err == nil {
		t.Error("an expired nonce was accepted")
	} else if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired nonce rejected for the wrong reason: %v", err)
	}
}

// No secret ⇒ disabled, and it must refuse to issue rather than mint something unverifiable.
func TestDisabledIssuerRefusesBothSides(t *testing.T) {
	n := NewNonceIssuer("")
	if n.Enabled() {
		t.Error("issuer with no secret reports enabled")
	}
	if _, err := n.Issue(); err == nil {
		t.Error("a disabled issuer minted a nonce")
	}
	if err := n.Verify("anything"); err == nil {
		t.Error("a disabled issuer verified a nonce")
	}
}

// Enforcement is opt-in so the rollout does not reject every sign-in before the client ships
// nonces. Both switches must agree.
func TestVerifierNonceEnforcementIsOptIn(t *testing.T) {
	v := NewGoogleVerifier("client-id.apps.googleusercontent.com")
	if v.NonceRequired() {
		t.Error("nonce enforcement is on before RequireNonce was called")
	}
	v.RequireNonce(NewNonceIssuer(testSecret))
	if !v.NonceRequired() {
		t.Error("RequireNonce did not turn enforcement on")
	}
	// A disabled issuer must not silently turn enforcement on and reject everything.
	v2 := NewGoogleVerifier("client-id.apps.googleusercontent.com")
	v2.RequireNonce(NewNonceIssuer(""))
	if v2.NonceRequired() {
		t.Error("an issuer with no secret enabled enforcement, which would reject every sign-in")
	}
}

func mustIssue(t *testing.T, n *NonceIssuer) string {
	t.Helper()
	v, err := n.Issue()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
