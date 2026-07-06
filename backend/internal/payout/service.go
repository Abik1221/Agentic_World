package payout

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// Config tunes the cash-out economics + safety windows.
type Config struct {
	CoinCents          int64         // face value of one coin (default 1)
	SellFeePct         int           // platform cut on withdrawal (default 10)
	StripeFeePct       int           // Stripe payout fee %, passed to the user
	StripeFeeFlatCents int64         // flat Stripe payout fee, passed to the user
	MinCoins           int64         // minimum withdrawal
	Clearing           time.Duration // a request must age this long before approval
}

// Service runs the request → approve → pay cash-out workflow.
type Service struct {
	repo  Repo
	bank  Bank
	xfer  Transferrer
	clock platform.Clock
	cfg   Config
	log   *slog.Logger
	m     *metrics
}

func New(repo Repo, bank Bank, xfer Transferrer, clock platform.Clock, cfg Config, log *slog.Logger, reg *prometheus.Registry) *Service {
	if cfg.CoinCents <= 0 {
		cfg.CoinCents = 1
	}
	if cfg.MinCoins <= 0 {
		cfg.MinCoins = 500
	}
	if cfg.Clearing <= 0 {
		cfg.Clearing = 24 * time.Hour
	}
	return &Service{repo: repo, bank: bank, xfer: xfer, clock: clock, cfg: cfg, log: log, m: newMetrics(reg)}
}

// quote computes the fee breakdown for withdrawing `coins`. The platform sell fee
// (in coins) goes to revenue; the Stripe payout fee (in cents) is deducted from
// the user's payout. Net is what actually reaches the bank.
func (s *Service) quote(coins int64) Quote {
	gross := coins * s.cfg.CoinCents
	feeCoins := coins * int64(s.cfg.SellFeePct) / 100
	stripeFee := gross*int64(s.cfg.StripeFeePct)/100 + s.cfg.StripeFeeFlatCents
	net := (coins-feeCoins)*s.cfg.CoinCents - stripeFee
	return Quote{Coins: coins, GrossCents: gross, FeeCoins: feeCoins, StripeFeeCents: stripeFee, NetCents: net}
}

// Available returns the agent's withdrawable winnings and the quote for cashing
// out `coins` (or all of it when coins <= 0).
func (s *Service) Available(ctx context.Context, agentPublicID string, coins int64) (int64, Quote, error) {
	avail, err := s.repo.Withdrawable(ctx, agentPublicID)
	if err != nil {
		return 0, Quote{}, err
	}
	if coins <= 0 || coins > avail {
		coins = avail
	}
	return avail, s.quote(coins), nil
}

// List returns recent withdrawals for an owner.
func (s *Service) List(ctx context.Context, ownerUserPublicID string, limit int) ([]Withdrawal, error) {
	return s.repo.ListByOwner(ctx, ownerUserPublicID, limit)
}

// AdminQueue returns withdrawals awaiting operator action.
func (s *Service) AdminQueue(ctx context.Context, status string, limit int) ([]AdminWithdrawal, error) {
	if status == "" {
		status = "requested"
	}
	items, err := s.repo.ListByStatus(ctx, status, limit)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	out := make([]AdminWithdrawal, len(items))
	for i, w := range items {
		wait := s.cfg.Clearing - now.Sub(w.RequestedAt)
		if wait < 0 {
			wait = 0
		}
		out[i] = AdminWithdrawal{
			Withdrawal: w, Owner: w.Owner, CanApprove: wait == 0,
			ClearingWaitMs: wait.Milliseconds(),
		}
	}
	return out, nil
}

