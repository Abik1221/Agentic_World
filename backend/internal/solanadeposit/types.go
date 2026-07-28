// Package solanadeposit implements the Beta deposit read-path: a user opens a
// deposit session for N USDC, the frontend builds a Solana Pay transfer to the
// platform token account tagged with the session's reference pubkey, and a
// background listener watches that reference. When a matching USDC transfer is
// finalized on-chain it credits the user's treasury (via the ledger, at the
// configured peg) and records the deposit idempotently by tx signature.
//
// It never touches the blockchain to sign/broadcast — that is the withdrawal
// path. Money moves only through the Crediter port (satisfied by wallet.Service),
// so this package never reaches the ledger or DB driver directly.
package solanadeposit

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/blockchain"
)

// Session status values.
const (
	StatusPending   = "pending"   // created; no on-chain activity seen yet
	StatusDetected  = "detected"  // a referencing signature seen, not yet finalized
	StatusCompleted = "completed" // finalized + credited
	StatusExpired   = "expired"   // window elapsed with no (finalized) payment
	StatusFailed    = "failed"    // on-chain failure / unrecoverable
)

// Session is a deposit intent.
type Session struct {
	PublicID       string    `json:"deposit_id"`
	UserPublicID   string    `json:"-"`
	Reference      string    `json:"reference"`      // base58 Solana Pay reference pubkey
	Asset          string    `json:"asset"`          // "USDC"
	AmountExpected int64     `json:"amount_base"`    // token base units expected
	CoinsExpected  int64     `json:"coins_expected"` // coins credited on success
	Status         string    `json:"status"`
	TxSignature    string    `json:"tx_signature,omitempty"`
	AmountReceived int64     `json:"amount_received,omitempty"`
	CoinsCredited  int64     `json:"coins_credited,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// CreditRecord is the confirmed, credited deposit written idempotently by tx
// signature; it also flips its session to completed.
type CreditRecord struct {
	TxSignature     string
	UserPublicID    string
	SessionPublicID string
	Mint            string
	AmountBase      int64
	Coins           int64
	Slot            int64
}

// Repo is the deposit persistence port (pgx impl lives in internal/store).
type Repo interface {
	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, publicID string) (Session, error)
	// GetSessionForUser returns the session iff owned by userPublicID (ErrNotFound otherwise).
	GetSessionForUser(ctx context.Context, publicID, userPublicID string) (Session, error)
	ListByUser(ctx context.Context, userPublicID string, limit int) ([]Session, error)
	// OpenSessions returns still-open (pending|detected) sessions for the listener.
	OpenSessions(ctx context.Context, limit int) ([]Session, error)
	// MarkDetected records a first-seen referencing signature (pending → detected),
	// idempotent; a no-op if the session already advanced.
	MarkDetected(ctx context.Context, publicID, txSignature string) error
	// CompleteCredit records the credited deposit (idempotent on tx_signature) and
	// flips the session to completed. Returns already=true if that signature was
	// already recorded. MUST be called AFTER the idempotent ledger credit succeeds.
	CompleteCredit(ctx context.Context, in CreditRecord) (already bool, err error)
	// ExpireSession flips an open session to expired (idempotent).
	ExpireSession(ctx context.Context, publicID string) error
}

// Chain is the read port over the Solana RPC client (satisfied by *blockchain.Client).
type Chain interface {
	SignaturesForAddress(ctx context.Context, address string, limit int) ([]blockchain.SignatureInfo, error)
	GetTransaction(ctx context.Context, signature string) (*blockchain.Transaction, error)
}

// Crediter credits a confirmed deposit to the owner's treasury (wallet.Service).
type Crediter interface {
	CreditDeposit(ctx context.Context, userPublicID string, userCoins, feeCoins int64, idemKey string) error
}

// Gate is the Super Admin deposit gate (satisfied by walletadmin.Service): it
// blocks a deposit on maintenance mode, a disabled deposit switch, a below-minimum
// amount, or a frozen wallet. Optional (nil ⇒ no dynamic gate).
type Gate interface {
	CheckDeposit(ctx context.Context, userPublicID string, amountBase int64) error
}

// Notifier writes a user notification (idempotent per kind+ref). Optional.
type Notifier interface {
	Notify(ctx context.Context, userPublicID, kind, ref string, payload []byte) error
}

// Config holds the deposit tunables.
type Config struct {
	USDCMint       string        // SPL mint we accept
	PlatformOwner  string        // platform wallet (Solana Pay recipient)
	PlatformATA    string        // platform USDC token account (deposits must land here)
	CoinsPerUSDC   int64         // peg: coins credited per 1 USDC (default 100)
	USDCDecimals   int           // token decimals (USDC = 6)
	SessionTTL     time.Duration // how long a deposit session stays open
	MinDepositBase int64         // minimum deposit in token base units (fallback)
	// DepositFeePct is the platform's cut on every deposit (default 5): the user is
	// credited (100−fee)% of the pegged coins and the platform keeps the rest.
	DepositFeePct int
}
