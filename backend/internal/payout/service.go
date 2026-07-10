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
	StripeFeePct       int           // network/processing fee %, passed to the user (0 for Solana)
	StripeFeeFlatCents int64         // flat network/processing fee, passed to the user
	MinCoins           int64         // minimum withdrawal
	Clearing           time.Duration // a request must age this long before approval
	Chain              string        // payout rail: ChainStripe (default) | ChainSolana
}

// Service runs the request → approve → pay cash-out workflow.
type Service struct {
	repo      Repo
	bank      Bank
	xfer      Transferrer
	confirmer Confirmer // Solana on-chain confirmation; nil in Stripe mode
	gate      Gate      // Super Admin withdrawal gate; nil ⇒ no dynamic gate
	notifier  Notifier  // user notifications; nil ⇒ none
	clock     platform.Clock
	cfg       Config
	log       *slog.Logger
	m         *metrics
}

// SetGate wires the Super Admin withdrawal gate (walletadmin). Optional.
func (s *Service) SetGate(g Gate) { s.gate = g }

// SetNotifier wires the user-notification writer. Optional.
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// notify writes a withdrawal notification (best-effort; never blocks the flow).
func (s *Service) notify(ctx context.Context, owner, kind, withdrawalID string, netCents int64) {
	if s.notifier == nil || owner == "" {
		return
	}
	payload, _ := json.Marshal(map[string]any{"withdrawal_id": withdrawalID, "net_cents": netCents})
	if err := s.notifier.Notify(ctx, owner, kind, "withdrawal:"+withdrawalID, payload); err != nil {
		s.log.Warn("payout: notify failed", "id", withdrawalID, "kind", kind, "error", err)
	}
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
	if cfg.Chain == "" {
		cfg.Chain = ChainStripe
	}
	return &Service{repo: repo, bank: bank, xfer: xfer, clock: clock, cfg: cfg, log: log, m: newMetrics(reg)}
}

// SetConfirmer wires the on-chain confirmation checker (Solana mode). The
// confirmation watcher (ConfirmBroadcasted) is a no-op until this is set.
func (s *Service) SetConfirmer(c Confirmer) { s.confirmer = c }

