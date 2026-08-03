package payout

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/agent-arena/arena/internal/secretbox"
	solana "github.com/gagliardetto/solana-go"
	ataprog "github.com/gagliardetto/solana-go/programs/associated-token-account"
	token "github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
)

// ResolveHotWalletSecret returns the base58 hot-wallet private key to sign payouts
// with. When encB64 is set it is base64(secretbox ciphertext) decrypted at boot
// with masterKey (AES-256-GCM), so the key is never stored in plaintext; otherwise
// the plaintext value is used (dev/local). Produce encB64 with cmd/wallet-secret-encrypt.
func ResolveHotWalletSecret(plaintext, encB64, masterKey string) (string, error) {
	if encB64 == "" {
		return plaintext, nil
	}
	if masterKey == "" {
		return "", errors.New("payout: SOLANA_HOT_WALLET_ENC_KEY is required when SOLANA_HOT_WALLET_SECRET_ENC is set")
	}
	ct, err := base64.StdEncoding.DecodeString(encB64)
	if err != nil {
		return "", fmt.Errorf("payout: SOLANA_HOT_WALLET_SECRET_ENC is not valid base64: %w", err)
	}
	c, err := secretbox.New(masterKey)
	if err != nil {
		return "", err
	}
	pt, err := c.Open(ct)
	if err != nil {
		return "", fmt.Errorf("payout: cannot decrypt SOLANA_HOT_WALLET_SECRET_ENC (wrong SOLANA_HOT_WALLET_ENC_KEY?): %w", err)
	}
	return string(pt), nil
}

// BroadcastAmbiguousError is returned when the send RPC fails AFTER the payout
// transaction was signed. The transaction's signature is fixed at signing time and
// the transaction MAY already have been accepted by the network, so the caller must
// NOT release escrow on this error (that would double-pay if it landed). The
// signature lets the confirm watcher settle the true outcome from the chain.
type BroadcastAmbiguousError struct{ Signature string }

func (e *BroadcastAmbiguousError) Error() string {
	return "payout: solana broadcast ambiguous (tx may be in flight): " + e.Signature
}

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

// TokenAccountIdentity is the mint + owning authority of an SPL token account. Mirrors
// blockchain.TokenAccountInfo structurally so the RPC client satisfies the reader below
// through a thin adapter, without payout depending on that package.
//
// A zero value means "that address is not an SPL token account at all", which the
// adapter must distinguish from an RPC failure — see VerifyRails.
type TokenAccountIdentity struct {
	Mint  string
	Owner string
}

// TokenAccountReader reads a token account's identity.
type TokenAccountReader interface {
	TokenAccount(ctx context.Context, tokenAccount string) (TokenAccountIdentity, error)
}

// VerifyRails checks, once at startup, that this signer can actually spend from the
// account it has been pointed at.
//
// The deposit side already proves that the account payers are told to fund is the
// account we watch. Nothing proved the equivalent for the way OUT, and the failure has
// a different shape: a TransferChecked whose authority does not own the source account
// is rejected by the token program, so every cash-out fails at broadcast. That is loud
// per-withdrawal and silent at deploy time — and a mainnet cutover is exactly when it
// happens, because it pairs a fresh hot-wallet key with a fresh ATA. Get either from
// the wrong deployment and payouts are dead on arrival.
//
// Reports rather than refuses, matching the deposit rails check: a transient RPC failure
// at boot must not take the payout rail down, which would be worse than the
// misconfiguration being looked for. ok is false only when the RPC answered clearly and
// the answer was wrong.
func (t *SolanaTransferrer) VerifyRails(ctx context.Context, chain TokenAccountReader) (ok bool, detail string) {
	info, err := chain.TokenAccount(ctx, t.sourceATA.String())
	if err != nil {
		return true, "unverified: " + err.Error()
	}
	if info.Mint == "" && info.Owner == "" {
		return false, fmt.Sprintf("payout source %s is not an SPL token account", t.sourceATA)
	}
	if info.Mint != t.mint.String() {
		return false, fmt.Sprintf(
			"payout source %s holds mint %s but payouts are denominated in %s — cash-outs would move the wrong asset or fail outright",
			t.sourceATA, info.Mint, t.mint)
	}
	if info.Owner != t.hotPub.String() {
		return false, fmt.Sprintf(
			"payout source %s is owned by %s but the hot wallet is %s — the signer has no authority over it, so every cash-out will fail to broadcast",
			t.sourceATA, info.Owner, t.hotPub)
	}
	return true, "ok"
}

// HotPublicKey returns the signing wallet's public key (base58). Safe to log: it is a
// public address, and it is the one thing about the hot wallet an operator needs in
// order to fund it with SOL or look it up on an explorer.
func (t *SolanaTransferrer) HotPublicKey() string { return t.hotPub.String() }

// SourceATA returns the token account payouts are signed from (base58).
func (t *SolanaTransferrer) SourceATA() string { return t.sourceATA.String() }

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
func (t *SolanaTransferrer) Transfer(ctx context.Context, destination string, amountCents int64, idemKey string) (string, error) {
	return t.TransferPreCommit(ctx, destination, amountCents, idemKey, nil)
}

// TransferPreCommit builds+signs the USDC payout, invokes onSigned with the
// deterministic signature BEFORE broadcasting, and only then sends it. The hook
// lets the caller durably record the signature (and move the withdrawal to
// 'broadcasted') while the transaction is still un-sent — so a crash or failure at
// or after the send never leaves an un-recorded in-flight payout. If onSigned
// returns an error, the transaction is NOT broadcast (nothing goes out). onSigned
// may be nil (plain Transfer). (M10)
func (t *SolanaTransferrer) TransferPreCommit(ctx context.Context, destination string, amountCents int64, _ string, onSigned func(signature string) error) (string, error) {
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
	// The signature is fixed at signing time. Durably record it BEFORE broadcasting
	// so an un-sent-but-signed tx can never become an un-tracked in-flight payout: if
	// this hook fails we abort without sending; if it succeeds the caller has already
	// persisted the signature and can resolve the payout from the chain no matter what
	// happens to the send below. (M10)
	if onSigned != nil && len(tx.Signatures) > 0 && !tx.Signatures[0].IsZero() {
		if err := onSigned(tx.Signatures[0].String()); err != nil {
			return "", fmt.Errorf("payout: pre-broadcast record failed (not sent): %w", err)
		}
	}
	sig, err := t.rpc.SendTransaction(ctx, tx)
	if err != nil {
		// The tx is already signed, so its signature is fixed and it may have reached
		// the network despite this RPC error (timeout, transient 5xx, dropped
		// response). Surface the deterministic signature so the caller records
		// 'broadcasted' and confirms on-chain rather than releasing escrow blindly.
		if len(tx.Signatures) > 0 && !tx.Signatures[0].IsZero() {
			return "", &BroadcastAmbiguousError{Signature: tx.Signatures[0].String()}
		}
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
