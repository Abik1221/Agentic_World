package payout

import (
	"context"
	"errors"
	"fmt"

	solana "github.com/gagliardetto/solana-go"
	ataprog "github.com/gagliardetto/solana-go/programs/associated-token-account"
	token "github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
)

// SolanaTransferrer pays out USDC on Solana from the platform hot wallet. It
// implements both Transferrer (broadcast) and Confirmer (finality check). The
// hot-wallet secret is held in memory only; callers decrypt it (secretbox) before
// constructing this. NEVER log the secret.
//
// A payout builds one transaction with two instructions: create-idempotent the
// recipient's USDC associated-token-account (so first-time recipients work), then
// a checked USDC transfer from the platform ATA to it. The hot wallet is both fee
// payer and transfer authority.
type SolanaTransferrer struct {
	rpc        *rpc.Client
	hot        solana.PrivateKey
	hotPub     solana.PublicKey
	mint       solana.PublicKey
	sourceATA  solana.PublicKey
	decimals   uint8
	commitment rpc.CommitmentType
}

// NewSolanaTransferrer validates its inputs and builds the payout client.
// hotWalletSecret is a base58-encoded Solana private key (64-byte expanded form).
func NewSolanaTransferrer(rpcURL, hotWalletSecret, usdcMint, platformATA string, decimals uint8) (*SolanaTransferrer, error) {
	hot, err := solana.PrivateKeyFromBase58(hotWalletSecret)
	if err != nil {
		return nil, fmt.Errorf("payout: invalid solana hot wallet secret: %w", err)
	}
	mint, err := solana.PublicKeyFromBase58(usdcMint)
	if err != nil {
		return nil, fmt.Errorf("payout: invalid usdc mint: %w", err)
	}
	ata, err := solana.PublicKeyFromBase58(platformATA)
	if err != nil {
		return nil, fmt.Errorf("payout: invalid platform ata: %w", err)
	}
	if decimals < 2 {
		decimals = 6 // USDC
	}
	return &SolanaTransferrer{
		rpc: rpc.New(rpcURL), hot: hot, hotPub: hot.PublicKey(),
		mint: mint, sourceATA: ata, decimals: decimals, commitment: rpc.CommitmentFinalized,
	}, nil
}

// PayoutsEnabled reports whether the destination is a usable Solana wallet.
func (t *SolanaTransferrer) PayoutsEnabled(_ context.Context, destination string) (bool, error) {
	if destination == "" {
		return false, nil
	}
	_, err := solana.PublicKeyFromBase58(destination)
	return err == nil, nil
}

// centsToBase converts a cent amount to USDC base units (1 cent = 10^(decimals-2)).
func (t *SolanaTransferrer) centsToBase(cents int64) uint64 {
	scale := uint64(1)
	for i := 0; i < int(t.decimals)-2; i++ {
		scale *= 10
	}
	return uint64(cents) * scale
}

// Transfer builds, signs and broadcasts the USDC payout, returning the tx
// signature. idemKey is unused here (Solana sends aren't key-idempotent; the
// service's 'processing' claim prevents double-broadcast).
func (t *SolanaTransferrer) Transfer(ctx context.Context, destination string, amountCents int64, _ string) (string, error) {
	if amountCents <= 0 {
		return "", errors.New("payout: non-positive amount")
	}
	destWallet, err := solana.PublicKeyFromBase58(destination)
	if err != nil {
		return "", fmt.Errorf("payout: invalid destination wallet: %w", err)
	}
	destATA, _, err := solana.FindAssociatedTokenAddress(destWallet, t.mint)
	if err != nil {
		return "", fmt.Errorf("payout: derive destination ata: %w", err)
	}
	amount := t.centsToBase(amountCents)

	recent, err := t.rpc.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return "", fmt.Errorf("payout: latest blockhash: %w", err)
	}

	createATA := ataprog.NewCreateIdempotentInstruction(t.hotPub, destWallet, t.mint).Build()
	transfer := token.NewTransferCheckedInstruction(amount, t.decimals, t.sourceATA, t.mint, destATA, t.hotPub, nil).Build()

	tx, err := solana.NewTransaction(
		[]solana.Instruction{createATA, transfer},
		recent.Value.Blockhash,
		solana.TransactionPayer(t.hotPub),
	)
	if err != nil {
		return "", fmt.Errorf("payout: build tx: %w", err)
	}
	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(t.hotPub) {
			return &t.hot
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("payout: sign tx: %w", err)
	}
	sig, err := t.rpc.SendTransaction(ctx, tx)
	if err != nil {
		return "", fmt.Errorf("payout: broadcast: %w", err)
	}
	return sig.String(), nil
}

// Confirm checks a broadcast signature's terminal state. finalized=false while
// pending; finalized=true with success reflecting whether the tx succeeded.
func (t *SolanaTransferrer) Confirm(ctx context.Context, signature string) (bool, bool, error) {
	sig, err := solana.SignatureFromBase58(signature)
	if err != nil {
		return false, false, fmt.Errorf("payout: bad signature: %w", err)
	}
	res, err := t.rpc.GetSignatureStatuses(ctx, true, sig)
	if err != nil {
		return false, false, err
	}
	if res == nil || len(res.Value) == 0 || res.Value[0] == nil {
		return false, false, nil // not yet visible
	}
	st := res.Value[0]
	if st.ConfirmationStatus != rpc.ConfirmationStatusFinalized {
		return false, false, nil // still confirming
	}
	return true, st.Err == nil, nil
}

// ensure the compile-time contract.
var (
	_ Transferrer = (*SolanaTransferrer)(nil)
	_ Confirmer   = (*SolanaTransferrer)(nil)
)
