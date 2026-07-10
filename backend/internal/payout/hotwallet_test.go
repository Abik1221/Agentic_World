package payout_test

import (
	"encoding/base64"
	"testing"

	"github.com/agent-arena/arena/internal/payout"
	"github.com/agent-arena/arena/internal/secretbox"
)

func TestResolveHotWalletSecret(t *testing.T) {
	const key = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM" // stand-in base58 secret
	const master = "a-strong-master-key-for-secretbox"

	// Plaintext passthrough (no encrypted form set).
	if got, err := payout.ResolveHotWalletSecret(key, "", ""); err != nil || got != key {
		t.Fatalf("plaintext = %q,%v; want %q,nil", got, err, key)
	}

	// Encrypted round-trip: seal with the master key, base64, then resolve.
	c, _ := secretbox.New(master)
	sealed, _ := c.Seal([]byte(key))
	encB64 := base64.StdEncoding.EncodeToString(sealed)
	if got, err := payout.ResolveHotWalletSecret("", encB64, master); err != nil || got != key {
		t.Fatalf("encrypted = %q,%v; want %q,nil", got, err, key)
	}

	// Encrypted form without a master key is rejected.
	if _, err := payout.ResolveHotWalletSecret("", encB64, ""); err == nil {
		t.Fatal("expected an error when SOLANA_HOT_WALLET_ENC_KEY is missing")
	}
	// Wrong master key fails to decrypt (no plaintext leak).
	if _, err := payout.ResolveHotWalletSecret("", encB64, "wrong-master-key"); err == nil {
		t.Fatal("expected decrypt failure with the wrong master key")
	}
}
