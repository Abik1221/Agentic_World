package solanadeposit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/blockchain"
	"github.com/agent-arena/arena/internal/paymenttrace"
	"github.com/agent-arena/arena/internal/platform"
)

// Service orchestrates deposit sessions over the Repo, Chain and Crediter ports.
type Service struct {
	repo     Repo
	chain    Chain
	crediter Crediter
	gate     Gate     // Super Admin deposit gate; nil ⇒ no dynamic gate
	notifier Notifier // user notifications; nil ⇒ none
	// trace records which stage of the deposit flow each attempt reached, so
	// "I paid and nothing happened" has an answer. Nil-safe: every method on a nil
	// *Service is a no-op, and nothing here may fail a credit.
	trace *paymenttrace.Service
	clock platform.Clock
	cfg   Config
	log   *slog.Logger
	// minDeposit supplies the LIVE admin-configured minimum, in CENTS. Nil ⇒ the
	// static config value.
	minDeposit func() int64
	// feePct supplies the LIVE admin-configured deposit fee percentage. Nil ⇒ config.
	feePct func() int
}

// SetDepositFeeSource wires the live admin-configured entry fee.
func (s *Service) SetDepositFeeSource(f func() int) { s.feePct = f }

// SetMinDepositSource wires the admin-configured minimum top-up. The admin sets it in
// dollars; USDC carries 6 decimals, so cents convert to base units at 10^4 each.
// Nil, or a non-positive value, keeps the static config.
func (s *Service) SetMinDepositSource(f func() int64) { s.minDeposit = f }

// minDepositBase resolves the floor for a session being opened now, in base units.
func (s *Service) minDepositBase() int64 {
	if s.minDeposit != nil {
		if cents := s.minDeposit(); cents > 0 {
			return cents * 10_000
		}
	}
	return s.cfg.MinDepositBase
}

// SetGate wires the Super Admin deposit gate (walletadmin). Optional.
func (s *Service) SetGate(g Gate) { s.gate = g }

// SetNotifier wires the user-notification writer. Optional.
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// SetTracer wires the payment-flow log. Optional; purely diagnostic.
func (s *Service) SetTracer(t *paymenttrace.Service) { s.trace = t }