// Request validates and files a withdrawal, locking the coins in escrow.
func (s *Service) Request(ctx context.Context, callerUserPublicID, agentPublicID string, coins int64) (Withdrawal, error) {
	owner, connect, err := s.repo.AgentOwner(ctx, agentPublicID)
	if err != nil {
		return Withdrawal{}, err
	}
	if owner != callerUserPublicID {
		return Withdrawal{}, ErrForbiddenSelf
	}
	if connect == "" {
		return Withdrawal{}, ErrNoKYC
	}
	if flagged, err := s.repo.AgentFlagged(ctx, agentPublicID); err != nil {
		return Withdrawal{}, err
	} else if flagged {
		return Withdrawal{}, ErrFlagged
	}
	if debt, err := s.repo.OutstandingDebt(ctx, agentPublicID); err != nil {
		return Withdrawal{}, err
	} else if debt > 0 {
		return Withdrawal{}, ErrDebt
	}
	if coins < s.cfg.MinCoins {
		return Withdrawal{}, ErrTooSmall
	}
	avail, err := s.repo.Withdrawable(ctx, agentPublicID)
	if err != nil {
		return Withdrawal{}, err
	}
	if coins > avail {
		return Withdrawal{}, ErrInsufficient
	}
	q := s.quote(coins)
	if q.NetCents <= 0 {
		return Withdrawal{}, ErrTooSmall
	}

	w := Withdrawal{
		PublicID: platform.NewID("wd"), Agent: agentPublicID, Owner: owner,
		Coins: coins, FeeCoins: q.FeeCoins, GrossCents: q.GrossCents,
		StripeFeeCents: q.StripeFeeCents, NetCents: q.NetCents,
		ConnectAccount: connect, Status: "requested",
	}
	// Lock the coins first, then record the request. If recording fails, release.
	if err := s.bank.Hold(ctx, w.PublicID, agentPublicID, coins); err != nil {
		return Withdrawal{}, err
	}
	if err := s.repo.Create(ctx, w); err != nil {
		_ = s.bank.Release(ctx, w.PublicID, agentPublicID, coins)
		return Withdrawal{}, err
	}
	s.m.requested.Inc()
	s.audit(ctx, callerUserPublicID, "withdrawal_request", w.PublicID, map[string]any{"agent": agentPublicID, "coins": coins, "net_cents": q.NetCents})
	return w, nil
}

// Approve executes a cleared withdrawal: transfer to the connected account, burn
// the held coins, mark paid. Idempotent (a paid withdrawal re-approves to a no-op;
// the transfer + burn carry their own idempotency keys).
func (s *Service) Approve(ctx context.Context, adminUserID, publicID string) error {
	w, err := s.repo.Get(ctx, publicID)
	if err != nil {
		return err
	}
	switch w.Status {
	case "paid":
		return nil // idempotent
	case "requested":
		// ok
	default:
		return ErrBadState
	}
	if s.clock.Now().Sub(w.RequestedAt) < s.cfg.Clearing {
		return ErrClearing
	}
	if flagged, err := s.repo.AgentFlagged(ctx, w.Agent); err != nil {
		return err
	} else if flagged {
		return ErrFlagged
	}
	// KYC/capabilities must actually be complete before we move money — a Connect
	// id existing is not enough. Fail closed so the hold stays and the admin can
	// retry once the payee finishes onboarding.
	if ok, err := s.xfer.PayoutsEnabled(ctx, w.ConnectAccount); err != nil {
		return err
	} else if !ok {
		return ErrNoKYC
	}

	transferID, err := s.xfer.Transfer(ctx, w.ConnectAccount, w.NetCents, "wd:"+w.PublicID)
	if err != nil {
		// Payout failed: return the held coins and mark it failed for re-request.
		_ = s.bank.Release(ctx, w.PublicID, w.Agent, w.Coins)
		_, _ = s.repo.SetStatus(ctx, w.PublicID, "requested", "failed", "", err.Error())
		s.audit(ctx, adminUserID, "withdrawal_failed", w.PublicID, map[string]any{"error": err.Error()})
		return err
	}
	if err := s.bank.Payout(ctx, w.PublicID, w.Agent, w.Coins, w.FeeCoins); err != nil {
		return err
	}
	if _, err := s.repo.SetStatus(ctx, w.PublicID, "requested", "paid", transferID, ""); err != nil {
		return err
	}
	s.m.paid.Inc()
	s.m.paidCents.Add(float64(w.NetCents))
	s.audit(ctx, adminUserID, "withdrawal_paid", w.PublicID, map[string]any{"transfer": transferID, "net_cents": w.NetCents})
	return nil
}

