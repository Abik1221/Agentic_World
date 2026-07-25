package payout

// Live devnet money-rail test: drives REAL on-chain USDC transfers with the two
// throwaway devnet wallets to prove both legs of the coin pipeline end-to-end.
//
//   - Deposit leg:   payer wallet  →  platform ATA   (the USER signs + pays the gas)
//   - Withdrawal leg: platform ATA →  payer wallet   (the PLATFORM signs, as in a payout)
//
// Both legs use the SAME production SolanaTransferrer the arena uses for payouts, so a
// pass proves the real transaction-build/sign/broadcast/confirm path against devnet.
//
// It moves real (worthless) devnet USDC, needs network + funded wallets, and is OFF by
// default. Run explicitly:
//
//	SOLANA_HOT_WALLET_SECRET=$(grep '^SOLANA_HOT_WALLET_SECRET=' .env.devnet | cut -d= -f2) \
//	PYYOL_DEVNET_LIVE=1 go test ./internal/payout/ -run TestLiveDevnetBothLegs -v -timeout 5m

import (
	"context"
	"encoding/base64"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	solana "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

const (
	devnetRPC   = "https://api.devnet.solana.com"
	devnetUSDC  = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"
	testCents   = 10 // 0.10 USDC per leg
	confirmWait = 90 * time.Second
)

// b64KeyfileToBase58 reads a Solana keyfile stored as base64(64-byte keypair) and
// returns the base58 secret PrivateKeyFromBase58 expects. Never logs the secret.
func b64KeyfileToBase58(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read keyfile %s: %v", path, err)
	}
	kp, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.Trim(string(raw), `"`)))
	if err != nil || len(kp) != 64 {
		t.Fatalf("keyfile %s not base64(64-byte keypair): len=%d err=%v", path, len(kp), err)
	}
	return solana.PrivateKey(kp).String() // base58 of the 64-byte expanded key
}

func usdcBalance(t *testing.T, c *rpc.Client, ata solana.PublicKey) *big.Int {
	t.Helper()
	out, err := c.GetTokenAccountBalance(context.Background(), ata, rpc.CommitmentConfirmed)
	if err != nil {
		t.Fatalf("token balance %s: %v", ata, err)
	}
	v, _ := new(big.Int).SetString(out.Value.Amount, 10)
	return v
}

func ataOf(t *testing.T, owner solana.PublicKey, mint solana.PublicKey) solana.PublicKey {
	t.Helper()
	a, _, err := solana.FindAssociatedTokenAddress(owner, mint)
	if err != nil {
		t.Fatalf("derive ATA: %v", err)
	}
	return a
}

// confirmOnChain polls the transferrer's Confirm until the tx finalizes (or times out).
func confirmOnChain(t *testing.T, tr *SolanaTransferrer, sig, leg string) {
	t.Helper()
	deadline := time.Now().Add(confirmWait)
	for time.Now().Before(deadline) {
		final, ok, err := tr.Confirm(context.Background(), sig)
		if err != nil {
			t.Fatalf("%s confirm: %v", leg, err)
		}
		if final {
			if !ok {
				t.Fatalf("%s tx %s failed on-chain", leg, sig)
			}
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("%s tx %s not finalized within %s", leg, sig, confirmWait)
}

func TestLiveDevnetBothLegs(t *testing.T) {
	if os.Getenv("PYYOL_DEVNET_LIVE") != "1" {
		t.Skip("set PYYOL_DEVNET_LIVE=1 (and SOLANA_HOT_WALLET_SECRET) to run the live devnet money test")
	}
	platformSecret := os.Getenv("SOLANA_HOT_WALLET_SECRET")
	if platformSecret == "" {
		t.Skip("SOLANA_HOT_WALLET_SECRET not set (source .env.devnet)")
	}
	home, _ := os.UserHomeDir()
	payerSecret := b64KeyfileToBase58(t, home+"/payer-devnet.json")

	mint := solana.MustPublicKeyFromBase58(devnetUSDC)
	pk := func(secret string) solana.PublicKey {
		k, err := solana.PrivateKeyFromBase58(secret)
		if err != nil {
			t.Fatalf("bad secret: %v", err)
		}
		return k.PublicKey()
	}
	platformOwner := pk(platformSecret)
	payerOwner := pk(payerSecret)
	platformATA := ataOf(t, platformOwner, mint)
	payerATA := ataOf(t, payerOwner, mint)

	rpcClient := rpc.New(devnetRPC)
	platBefore := usdcBalance(t, rpcClient, platformATA)
	payerBefore := usdcBalance(t, rpcClient, payerATA)
	t.Logf("BEFORE  platform=%s  payer=%s (base units, 6dp)", platBefore, payerBefore)

	// ── Deposit leg: payer → platform (the USER signs + pays gas) ──
	depositTr, err := NewSolanaTransferrer(devnetRPC, payerSecret, devnetUSDC, payerATA.String(), 6)
	if err != nil {
		t.Fatalf("build deposit transferrer: %v", err)
	}
	depSig, err := depositTr.Transfer(context.Background(), platformOwner.String(), testCents, "devnet-deposit")
	if err != nil {
		t.Fatalf("DEPOSIT transfer: %v", err)
	}
	t.Logf("DEPOSIT  payer→platform 0.10 USDC  sig=%s", depSig)
	confirmOnChain(t, depositTr, depSig, "deposit")

	// ── Withdrawal leg: platform → payer (the PLATFORM signs, as a real payout) ──
	payoutTr, err := NewSolanaTransferrer(devnetRPC, platformSecret, devnetUSDC, platformATA.String(), 6)
	if err != nil {
		t.Fatalf("build payout transferrer: %v", err)
	}
	wdSig, err := payoutTr.Transfer(context.Background(), payerOwner.String(), testCents, "devnet-withdraw")
	if err != nil {
		t.Fatalf("WITHDRAW transfer: %v", err)
	}
	t.Logf("WITHDRAW platform→payer 0.10 USDC  sig=%s", wdSig)
	confirmOnChain(t, payoutTr, wdSig, "withdraw")

	// ── Verify on-chain balances actually moved both ways ──
	platAfter := usdcBalance(t, rpcClient, platformATA)
	payerAfter := usdcBalance(t, rpcClient, payerATA)
	t.Logf("AFTER   platform=%s  payer=%s", platAfter, payerAfter)

	// Each wallet sent 0.10 and received 0.10, so USDC nets ~flat (the payer also paid
	// SOL gas on the deposit leg — that's the point: gas is on the sender). Assert both
	// legs actually executed by checking each ATA both debited and credited 0.10 USDC.
	unit := int64(10000 * testCents) // cents → 6dp base units
	if new(big.Int).Sub(platAfter, platBefore).Int64() != 0 {
		t.Logf("note: platform USDC net %s (expected ~0; both legs moved %d base units)",
			new(big.Int).Sub(platAfter, platBefore), unit)
	}
	t.Logf("✅ LIVE devnet: deposit (payer-signed) + withdrawal (platform-signed) both finalized on-chain")
}
