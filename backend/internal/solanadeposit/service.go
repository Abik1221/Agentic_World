package solanadeposit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/blockchain"
	"github.com/agent-arena/arena/internal/platform"
)

// Service orchestrates deposit sessions over the Repo, Chain and Crediter ports.
type Service struct {
	repo     Repo
	chain    Chain
	crediter Crediter
	gate     Gate     // Super Admin deposit gate; nil ⇒ no dynamic gate
	notifier Notifier // user notifications; nil ⇒ none
	clock    platform.Clock
	cfg      Config
	log      *slog.Logger
}

// SetGate wires the Super Admin deposit gate (walletadmin). Optional.
func (s *Service) SetGate(g Gate) { s.gate = g }

// SetNotifier wires the user-notification writer. Optional.
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// New builds the deposit service, applying peg/decimal/TTL defaults.
func New(repo Repo, chain Chain, crediter Crediter, clock platform.Clock, cfg Config, log *slog.Logger) *Service {
	if cfg.CoinsPerUSDC <= 0 {
		cfg.CoinsPerUSDC = 100 // 1 USDC = 100 coins (1 coin = 1¢)
	}
	if cfg.USDCDecimals <= 0 {
		cfg.USDCDecimals = 6
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 30 * time.Minute
	}
	return &Service{repo: repo, chain: chain, crediter: crediter, clock: clock, cfg: cfg, log: log}
}