// Reject denies a withdrawal and returns the held coins. Idempotent.
func (s *Service) Reject(ctx context.Context, adminUserID, publicID, reason string) error {
	w, err := s.repo.Get(ctx, publicID)
	if err != nil {
		return err
	}
	if w.Status == "rejected" {
		return nil
	}
	if w.Status != "requested" {
		return ErrBadState
	}
	// Release first (idempotent), then flip status — a retry converges safely.
	if err := s.bank.Release(ctx, w.PublicID, w.Agent, w.Coins); err != nil {
		return err
	}
	if _, err := s.repo.SetStatus(ctx, w.PublicID, "requested", "rejected", "", reason); err != nil {
		return err
	}
	s.audit(ctx, adminUserID, "withdrawal_reject", w.PublicID, map[string]any{"reason": reason})
	return nil
}

// Get returns a withdrawal (ownership checked by the handler).
func (s *Service) Get(ctx context.Context, publicID string) (Withdrawal, error) {
	return s.repo.Get(ctx, publicID)
}

// ReverseByTransfer reconciles a payout that Stripe later reversed (transfer.reversed
// webhook): it re-credits the agent's coins (the exact inverse of the burn) and marks
// the withdrawal `reversed`. Idempotent and safe to replay — an unknown transfer, a
// non-paid withdrawal, or an already-reversed one is a no-op. This is the mirror of
// the deposit-side refund/dispute reversal, so the ledger stays self-healing.
func (s *Service) ReverseByTransfer(ctx context.Context, transferID, reason string) error {
	if transferID == "" {
		return nil
	}
	w, err := s.repo.GetByTransferID(ctx, transferID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.log.Warn("payout: transfer.reversed for an unknown transfer; ignoring", "transfer", transferID)
			return nil
		}
		return err
	}
	if w.Status == "reversed" {
		return nil // already reconciled
	}
	if w.Status != "paid" {
		s.log.Warn("payout: transfer.reversed for a non-paid withdrawal; ignoring",
			"id", w.PublicID, "status", w.Status, "transfer", transferID)
		return nil
	}
	// Re-credit before flipping status; the ledger key makes the re-credit single-effect.
	if err := s.bank.ReversePayout(ctx, w.PublicID, w.Agent, w.Coins, w.FeeCoins); err != nil {
		return err
	}
	if _, err := s.repo.SetStatus(ctx, w.PublicID, "paid", "reversed", w.TransferID, reason); err != nil {
		return err
	}
	s.m.reversed.Inc()
	s.audit(ctx, "stripe", "withdrawal_reversed", w.PublicID,
		map[string]any{"reason": reason, "coins": w.Coins, "transfer": transferID})
	return nil
}

func (s *Service) audit(ctx context.Context, actor, action, target string, detail map[string]any) {
	b, _ := json.Marshal(detail)
	if err := s.repo.Audit(ctx, actor, action, target, b); err != nil {
		s.log.Error("payout: audit write failed", "action", action, "target", target, "error", err)
	}
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	requested prometheus.Counter
	paid      prometheus.Counter
	paidCents prometheus.Counter
	reversed  prometheus.Counter
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		requested: prometheus.NewCounter(prometheus.CounterOpts{Name: "withdrawals_requested_total", Help: "Cash-out requests filed."}),
		paid:      prometheus.NewCounter(prometheus.CounterOpts{Name: "withdrawals_paid_total", Help: "Cash-outs paid to a bank."}),
		paidCents: prometheus.NewCounter(prometheus.CounterOpts{Name: "withdrawals_paid_cents_total", Help: "Total cents paid out to users."}),
		reversed:  prometheus.NewCounter(prometheus.CounterOpts{Name: "withdrawals_reversed_total", Help: "Payouts reversed by Stripe and re-credited."}),
	}
	reg.MustRegister(m.requested, m.paid, m.paidCents, m.reversed)
	return m
}