// solana reports whether the service runs on the Solana payout rail.
func (s *Service) solana() bool { return s.cfg.Chain == ChainSolana }

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
func (s *Service) Available(ctx context.Context, callerUserPublicID, agentPublicID string, coins int64) (int64, Quote, error) {
	// Ownership check (mirrors Request): a user may only read their own agent's
	// withdrawable balance/quote — this leaks another user's net winnings otherwise.
	owner, _, err := s.repo.AgentOwner(ctx, agentPublicID)
	if err != nil {
		return 0, Quote{}, err
	}
	if owner != callerUserPublicID {
		return 0, Quote{}, ErrForbiddenSelf
	}
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
	// Resolve the payout destination for the active rail: a Solana wallet address
	// (Beta) or a Stripe Connect account.
	var destWallet string
	if s.solana() {
		w, err := s.repo.DestinationWallet(ctx, owner)
		if err != nil {
			return Withdrawal{}, err
		}
		if w == "" {
			return Withdrawal{}, ErrNoWallet
		}
		destWallet = w
	} else if connect == "" {
		return Withdrawal{}, ErrNoKYC
	}
	// Super Admin gate: maintenance / withdrawals-disabled / bounds / frozen wallet.
	if s.gate != nil {
		if err := s.gate.CheckWithdraw(ctx, owner, coins); err != nil {
			return Withdrawal{}, err
		}
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
		ConnectAccount: connect, Chain: s.cfg.Chain, DestWallet: destWallet, Status: "requested",
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

// Approve executes a cleared withdrawal. Stripe: transfer → burn → paid, in one
// step. Solana: claim → broadcast → 'broadcasted' (coins stay held), with a
// confirmation watcher burning them once the tx finalizes. Idempotent: a paid or
// in-flight withdrawal re-approves to a no-op.
func (s *Service) Approve(ctx context.Context, adminUserID, publicID string) error {
	w, err := s.repo.Get(ctx, publicID)
	if err != nil {
		return err
	}
	switch w.Status {
	case "paid":
		return nil // idempotent
	case "processing", "broadcasted":
		// Solana in-flight: a confirmation watcher finalizes it. Re-approving must
		// never re-broadcast (double-spend), so treat as an idempotent no-op.
		if w.Chain == ChainSolana {
			return nil
		}
		return ErrBadState
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
	if w.Chain == ChainSolana {
		return s.approveSolana(ctx, adminUserID, w)
	}
	return s.approveStripe(ctx, adminUserID, w)
}

// approveStripe transfers to the connected account, burns the held coins, and
// marks the withdrawal paid — synchronous because a Stripe transfer settles at
// the API call (idempotency-key protected).
func (s *Service) approveStripe(ctx context.Context, adminUserID string, w Withdrawal) error {
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
	s.notify(ctx, w.Owner, "withdrawal_paid", w.PublicID, w.NetCents)
	return nil
}

// approveSolana claims the withdrawal (so a retry can't double-broadcast), then
// broadcasts the USDC transfer. Coins stay HELD in escrow through 'broadcasted';
// they are burned only once ConfirmBroadcasted sees the tx finalize.
func (s *Service) approveSolana(ctx context.Context, adminUserID string, w Withdrawal) error {
	if ok, err := s.xfer.PayoutsEnabled(ctx, w.DestWallet); err != nil {
		return err
	} else if !ok {
		return ErrNoWallet
	}
	// Atomic claim BEFORE broadcasting: Solana sends aren't idempotent by key, so a
	// concurrent/retried approve must be locked out to prevent a double payout.
	claimed, err := s.repo.SetStatus(ctx, w.PublicID, "requested", "processing", "", "")
	if err != nil {
		return err
	}
	if !claimed {
		return nil // another approver claimed it, or it advanced — idempotent
	}
	sig, err := s.xfer.Transfer(ctx, w.DestWallet, w.NetCents, "wd:"+w.PublicID)
	if err != nil {
		// A broadcast-ambiguous error means the send RPC failed but the signed tx MAY
		// have landed on-chain. Releasing here would double-pay (USDC gone AND coins
		// back). Record the signature, keep the coins HELD in 'broadcasted', and let
		// ConfirmBroadcasted settle it from the chain. Only a definitive pre-broadcast
		// failure (nothing was sent) releases the hold.
		var amb *BroadcastAmbiguousError
		if errors.As(err, &amb) && amb.Signature != "" {
			if _, e := s.repo.SetStatus(ctx, w.PublicID, "processing", "broadcasted", amb.Signature, "broadcast ambiguous"); e != nil {
				s.log.Error("payout: ambiguous solana broadcast; status write failed; needs reconciliation",
					"id", w.PublicID, "sig", amb.Signature, "error", e)
				return e
			}
			s.log.Warn("payout: ambiguous solana broadcast; holding for on-chain confirmation",
				"id", w.PublicID, "sig", amb.Signature, "error", err)
			s.audit(ctx, adminUserID, "withdrawal_broadcasted", w.PublicID,
				map[string]any{"signature": amb.Signature, "ambiguous": true})
			return err
		}
		// Definitive pre-broadcast failure: nothing was sent — safe to release.
		_ = s.bank.Release(ctx, w.PublicID, w.Agent, w.Coins)
		_, _ = s.repo.SetStatus(ctx, w.PublicID, "processing", "failed", "", err.Error())
		s.audit(ctx, adminUserID, "withdrawal_failed", w.PublicID, map[string]any{"error": err.Error()})
		return err
	}
	// Broadcast succeeded. Record the signature; coins stay held until the tx
	// finalizes. If THIS write fails, funds may be in flight — never release; leave
	// it 'processing' with a loud log for ops reconciliation.
	if _, err := s.repo.SetStatus(ctx, w.PublicID, "processing", "broadcasted", sig, ""); err != nil {
		s.log.Error("payout: solana broadcast succeeded but status write failed; needs reconciliation",
			"id", w.PublicID, "sig", sig, "error", err)
		return err
	}
	s.audit(ctx, adminUserID, "withdrawal_broadcasted", w.PublicID,
		map[string]any{"signature": sig, "net_cents": w.NetCents, "wallet": w.DestWallet})
	return nil
}

// ConfirmBroadcasted advances broadcasted Solana withdrawals: on finalized
// success it burns the held coins and marks paid; on on-chain failure it releases
// the hold and marks failed. Idempotent (burn/release are keyed on the withdrawal
// id) and safe to run on a ticker. No-op in Stripe mode / before a confirmer is set.
func (s *Service) ConfirmBroadcasted(ctx context.Context) (int, error) {
	if s.confirmer == nil {
		return 0, nil
	}
	items, err := s.repo.ListByStatus(ctx, "broadcasted", 100)
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, w := range items {
		finalized, ok, err := s.confirmer.Confirm(ctx, w.TransferID)
		if err != nil {
			s.log.Warn("payout: confirm check", "id", w.PublicID, "sig", w.TransferID, "error", err)
			continue
		}
		if !finalized {
			continue // still confirming
		}
		if ok {
			if err := s.bank.Payout(ctx, w.PublicID, w.Agent, w.Coins, w.FeeCoins); err != nil {
				s.log.Error("payout: confirm burn", "id", w.PublicID, "error", err)
				continue
			}
			if _, err := s.repo.SetStatus(ctx, w.PublicID, "broadcasted", "paid", w.TransferID, ""); err != nil {
				s.log.Error("payout: confirm status", "id", w.PublicID, "error", err)
				continue
			}
			s.m.paid.Inc()
			s.m.paidCents.Add(float64(w.NetCents))
			s.audit(ctx, "solana:confirm", "withdrawal_paid", w.PublicID, map[string]any{"signature": w.TransferID, "net_cents": w.NetCents})
			s.notify(ctx, w.Owner, "withdrawal_paid", w.PublicID, w.NetCents)
		} else {
			// Finalized but the transaction failed on-chain: give the coins back.
			if err := s.bank.Release(ctx, w.PublicID, w.Agent, w.Coins); err != nil {
				s.log.Error("payout: confirm release", "id", w.PublicID, "error", err)
				continue
			}
			if _, err := s.repo.SetStatus(ctx, w.PublicID, "broadcasted", "failed", w.TransferID, "on-chain failure"); err != nil {
				s.log.Error("payout: confirm fail status", "id", w.PublicID, "error", err)
				continue
			}
			s.audit(ctx, "solana:confirm", "withdrawal_failed", w.PublicID, map[string]any{"signature": w.TransferID, "reason": "on-chain failure"})
			s.notify(ctx, w.Owner, "withdrawal_failed", w.PublicID, w.NetCents)
		}
		settled++
	}
	return settled, nil
}

// OnAccountUpdated reacts to a Stripe account.updated webhook: once a connected
// account can receive payouts (KYC finished), it re-attempts approval of that
// account's still-pending withdrawals — the ones the capability gate previously
// blocked. Each goes through the full Approve path, so the clearing window, fraud
// gate, and idempotency all still apply; a not-yet-cleared or otherwise-ineligible
// item is simply left for the admin queue. Business-state errors are logged, not
// propagated, so the webhook isn't retried for a normal "still clearing" outcome.
func (s *Service) OnAccountUpdated(ctx context.Context, connectAccountID string, payoutsEnabled bool) error {
	if connectAccountID == "" || !payoutsEnabled {
		return nil
	}
	pending, err := s.repo.PendingByConnectAccount(ctx, connectAccountID)
	if err != nil {
		return err
	}
	for _, w := range pending {
		switch err := s.Approve(ctx, "stripe:account.updated", w.PublicID); {
		case err == nil:
			s.log.Info("payout: auto-approved after KYC completed", "id", w.PublicID, "account", connectAccountID)
		case errors.Is(err, ErrClearing), errors.Is(err, ErrFlagged), errors.Is(err, ErrNoKYC),
			errors.Is(err, ErrBadState), errors.Is(err, ErrInsufficient):
			s.log.Info("payout: auto-approve deferred", "id", w.PublicID, "reason", err)
		default:
			// Unexpected (e.g. transient DB/transfer error): surface it so Stripe
			// retries the event; already-paid items are idempotent no-ops on retry.
			return err
		}
	}
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
