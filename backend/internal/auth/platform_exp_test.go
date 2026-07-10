package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// mintPlatformToken builds a signed Platform token with the given claims (test helper).
func mintPlatformToken(t *testing.T, priv ed25519.PrivateKey, c platformClaims) string {
	t.Helper()
	payload, _ := json.Marshal(c)
	sig := ed25519.Sign(priv, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// TestPlatformTokenRequiresExpiry guards M1: a token with no exp used to be
// accepted forever; now it (and an over-long-lived one) must be rejected.
func TestPlatformTokenRequiresExpiry(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := &PlatformVerifier{pub: pub}
	now := time.Now().Unix()

	cases := []struct {
		name  string
		claim platformClaims
		ok    bool
	}{
		{"no exp is rejected", platformClaims{Iss: "super-admin", Sub: "u1", Iat: now}, false},
		{"expired is rejected", platformClaims{Iss: "super-admin", Sub: "u1", Iat: now - 7200, Exp: now - 3600}, false},
		{"over-long lifetime rejected", platformClaims{Iss: "super-admin", Sub: "u1", Iat: now, Exp: now + int64((48 * time.Hour).Seconds())}, false},
		// SEC-L1: no iat previously skipped the max-age cap; now it's required so a
		// token can't dodge the lifetime bound by omitting iat.
		{"no iat is rejected", platformClaims{Iss: "super-admin", Sub: "u1", Exp: now + 600}, false},
		{"valid short-lived is accepted", platformClaims{Iss: "super-admin", Sub: "u1", Iat: now, Exp: now + 600}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := v.Verify(mintPlatformToken(t, priv, tc.claim))
			if tc.ok && (err != nil || p == nil) {
				t.Fatalf("expected accept, got p=%v err=%v", p, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected reject, got accepted")
			}
		})
	}
}
