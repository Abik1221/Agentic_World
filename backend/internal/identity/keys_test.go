package identity

import "testing"

func TestKeyRoundTrip(t *testing.T) {
	const pepper = "test-pepper"
	k, err := generateKey(pepper)
	if err != nil {
		t.Fatalf("generateKey: %v", err)
	}

	prefix, secret, err := splitKey(k.Raw)
	if err != nil {
		t.Fatalf("splitKey: %v", err)
	}
	if prefix != k.Prefix {
		t.Fatalf("prefix mismatch: got %q want %q", prefix, k.Prefix)
	}
	if !verifySecret(k.Hash, secret, pepper) {
		t.Fatal("verifySecret failed for correct secret")
	}
	if verifySecret(k.Hash, secret, "wrong-pepper") {
		t.Fatal("verifySecret succeeded with wrong pepper")
	}
	if verifySecret(k.Hash, "not-the-secret", pepper) {
		t.Fatal("verifySecret succeeded with wrong secret")
	}
}

func TestSplitKey_Malformed(t *testing.T) {
	for _, bad := range []string{"", "nope", "sk_arena_", "sk_arena_onlylookup", "bearer x"} {
		if _, _, err := splitKey(bad); err == nil {
			t.Errorf("splitKey(%q) = nil error, want error", bad)
		}
	}
}

func TestLimitsValidate(t *testing.T) {
	if err := DefaultLimits().Validate(); err != nil {
		t.Fatalf("DefaultLimits invalid: %v", err)
	}
	bad := DefaultLimits()
	bad.MaxBid = bad.CoinLimitPerMatch + 1
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error when max_bid > coin_limit_per_match")
	}
}