// New builds the deposit service, applying peg/decimal/TTL defaults.
func New(repo Repo, chain Chain, crediter Crediter, clock platform.Clock, cfg Config, log *slog.Logger) *Service {
	// Zero is a REAL setting, not "unset": deposits are free by default and the
	// platform's take is charged once, on the way out. This used to coerce 0 to 5,
	// which meant the entry fee could not actually be turned off — an operator (or
	// this default) setting it to 0 still got charged 5%. Only a negative value is
	// nonsense, and it clamps to free rather than to a charge.
	if cfg.DepositFeePct < 0 {
		cfg.DepositFeePct = 0
	}
	// Bounded 0..50, matching the live accessor and every other fee crossing the
	// config bus: above half the deposit is indistinguishable from confiscation.
	if cfg.DepositFeePct > 50 {
		cfg.DepositFeePct = 50
	}
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

// VerifyRails checks, once at startup, that the deposit destination we advertise
// is the account we actually watch.
//
// Every payer is told to send to PlatformOwner (Solana Pay URL / QR) or to that
// owner's associated token account (the one-click wallet path). Crediting, by
// contrast, only counts USDC that lands in the configured PlatformATA. If those
// two disagree — a copy-pasted address, an ATA for the wrong mint, a devnet value
// left in a mainnet deploy — then every deposit succeeds on-chain and none is ever
// credited. The payer's money is gone and the platform's ledger shows nothing,
// with no error raised anywhere in the system. That is the worst failure this rail
// can have, and it is invisible without a check like this one.
//
// It reports rather than refuses. Returning an error here would let a transient
// RPC failure at boot take the entire deposit rail offline, which is a worse
// outcome than a loud log; the caller logs at ERROR and keeps serving. `ok` is
// false only when the RPC answered clearly and the answer was wrong.
func (s *Service) VerifyRails(ctx context.Context, chain interface {
	TokenAccount(ctx context.Context, tokenAccount string) (blockchain.TokenAccountInfo, error)
}) (ok bool, detail string) {
	if s.cfg.PlatformATA == "" || s.cfg.PlatformOwner == "" || s.cfg.USDCMint == "" {
		return false, "deposit rails incomplete (mint/owner/ATA)"
	}
	info, err := chain.TokenAccount(ctx, s.cfg.PlatformATA)
	if errors.Is(err, blockchain.ErrNotToken) {
		return false, fmt.Sprintf("SOLANA_PLATFORM_ATA %s is not an SPL token account", s.cfg.PlatformATA)
	}
	if err != nil {
		// Could not reach the RPC. Unknown, not wrong.
		s.log.Warn("deposit rails not verified (rpc unavailable)", "error", err)
		return true, "unverified: " + err.Error()
	}
	if info.Mint != s.cfg.USDCMint {
		return false, fmt.Sprintf("SOLANA_PLATFORM_ATA holds mint %s but deposits accept %s — deposits will never credit",
			info.Mint, s.cfg.USDCMint)
	}
	if info.Owner != s.cfg.PlatformOwner {
		return false, fmt.Sprintf("SOLANA_PLATFORM_ATA is owned by %s but payers are directed to %s — deposits will never credit",
			info.Owner, s.cfg.PlatformOwner)
	}
	return true, "ok"
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

// depositFeePct resolves the LIVE admin-configured entry fee.
//
// The fee itself is not new — it has always been deducted at the credit site below.
// What was missing is the operator being able to change it: it was env-only, so the
// admin's economy screen could not touch the charge on money coming in even though it
// owned the one on money going out.
//
// Bounded 0..50 for the same reason as every other fee crossing the config bus: this
// value arrives from another service, and a rate above half is indistinguishable from
// confiscation.
func (s *Service) depositFeePct() int {
	if s.feePct != nil {
		if p := s.feePct(); p >= 0 && p <= 50 {
			return p
		}
	}
	return s.cfg.DepositFeePct
}

// Create opens a deposit session for amountBase token base units.
func (s *Service) Create(ctx context.Context, userPublicID string, amountBase int64) (Session, error) {
	if amountBase <= 0 {
		return Session{}, errInvalid("amount must be greater than zero")
	}
	if minBase := s.minDepositBase(); minBase > 0 && amountBase < minBase {
		return Session{}, errInvalid(fmt.Sprintf("minimum deposit is %s USDC", s.formatUSDC(minBase)))
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
	s.trace.OK(ctx, userPublicID, paymenttrace.FlowDeposit, sess.PublicID, paymenttrace.StageSessionCreated,
		map[string]any{
			"amount_base": amountBase, "coins_expected": coins, "asset": sess.Asset,
			"reference": ref, "recipient": s.cfg.PlatformOwner, "expires_at": sess.ExpiresAt,
		})
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

// Decimals is the accepted token's precision (6 for USDC/USDT). The client needs it
// to read the payer's on-chain balance BEFORE a deposit session exists.
func (s *Service) Decimals() int { return s.cfg.USDCDecimals }

// CoinsPerUSDC is the peg: how many game coins one whole token buys.
func (s *Service) CoinsPerUSDC() int64 { return s.cfg.CoinsPerUSDC }

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
			// Record WHICH state it expired from. "Expired without ever seeing a
			// transfer" means the user never paid; "expired while detected" means we
			// saw their money and dropped it, and those need opposite responses.
			s.trace.Failed(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
				paymenttrace.StageSessionExpired,
				"the payment window closed with the deposit still "+sess.Status,
				map[string]any{"status_at_expiry": sess.Status, "expires_at": sess.ExpiresAt})
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
			s.trace.Failed(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
				paymenttrace.StageChainRejected,
				"the transaction the RPC offered does not carry this deposit's reference",
				map[string]any{"signature": si.Signature})
			continue
		}
		// The transfer exists and genuinely belongs to this session. Record detection
		// HERE rather than only in the not-yet-finalized branch below: a deposit that
		// finalizes before the first poll never passes through "detected", and a
		// diagram missing a step it already completed reads as a hole in the flow.
		s.trace.OK(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
			paymenttrace.StagePaymentDetected, map[string]any{"signature": si.Signature})

		received := s.receivedToPlatform(tx)
		if received < sess.AmountExpected {
			// Underpaid is the single most common self-inflicted deposit failure, and
			// it is invisible on-chain: the transfer succeeded, it just does not satisfy
			// this request. Record both numbers so the UI can say exactly how short it
			// was instead of leaving the deposit silently pending until it expires.
			s.trace.Pending(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
				paymenttrace.StageChainFinalized,
				"a transfer arrived but it is short of the requested amount",
				map[string]any{
					"signature": si.Signature, "received_base": received,
					"expected_base": sess.AmountExpected, "shortfall_base": sess.AmountExpected - received,
				})
			continue // underpaid (or unrelated credit) — keep waiting
		}
		// Absolute sanity cap: a credit above this is a malformed/malicious RPC amount,
		// never a real deposit. Reject rather than convert (defense in depth with the
		// parse-error propagation in blockchain.tokenBalance.base). (M7)
		if received > maxCreditBase {
			s.log.Error("deposit amount exceeds sanity cap — rejecting",
				"deposit", sess.PublicID, "sig", si.Signature, "received", received)
			s.trace.Failed(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
				paymenttrace.StageChainRejected,
				"the reported transfer amount is impossibly large and was refused",
				map[string]any{"signature": si.Signature, "received_base": received})
			continue
		}
		// The transfer is final, addressed to us, and sufficient.
		s.trace.OK(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
			paymenttrace.StageChainFinalized,
			map[string]any{"signature": si.Signature, "received_base": received, "slot": tx.Slot})
		coins := s.coinsFor(received)
		// Platform deposit fee: the user is credited (100−fee)% of the pegged coins,
		// the platform keeps the rest. Floored so escrow accounting stays whole.
		feeCoins := coins * int64(s.depositFeePct()) / 100
		userCoins := coins - feeCoins

		// Credit FIRST (idempotent on the ledger key), then record. A crash between
		// the two is safe: the ledger key makes a re-credit a no-op, and the record
		// insert is idempotent on the tx signature.
		if err := s.crediter.CreditDeposit(ctx, sess.UserPublicID, userCoins, feeCoins, "solana:"+si.Signature); err != nil {
			s.log.Error("deposit credit", "deposit", sess.PublicID, "sig", si.Signature, "error", err)
			// The worst state a deposit can be in: the money is irreversibly ours and
			// the user has nothing. It must be visible to them and to an operator
			// immediately, not discoverable only by reading server logs.
			s.trace.Failed(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
				paymenttrace.StageCoinsCredited,
				"your transfer confirmed but the credit did not post",
				map[string]any{"signature": si.Signature, "coins": userCoins, "error": err.Error()})
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
			s.trace.OK(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
				paymenttrace.StageCoinsCredited, map[string]any{
					"signature": si.Signature, "coins_gross": coins, "coins_credited": userCoins,
					"fee_coins": feeCoins, "fee_pct": s.depositFeePct(),
				})
			if s.notifier != nil {
				payload, _ := json.Marshal(map[string]any{"deposit_id": sess.PublicID, "coins": coins, "amount_base": received, "signature": si.Signature})
				if err := s.notifier.Notify(ctx, sess.UserPublicID, "deposit_completed", "deposit:"+si.Signature, payload); err != nil {
					s.log.Warn("deposit notify failed", "deposit", sess.PublicID, "error", err)
					s.trace.Failed(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
						paymenttrace.StageUserNotified,
						"the credits landed but the confirmation could not be delivered",
						map[string]any{"error": err.Error()})
				} else {
					s.trace.OK(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
						paymenttrace.StageUserNotified, nil)
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
		// Two stages, deliberately: the transfer HAS been seen (ok), and finality is
		// still outstanding (pending). Collapsing them into one would lose the
		// distinction the user most needs — "we have your payment" versus "the chain
		// has not confirmed it yet" — and make a slow network look like a lost deposit.
		s.trace.OK(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
			paymenttrace.StagePaymentDetected, nil)
		s.trace.Pending(ctx, sess.UserPublicID, paymenttrace.FlowDeposit, sess.PublicID,
			paymenttrace.StageChainFinalized, "waiting for Solana to finalize the transfer", nil)
		// Tell the payer their transfer has been SEEN, on the first transition only
		// (sess.Status is the value read at the start of this pass, so a session
		// already `detected` does not re-notify — and the notification write is
		// idempotent on kind+ref regardless).
		//
		// This is the difference between "I paid and the site said nothing" and "the
		// site saw it and is waiting for finality". Finality can take a while; silence
		// during it is what makes a working deposit feel like a lost one.
		if sess.Status == StatusPending && s.notifier != nil {
			payload, _ := json.Marshal(map[string]any{
				"deposit_id": sess.PublicID, "coins": sess.CoinsExpected, "amount_base": sess.AmountExpected,
			})
			if err := s.notifier.Notify(ctx, sess.UserPublicID, "deposit_detected", "deposit:"+sess.PublicID, payload); err != nil {
				s.log.Warn("deposit detected notify failed", "deposit", sess.PublicID, "error", err)
			}
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