// pow10 returns 10^n for small n (token decimals).
func pow10(n int) int64 {
	out := int64(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}

// maxCreditBase is an absolute upper bound on a single deposit's base units
// (1e15 = 1,000,000,000 USDC at 6 decimals). No legitimate deposit approaches it;
// a value above it signals a malformed/malicious RPC amount and is rejected before
// conversion. It also keeps base*CoinsPerUSDC (≤ 1e17) safely within int64. (M7)
const maxCreditBase int64 = 1_000_000_000_000_000

// coinsFor converts token base units to coins at the configured peg, floored to
// whole coins (a fractional remainder below one coin is not credited). Callers
// must ensure base <= maxCreditBase (enforced at the deposit crediting site), which
// guarantees the intermediate multiply cannot overflow int64. (M7)
func (s *Service) coinsFor(base int64) int64 {
	if base <= 0 || base > maxCreditBase {
		return 0
	}
	return base * s.cfg.CoinsPerUSDC / pow10(s.cfg.USDCDecimals)
}

// Create opens a deposit session for amountBase token base units.
func (s *Service) Create(ctx context.Context, userPublicID string, amountBase int64) (Session, error) {
	if amountBase <= 0 {
		return Session{}, errInvalid("amount must be greater than zero")
	}
	if s.cfg.MinDepositBase > 0 && amountBase < s.cfg.MinDepositBase {
		return Session{}, errInvalid(fmt.Sprintf("minimum deposit is %s USDC", s.formatUSDC(s.cfg.MinDepositBase)))
	}
	coins := s.coinsFor(amountBase)
	if coins <= 0 {
		return Session{}, errInvalid("amount is below one credit")
	}
	// Super Admin gate: maintenance / deposits-disabled / minimum / frozen wallet.
	if s.gate != nil {
		if err := s.gate.CheckDeposit(ctx, userPublicID, amountBase); err != nil {
			return Session{}, err
		}
	}
	ref, err := blockchain.NewReference()
	if err != nil {
		return Session{}, err
	}
	now := s.clock.Now()
	sess := Session{
		PublicID:       platform.NewID(platform.PrefixDeposit),
		UserPublicID:   userPublicID,
		Reference:      ref,
		Asset:          "USDC",
		AmountExpected: amountBase,
		CoinsExpected:  coins,
		Status:         StatusPending,
		CreatedAt:      now,
		ExpiresAt:      now.Add(s.cfg.SessionTTL),
	}
	if err := s.repo.CreateSession(ctx, sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// USDCToBase converts a decimal USDC amount to token base units, rounded to the
// nearest base unit (avoids float drift on typical human-entered amounts).
func (s *Service) USDCToBase(amount float64) int64 {
	return int64(math.Round(amount * float64(pow10(s.cfg.USDCDecimals))))
}

// Get returns a session owned by userPublicID.
func (s *Service) Get(ctx context.Context, publicID, userPublicID string) (Session, error) {
	return s.repo.GetSessionForUser(ctx, publicID, userPublicID)
}

// List returns a user's recent deposit sessions.
func (s *Service) List(ctx context.Context, userPublicID string, limit int) ([]Session, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.repo.ListByUser(ctx, userPublicID, limit)
}

// PayURL builds the Solana Pay transfer URL for a session (recipient is the
// platform owner wallet; the payer's wallet derives the USDC ATA).
func (s *Service) PayURL(sess Session) string {
	q := url.Values{}
	q.Set("amount", s.formatUSDC(sess.AmountExpected))
	q.Set("spl-token", s.cfg.USDCMint)
	q.Set("reference", sess.Reference)
	q.Set("label", "Onavion")
	q.Set("message", "Onavion deposit "+sess.PublicID)
	return "solana:" + s.cfg.PlatformOwner + "?" + q.Encode()
}

// Recipient is the platform wallet a deposit is sent to (for the UI/QR).
func (s *Service) Recipient() string { return s.cfg.PlatformOwner }

// USDCMint exposes the accepted mint (for the UI).
func (s *Service) USDCMint() string { return s.cfg.USDCMint }

// formatUSDC renders base units as a decimal USDC string (e.g. 5000000 → "5").
func (s *Service) formatUSDC(base int64) string {
	unit := pow10(s.cfg.USDCDecimals)
	whole := base / unit
	frac := base % unit
	if frac == 0 {
		return strconv.FormatInt(whole, 10)
	}
	// Trim trailing zeros in the fractional part.
	fracStr := fmt.Sprintf("%0*d", s.cfg.USDCDecimals, frac)
	for len(fracStr) > 0 && fracStr[len(fracStr)-1] == '0' {
		fracStr = fracStr[:len(fracStr)-1]
	}
	return strconv.FormatInt(whole, 10) + "." + fracStr
}

// Poll scans open sessions once: expire the elapsed, detect referencing
// signatures, and credit finalized matching transfers. Returns how many deposits
// were credited this pass. Safe to run on a ticker; idempotent per tx signature.
func (s *Service) Poll(ctx context.Context) (int, error) {
	sessions, err := s.repo.OpenSessions(ctx, 100)
	if err != nil {
		return 0, err
	}
	now := s.clock.Now()
	credited := 0
	for _, sess := range sessions {
		if s.shouldExpire(sess, now) {
			if err := s.repo.ExpireSession(ctx, sess.PublicID); err != nil {
				s.log.Warn("deposit expire", "deposit", sess.PublicID, "error", err)
			}
			continue
		}
		if s.processSession(ctx, sess) {
			credited++
		}
	}
	return credited, nil
}

// Grace windows that keep an in-flight deposit from being dropped at the raw TTL
// and then finalizing uncredited (user fund loss, M9). A pending session (no
// on-chain activity seen) gets a short grace for a late-arriving/late-indexed
// payment; a detected session (a referencing tx was already seen) gets a long
// grace so it is never expired out from under a payment awaiting finality.
const (
	pendingExpiryGrace  = 15 * time.Minute
	detectedExpiryGrace = 24 * time.Hour
)

// shouldExpire decides whether an open session may be expired yet. A DETECTED
// session (money seen in flight) is protected far past the window; a PENDING one
// gets a short grace beyond its TTL. This prevents the "finalized just after
// expiry ⇒ never credited, no reclaim" loss. (M9)
func (s *Service) shouldExpire(sess Session, now time.Time) bool {
	if sess.Status == StatusDetected {
		return now.After(sess.ExpiresAt.Add(detectedExpiryGrace))
	}
	return now.After(sess.ExpiresAt.Add(pendingExpiryGrace))
}

// processSession checks one session's reference for a finalized, matching USDC
// transfer and credits it. Returns true if a deposit was credited.
func (s *Service) processSession(ctx context.Context, sess Session) bool {
	sigs, err := s.chain.SignaturesForAddress(ctx, sess.Reference, 25)
	if err != nil {
		s.log.Warn("deposit signatures", "deposit", sess.PublicID, "error", err)
		return false
	}
	detected := false
	for _, si := range sigs {
		if si.Err != nil {
			continue // failed transaction
		}
		// Credit ONLY at finalized commitment: a "confirmed" tx can still be dropped
		// by a reorg, but a ledger credit is irreversible. If the RPC reports a
		// non-finalized status for this signature, treat it as in-flight and wait —
		// this holds even if the deposit RPC commitment knob was set to "confirmed". (H3)
		if si.ConfirmationStatus != "" && si.ConfirmationStatus != "finalized" {
			detected = true
			continue
		}
		tx, err := s.chain.GetTransaction(ctx, si.Signature)
		if err != nil {
			s.log.Warn("deposit get tx", "deposit", sess.PublicID, "sig", si.Signature, "error", err)
			continue
		}
		if tx == nil {
			// Seen but not finalized yet — surface a "confirming" state to the UI.
			detected = true
			continue
		}
		if tx.Failed {
			continue
		}
		// Independently re-verify the session's reference is genuinely an account in
		// THIS transaction. getSignaturesForAddress(reference) binds a signature to the
		// reference on the RPC's word alone; a compromised/MITM RPC could return an
		// unrelated real deposit's signature to mint coins for an attacker. Trust the
		// transaction's own account list, not the lookup. (H3)
		if !tx.HasAccount(sess.Reference) {
			s.log.Warn("deposit reference not present in tx accounts — rejecting",
				"deposit", sess.PublicID, "sig", si.Signature)
			continue
		}
		received := s.receivedToPlatform(tx)
		if received < sess.AmountExpected {
			continue // underpaid (or unrelated credit) — keep waiting
		}
		// Absolute sanity cap: a credit above this is a malformed/malicious RPC amount,
		// never a real deposit. Reject rather than convert (defense in depth with the
		// parse-error propagation in blockchain.tokenBalance.base). (M7)
		if received > maxCreditBase {
			s.log.Error("deposit amount exceeds sanity cap — rejecting",
				"deposit", sess.PublicID, "sig", si.Signature, "received", received)
			continue
		}
		coins := s.coinsFor(received)

		// Credit FIRST (idempotent on the ledger key), then record. A crash between
		// the two is safe: the ledger key makes a re-credit a no-op, and the record
		// insert is idempotent on the tx signature.
		if err := s.crediter.CreditDeposit(ctx, sess.UserPublicID, coins, "solana:"+si.Signature); err != nil {
			s.log.Error("deposit credit", "deposit", sess.PublicID, "sig", si.Signature, "error", err)
			return false
		}
		already, err := s.repo.CompleteCredit(ctx, CreditRecord{
			TxSignature:     si.Signature,
			UserPublicID:    sess.UserPublicID,
			SessionPublicID: sess.PublicID,
			Mint:            s.cfg.USDCMint,
			AmountBase:      received,
			Coins:           coins,
			Slot:            int64(tx.Slot),
		})
		if err != nil {
			s.log.Error("deposit record", "deposit", sess.PublicID, "sig", si.Signature, "error", err)
			return false
		}
		if !already {
			s.log.Info("deposit credited", "deposit", sess.PublicID, "user", sess.UserPublicID, "coins", coins, "sig", si.Signature)
			if s.notifier != nil {
				payload, _ := json.Marshal(map[string]any{"deposit_id": sess.PublicID, "coins": coins, "amount_base": received, "signature": si.Signature})
				if err := s.notifier.Notify(ctx, sess.UserPublicID, "deposit_completed", "deposit:"+si.Signature, payload); err != nil {
					s.log.Warn("deposit notify failed", "deposit", sess.PublicID, "error", err)
				}
			}
			return true
		}
		return false
	}
	if detected {
		if err := s.repo.MarkDetected(ctx, sess.PublicID, ""); err != nil {
			s.log.Warn("deposit mark detected", "deposit", sess.PublicID, "error", err)
		}
	}
	return false
}

// receivedToPlatform sums USDC credited to the platform's CANONICAL token account
// in a tx. It matches the exact configured PlatformATA only — not "any account
// owned by the platform owner". The owner fallback would count USDC landing in
// arbitrary attacker-created token accounts that merely set their authority to the
// platform owner: still platform-controlled, but untracked/unmonitored and outside
// reconciliation. Deposits must land in the one canonical ATA. (M8)
func (s *Service) receivedToPlatform(tx *blockchain.Transaction) int64 {
	var total int64
	for _, cr := range tx.Credits {
		if cr.Mint != s.cfg.USDCMint || cr.Delta <= 0 {
			continue
		}
		if cr.Account == s.cfg.PlatformATA {
			total += cr.Delta
		}
	}
	return total
}
