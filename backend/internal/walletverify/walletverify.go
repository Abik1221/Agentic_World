// Package walletverify proves a user controls the Solana wallet they want paid
// out to. The server issues a random nonce; the user signs the challenge message
// with the wallet's key (in the browser wallet); the server verifies the Ed25519
// signature against the wallet address (a base58 Ed25519 public key) and records
// the wallet as verified. Payouts then require a proven wallet (see payout).
package walletverify

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform"
	solana "github.com/gagliardetto/solana-go"
)

// challengeHeader is the fixed, human-readable first line the wallet signs, so a
// user can see what they're approving and it can't be confused with another app's
// message.
const challengeHeader = "pyyol wallet verification"

// challengeMessage builds the EXACT message the wallet signs. It binds the acting
// user and an explicit expiry into the signature (not just the nonce), so a signed
// challenge can't be replayed for a different user and is self-describing about when
// it lapses (defense in depth / phishing resistance). Both StartChallenge and Verify
// derive the message through this one function so the strings can never drift. (L7)
func challengeMessage(userPublicID, nonce string, expiresAt time.Time) string {
	return challengeHeader + "\n" +
		"user: " + userPublicID + "\n" +
		"nonce: " + nonce + "\n" +
		"expires: " + expiresAt.UTC().Format(time.RFC3339)
}

var (
	ErrNoChallenge        = httpx.NewError(http.StatusBadRequest, "no_challenge", "Request a verification challenge first.")
	ErrChallengeExpired   = httpx.NewError(http.StatusBadRequest, "challenge_expired", "The verification challenge expired; request a new one.")
	ErrBadWalletSignature = httpx.NewError(http.StatusBadRequest, "bad_wallet_signature", "The signature does not prove control of this wallet.")
)

func errInvalid(msg string) error {
	return httpx.NewError(http.StatusBadRequest, "invalid_request", msg)
}

// Challenge is a pending wallet-ownership challenge.
type Challenge struct {
	WalletAddress string
	Nonce         string
	ExpiresAt     time.Time
}

// Repo persists challenges + the verified wallet on the user.
type Repo interface {
	SaveChallenge(ctx context.Context, userPublicID, walletAddress, nonce string, expiresAt time.Time) error
	GetChallenge(ctx context.Context, userPublicID string) (Challenge, bool, error)
	MarkVerified(ctx context.Context, userPublicID, walletAddress string, at time.Time) error
	ClearChallenge(ctx context.Context, userPublicID string) error
	// ClearVerified removes the user's verified wallet AND the payout destination
	// hint, returning them to the "no wallet on file" state.
	ClearVerified(ctx context.Context, userPublicID string) error
}

// Service issues + verifies wallet-ownership challenges.
type Service struct {
	repo  Repo
	clock platform.Clock
	ttl   time.Duration
	// connected records which wallet the browser connected (display hints only).
	// Optional — see connected.go.
	connected ConnectedRepo
}

func New(repo Repo, clock platform.Clock) *Service {
	return &Service{repo: repo, clock: clock, ttl: 10 * time.Minute}
}

// StartChallenge validates the wallet address, stores a fresh random nonce for the
// user, and returns the exact message the wallet must sign.
func (s *Service) StartChallenge(ctx context.Context, userPublicID, walletAddress string) (message, nonce string, err error) {
	walletAddress = strings.TrimSpace(walletAddress)
	if _, e := solana.PublicKeyFromBase58(walletAddress); e != nil {
		return "", "", errInvalid("invalid solana wallet address")
	}
	raw := make([]byte, 24)
	if _, e := rand.Read(raw); e != nil {
		return "", "", e
	}
	nonce = hex.EncodeToString(raw)
	// Truncate to whole seconds so the stored expiry round-trips through the DB to the
	// exact same RFC3339 string Verify reconstructs (sub-second precision would drift).
	expiresAt := s.clock.Now().Add(s.ttl).Truncate(time.Second)
	if err := s.repo.SaveChallenge(ctx, userPublicID, walletAddress, nonce, expiresAt); err != nil {
		return "", "", err
	}
	return challengeMessage(userPublicID, nonce, expiresAt), nonce, nil
}

// Unlink removes the user's linked payout wallet (verified address + destination
// hint) and drops any pending challenge. Withdrawals already in flight are
// unaffected: each withdrawal row persists its own dest_wallet_address, so this
// only means a future withdrawal must prove a wallet again.
func (s *Service) Unlink(ctx context.Context, userPublicID string) error {
	if err := s.repo.ClearVerified(ctx, userPublicID); err != nil {
		return err
	}
	_ = s.repo.ClearChallenge(ctx, userPublicID)
	return nil
}

// Verify checks a base58 Ed25519 signature over the pending challenge message for
// the given wallet and, on success, records the wallet as verified for the user.
func (s *Service) Verify(ctx context.Context, userPublicID, walletAddress, signatureBase58 string) error {
	ch, found, err := s.repo.GetChallenge(ctx, userPublicID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNoChallenge
	}
	if s.clock.Now().After(ch.ExpiresAt) {
		return ErrChallengeExpired
	}
	walletAddress = strings.TrimSpace(walletAddress)
	if walletAddress != ch.WalletAddress {
		return errInvalid("wallet address does not match the challenge")
	}
	pub, err := solana.PublicKeyFromBase58(walletAddress)
	if err != nil {
		return errInvalid("invalid solana wallet address")
	}
	sig, err := solana.SignatureFromBase58(strings.TrimSpace(signatureBase58))
	if err != nil {
		return errInvalid("invalid signature encoding (expected base58)")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub[:]), []byte(challengeMessage(userPublicID, ch.Nonce, ch.ExpiresAt)), sig[:]) {
		return ErrBadWalletSignature
	}
	if err := s.repo.MarkVerified(ctx, userPublicID, walletAddress, s.clock.Now()); err != nil {
		return err
	}
	_ = s.repo.ClearChallenge(ctx, userPublicID)
	return nil
}
