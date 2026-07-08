package payout_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	solana "github.com/gagliardetto/solana-go"

	"github.com/agent-arena/arena/internal/payout"
)

// TestSolanaTransferrerDevnetSmoke proves the solana-go signing path end-to-end
// against live devnet: it builds, signs and submits a real USDC transfer from a
// freshly-generated (unfunded) hot wallet. The broadcast is EXPECTED to fail
// (no funds / no source account), but reaching the "broadcast" stage proves the
// transaction serialized + signed correctly — the parts fakes can't cover.
// Gated behind SOLANA_DEVNET_SMOKE=1 so normal `go test` stays offline.
func TestSolanaTransferrerDevnetSmoke(t *testing.T) {
	if os.Getenv("SOLANA_DEVNET_SMOKE") != "1" {
		t.Skip("set SOLANA_DEVNET_SMOKE=1 to run (requires devnet network)")
	}
	const devnetUSDC = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU" // devnet USDC mint
	hot := solana.NewWallet()
	mint := solana.MustPublicKeyFromBase58(devnetUSDC)
	srcATA, _, err := solana.FindAssociatedTokenAddress(hot.PublicKey(), mint)
	if err != nil {
		t.Fatalf("derive source ATA: %v", err)
	}
	xfer, err := payout.NewSolanaTransferrer("https://api.devnet.solana.com", hot.PrivateKey.String(), devnetUSDC, srcATA.String(), 6)
	if err != nil {
		t.Fatalf("new transferrer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A random valid destination wallet.
	dest := solana.NewWallet().PublicKey().String()
	ok, err := xfer.PayoutsEnabled(ctx, dest)
	if err != nil || !ok {
		t.Fatalf("PayoutsEnabled(%q) = %v, %v", dest, ok, err)
	}

	_, err = xfer.Transfer(ctx, dest, 100, "wd:smoke")
	if err == nil {
		t.Fatal("expected the unfunded broadcast to be rejected by devnet")
	}
	// The failure must be at the BROADCAST stage — anything earlier (build/sign)
	// would indicate a real serialization/signing bug.
	if !strings.Contains(err.Error(), "broadcast") {
		t.Fatalf("failed before broadcast (build/sign bug?): %v", err)
	}
	t.Logf("devnet rejected unfunded broadcast as expected: %v", err)
}
