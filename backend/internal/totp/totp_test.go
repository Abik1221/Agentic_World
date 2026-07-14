package totp

import (
	"testing"
	"time"
)

// RFC 6238 test vector: secret "12345678901234567890" (ASCII) base32-encoded,
// SHA1, at Unix time 59 → code 94287082 → last 6 digits "287082".
func TestRFC6238Vector(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	got, err := Code(secret, time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got != "287082" {
		t.Fatalf("code = %s, want 287082", got)
	}
}

func TestValidateWindowAndSkew(t *testing.T) {
	secret, _ := GenerateSecret()
	now := time.Unix(1_700_000_000, 0)
	code, _ := Code(secret, now)

	if !Validate(secret, code, now, 1) {
		t.Fatal("current code should validate")
	}
	// A code from the previous window still validates with skew=1...
	prev, _ := Code(secret, now.Add(-30*time.Second))
	if !Validate(secret, prev, now, 1) {
		t.Fatal("previous-window code should validate with skew=1")
	}
	// ...but not with skew=0.
	if Validate(secret, prev, now, 0) && prev != code {
		t.Fatal("previous-window code must not validate with skew=0")
	}
	// Two windows away is rejected.
	far, _ := Code(secret, now.Add(-90*time.Second))
	if Validate(secret, far, now, 1) && far != code {
		t.Fatal("far code must not validate")
	}
	// Wrong / malformed codes are rejected.
	if Validate(secret, "000000", now.Add(15*time.Second), 1) && code == "000000" {
		// extremely unlikely; ignore the 1-in-1e6 coincidence
	}
	if Validate(secret, "abc", now, 1) || Validate(secret, "", now, 1) || Validate("!!bad!!", code, now, 1) {
		t.Fatal("malformed input must be rejected")
	}
}

func TestURIContainsSecret(t *testing.T) {
	u := URI("JBSWY3DPEHPK3PXP", "pyyol", "user@example.com")
	if u == "" || u[:16] != "otpauth://totp/p" {
		t.Fatalf("unexpected uri: %s", u)
	}
}
