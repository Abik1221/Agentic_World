package payout

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/paymenttrace"
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
	// Anti-drain controls (0 ⇒ that limit is disabled).
	VelocityWindow     time.Duration // rolling window for the two caps below
	MaxPerWindow       int           // max withdrawals per owner per window
	MaxCentsPerWindow  int64         // max net cents per owner per window
	NewAddressCooldown time.Duration // freeze payouts until the verified wallet is this old
}

// Service runs the request → approve → pay cash-out workflow.
type Service struct {
	repo      Repo
	bank      Bank
	xfer      Transferrer
	confirmer Confirmer // Solana on-chain confirmation; nil in Stripe mode
	gate      Gate      // Super Admin withdrawal gate; nil ⇒ no dynamic gate
	notifier  Notifier  // user notifications; nil ⇒ none
	// trace records which stage of the cash-out each request reached. Nil-safe.
	trace *paymenttrace.Service
	clock platform.Clock
	cfg   Config
	// economy supplies the LIVE admin-configured fee/minimum. Nil ⇒ static config.
	economy func() (feePct int, minCoins int64)
	// breaker halts ALL payouts when total outflow spikes. Nil disables it.
	breaker *Breaker
	// totals feeds the breaker platform-wide volume. Satisfied by the payout repo.
	totals PayoutTotals
	// funding reports whether the payout wallet can settle an amount right now.
	// Nil ⇒ no funding pre-check (the Stripe/Dev rails, which have no hot wallet).
	funding FundingSource
	log     *slog.Logger
	m       *metrics
}

// FundingSource answers, without I/O, whether the payout rail can settle amountCents
// right now — and if not, one sentence naming what an operator must do. Satisfied by
// *SolvencyMonitor.
//
// Synchronous and I/O-free on purpose: this is consulted on an interactive admin click,
// where waiting on an RPC round trip would be a real latency cost for a marginally
// fresher number. It reads the monitor's most recent reconciliation instead.
type FundingSource interface {
	CanPay(amountCents int64) (ok bool, reason string)
}

// SetGate wires the Super Admin withdrawal gate (walletadmin). Optional.
func (s *Service) SetGate(g Gate) { s.gate = g }

// SetBreaker wires the payout circuit breaker. Nil leaves payouts unguarded.
func (s *Service) SetBreaker(b *Breaker, totals PayoutTotals) {
	s.breaker, s.totals = b, totals
}

// BreakerState reports the halt status for the admin surface.
func (s *Service) BreakerState() (open bool, reason string, since time.Time) {
	if s.breaker == nil {
		return false, "", time.Time{}
	}
	return s.breaker.Tripped()
}

// ResumePayouts clears a tripped breaker. Deliberately an explicit human action —
// see the note on Breaker.
func (s *Service) ResumePayouts(adminUserID string) {
	if s.breaker == nil {
		return
	}
	open, reason, since := s.breaker.Tripped()
	s.breaker.Reset()
	if open {
		s.log.Warn("payout: circuit breaker RESET by admin",
			"admin", adminUserID, "was_open_since", since, "original_reason", reason)
	}
}

// checkBreaker guards a payout about to happen. Evaluated at BOTH request and
// approval: request is where a flood first shows up, and approval is where the money
// actually leaves — a queue built up before the breaker tripped must not drain
// afterwards just because each item was accepted earlier.
func (s *Service) checkBreaker(ctx context.Context, pendingCents int64) error {
	if s.breaker == nil || s.totals == nil {
		return nil
	}
	if err := s.breaker.Check(ctx, s.totals, s.clock.Now(), pendingCents); err != nil {
		s.log.Error("payout: CIRCUIT BREAKER OPEN — payouts halted", "error", err)
		return err
	}
	return nil
}

// SetFunding wires the payout-wallet funding pre-check used at approval. Optional:
// unset (or a nil source) leaves approval exactly as it was, which is what the
// Stripe/Dev rails want — they have no hot wallet whose balance could be short.
func (s *Service) SetFunding(f FundingSource) { s.funding = f }

// checkFunding refuses an approval the payout wallet demonstrably cannot settle.
//
// Evaluated at APPROVAL only, not at request. At request time the money does not move
// and the float may well be topped up before the clearing window expires, so refusing
// there would reject cash-outs that are going to be perfectly payable — and would leak
// the platform's treasury state to the user, who can do nothing about it. Approval is
// the moment the transfer is actually built, and the operator is someone who CAN act on
// the answer.
//
// Fails open by construction: the source itself returns ok when it does not know (see
// SolvencyMonitor.CanPay). A pre-check that halted payouts because its own RPC was down
// would turn an observability outage into a money outage.
func (s *Service) checkFunding(w Withdrawal) error {
	if s.funding == nil {
		return nil
	}
	ok, reason := s.funding.CanPay(w.NetCents)
	if ok {
		return nil
	}
	// WARN not ERROR: this is a treasury-operations task, not a fault. It is the signal
	// to move funds, and it is expected periodically on a split-custody deployment where
	// the hot wallet is deliberately a small float.
	s.log.Warn("payout: approval refused — payout wallet cannot settle this cash-out",
		"withdrawal", w.PublicID, "net_cents", w.NetCents, "reason", reason)
	return unfundedError(reason)
}

// SetNotifier wires the user-notification writer. Optional.
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// SetTracer wires the payment-flow log — which stage of the cash-out each request
// reached. Optional and purely diagnostic; nil-safe, and no payout may fail
// because of it.
func (s *Service) SetTracer(t *paymenttrace.Service) { s.trace = t }

// traceStage is the withdrawal-side shorthand: every call already knows the flow
// and the ref, so keeping them out of the call sites keeps the money code readable.
func (s *Service) traceOK(ctx context.Context, w Withdrawal, stage string, meta map[string]any) {
	s.trace.OK(ctx, w.Owner, paymenttrace.FlowWithdrawal, w.PublicID, stage, meta)
}

func (s *Service) traceFailed(ctx context.Context, w Withdrawal, stage, detail string, meta map[string]any) {
	s.trace.Failed(ctx, w.Owner, paymenttrace.FlowWithdrawal, w.PublicID, stage, detail, meta)
}

// notify writes a withdrawal notification (best-effort; never blocks the flow).
//
// `coins` is carried alongside the cash figure because the two answer different
// questions: net_cents is what lands in the wallet, coins is what left the balance.
// A "requested" notice that states only the dollars cannot explain the number the
// user is actually watching change.
func (s *Service) notify(ctx context.Context, owner, kind, withdrawalID string, netCents, coins int64) {
	if s.notifier == nil || owner == "" {
		return
	}
	payload, _ := json.Marshal(map[string]any{"withdrawal_id": withdrawalID, "net_cents": netCents, "coins": coins})
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
	if cfg.VelocityWindow <= 0 {
		cfg.VelocityWindow = 24 * time.Hour
	}
	if cfg.Chain == "" {
		cfg.Chain = ChainStripe
	}
	if cfg.Chain == ChainSolana {
		// The Solana rail has no Stripe processing fee — the only network cost is the
		// hot wallet's SOL tx fee, which the platform absorbs. Charging a Stripe fee on
		// this rail shortchanges the user AND drifts the stripe_clearing ledger vs the
		// actual on-chain USDC outflow (a reconciliation gap that grows per payout).
		cfg.StripeFeePct = 0
		cfg.StripeFeeFlatCents = 0
	}
	return &Service{repo: repo, bank: bank, xfer: xfer, clock: clock, cfg: cfg, log: log, m: newMetrics(reg)}
}

// SetConfirmer wires the on-chain confirmation checker (Solana mode). The
// confirmation watcher (ConfirmBroadcasted) is a no-op until this is set.
func (s *Service) SetConfirmer(c Confirmer) { s.confirmer = c }

// SetEconomySource wires the LIVE admin-configured cash-out economics (withdrawal
// fee %, minimum withdrawal in coins). Nil keeps the static config.
//
// Read when a withdrawal is REQUESTED. The resulting fee is persisted on the
// withdrawal row and payout settles from that row, so an admin changing the fee never
// re-prices a cash-out a user already asked for — they are charged the fee they were
// quoted, which is the only defensible rule. Same timing contract as the match rake.
func (s *Service) SetEconomySource(f func() (feePct int, minCoins int64)) { s.economy = f }

// feePct resolves the withdrawal fee for a quote being made now.
func (s *Service) feePct() int {
	if s.economy != nil {
		if p, _ := s.economy(); p >= 0 && p <= 50 {
			return p
		}
	}
	return s.cfg.SellFeePct
}

// minCoins resolves the minimum withdrawal for a request being made now.
func (s *Service) minCoins() int64 {
	if s.economy != nil {
		if _, m := s.economy(); m > 0 {
			return m
		}
	}
	return s.cfg.MinCoins
}

// solana reports whether the service runs on the Solana payout rail.
func (s *Service) solana() bool { return s.cfg.Chain == ChainSolana }

// quote computes the fee breakdown for withdrawing `coins`. The platform sell fee
// (in coins) goes to revenue; the Stripe payout fee (in cents) is deducted from
// the user's payout. Net is what actually reaches the bank.
func (s *Service) quote(coins int64) Quote {
	if coins <= 0 {
		return Quote{}
	}
	gross := coins * s.cfg.CoinCents
	feeCoins := coins * int64(s.feePct()) / 100
	stripeFee := gross*int64(s.cfg.StripeFeePct)/100 + s.cfg.StripeFeeFlatCents
	net := (coins-feeCoins)*s.cfg.CoinCents - stripeFee
	return Quote{Coins: coins, GrossCents: gross, FeeCoins: feeCoins, StripeFeeCents: stripeFee, NetCents: net}
}

// Available returns the agent's withdrawable winnings and the quote for cashing
// out `coins` (or all of it when coins <= 0).
func (s *Service) Available(ctx context.Context, callerUserPublicID, agentPublicID string, coins int64) (int64, Quote, error) {
	if agentPublicID == "" {
		var err error
		agentPublicID, err = s.repo.PrimaryAgent(ctx, callerUserPublicID)
		if err != nil {
			return 0, Quote{}, err
		}
		if agentPublicID == "" {
			return 0, s.quote(0), nil
		}
	}
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
		hint, err := s.repo.DestinationWallet(ctx, owner)
		if err != nil {
			return Withdrawal{}, err
		}
		if hint == "" {
			return Withdrawal{}, ErrNoWallet
		}
		// Only pay a wallet whose ownership the user has PROVEN (signed challenge),
		// and only if it still matches the linked destination — so an unverified or
		// swapped hint can't redirect funds (W2).
		verified, err := s.repo.VerifiedWallet(ctx, owner)
		if err != nil {
			return Withdrawal{}, err
		}
		if verified == "" || verified != hint {
			return Withdrawal{}, ErrWalletNotVerified
		}
		destWallet = verified
		// New-address cooldown: a freshly (re)verified wallet is frozen briefly so a
		// taken-over account can't swap the payout wallet and immediately drain. CEX-
		// standard control; the legitimate owner just waits out the short hold.
		if s.cfg.NewAddressCooldown > 0 {
			_, verifiedAt, err := s.repo.VerifiedWalletAt(ctx, owner)
			if err != nil {
				return Withdrawal{}, err
			}
			if !verifiedAt.IsZero() && s.clock.Now().Sub(verifiedAt) < s.cfg.NewAddressCooldown {
				return Withdrawal{}, ErrAddressCooldown
			}
		}
	} else if connect == "" {
		return Withdrawal{}, ErrNoKYC
	}
	// Everything from here — the velocity/entitlement checks, the coin Hold, and the
	// row Create — must be ATOMIC per owner, or two concurrent requests each read a
	// stale `Withdrawable`/velocity (before either row exists) and both pass, letting
	// an owner over-withdraw beyond their net winnings (deposit laundering) and blow
	// past the velocity caps. A per-owner advisory lock serializes it: a sibling
	// request waits, then reads the committed state and is correctly rejected.
	var w Withdrawal
	if err := s.repo.WithOwnerLock(ctx, owner, func() error {
		// Velocity caps: bound how fast a single owner can move money out, per rolling
		// window — a scripted drain (e.g. after a session compromise) hits the wall.
		if s.cfg.MaxPerWindow > 0 || s.cfg.MaxCentsPerWindow > 0 {
			since := s.clock.Now().Add(-s.cfg.VelocityWindow)
			count, cents, err := s.repo.WithdrawnSince(ctx, owner, since)
			if err != nil {
				return err
			}
			if s.cfg.MaxPerWindow > 0 && count >= s.cfg.MaxPerWindow {
				return ErrVelocity
			}
			if s.cfg.MaxCentsPerWindow > 0 && cents+s.quote(coins).NetCents > s.cfg.MaxCentsPerWindow {
				return ErrVelocity
			}
		}
		// Super Admin gate: maintenance / withdrawals-disabled / bounds / frozen wallet.
		if s.gate != nil {
			if err := s.gate.CheckWithdraw(ctx, owner, coins); err != nil {
				return err
			}
		}
		if flagged, err := s.repo.AgentFlagged(ctx, agentPublicID); err != nil {
			return err
		} else if flagged {
			return ErrFlagged
		}
		if debt, err := s.repo.OutstandingDebt(ctx, agentPublicID); err != nil {
			return err
		} else if debt > 0 {
			return ErrDebt
		}
		// Minimum-withdrawal single source of truth: when the Super Admin gate is wired
		// it owns the coin minimum/maximum; the env floor is only a no-gate fallback.
		if s.gate == nil && coins < s.minCoins() {
			return ErrTooSmall
		}
		avail, err := s.repo.Withdrawable(ctx, agentPublicID)
		if err != nil {
			return err
		}
		if coins > avail {
			return ErrInsufficient
		}
		q := s.quote(coins)
		// Inside the lock on purpose: outside it, a burst of concurrent requests each
		// read the same pre-burst volume and every one of them passes. Serialized,
		// each sees the committed total including its siblings.
		if err := s.checkBreaker(ctx, q.NetCents); err != nil {
			return err
		}
		if q.NetCents <= 0 {
			return ErrTooSmall
		}
		w = Withdrawal{
			PublicID: platform.NewID("wd"), Agent: agentPublicID, Owner: owner,
			Coins: coins, FeeCoins: q.FeeCoins, GrossCents: q.GrossCents,
			StripeFeeCents: q.StripeFeeCents, NetCents: q.NetCents,
			ConnectAccount: connect, Chain: s.cfg.Chain, DestWallet: destWallet, Status: "requested",
		}
		// Bring the coins to the wallet the escrow leg draws from.
		//
		// `avail` above counts the agent's wallet AND the owner's treasury, because
		// both hold this user's spendable coins: a purchase credits the treasury,
		// match winnings credit the agent. bank.Hold only ever debits the agent, so
		// anything sitting in the treasury has to cross over first — otherwise the
		// hold fails against the ledger's non-negative balance constraint on a
		// request the entitlement check had just allowed, which is exactly the
		// "withdrawal refuses every amount" a user with only purchased coins hit.
		//
		// Inside the owner lock and before the hold, so it cannot interleave with a
		// sibling request. Idempotent on the withdrawal id.
		agentBal, err := s.repo.AgentBalance(ctx, agentPublicID)
		if err != nil {
			return err
		}
		if short := coins - agentBal; short > 0 {
			if err := s.bank.SweepFromTreasury(ctx, w.PublicID, owner, agentPublicID, short); err != nil {
				return err
			}
		}
		// Lock the coins first, then record the request. If recording fails, release.
		if err := s.bank.Hold(ctx, w.PublicID, agentPublicID, coins); err != nil {
			return err
		}
		if err := s.repo.Create(ctx, w); err != nil {
			_ = s.bank.Release(ctx, w.PublicID, agentPublicID, coins)
			return err
		}
		return nil
	}); err != nil {
		return Withdrawal{}, err
	}
	s.m.requested.Inc()
	s.audit(ctx, callerUserPublicID, "withdrawal_request", w.PublicID, map[string]any{"agent": agentPublicID, "coins": coins, "net_cents": w.NetCents})
	// The request is the moment the coins ACTUALLY LEAVE the spendable balance (the
	// Hold above), yet it was the one withdrawal transition that told the user
	// nothing — paid and failed both notified, requested did not. So the balance
	// dropped with no explanation attached to it, which reads as coins going missing
	// rather than coins being reserved for a payout in progress.
	s.notify(ctx, owner, "withdrawal_requested", w.PublicID, w.NetCents, w.Coins)
	// Two stages from one call, because they are two different facts: the request
	// was accepted, AND the coins actually left the spendable balance. When a user
	// asks "where did my credits go", the second is the answer.
	s.traceOK(ctx, w, paymenttrace.StageRequested, map[string]any{
		"agent": agentPublicID, "coins": w.Coins, "fee_coins": w.FeeCoins,
		"gross_cents": w.GrossCents, "net_cents": w.NetCents,
		"chain": w.Chain, "dest_wallet": w.DestWallet,
	})
	s.traceOK(ctx, w, paymenttrace.StageCoinsHeld, map[string]any{"coins": w.Coins})
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
	// Separation of duties (maker-checker): the admin approving a withdrawal must not
	// be its owner. A self-approval lets a compromised or insider admin cash out their
	// own balance with no second reviewer. Platform-token admins carry no user id
	// (adminUserID == ""), so they are always a distinct approver and are unaffected. (M2)
	if adminUserID != "" && adminUserID == w.Owner {
		return ErrSelfApproval
	}
	// Approval is where money actually leaves, so it is checked again here — not only
	// at request time. A queue of withdrawals accepted BEFORE the breaker tripped must
	// not drain afterwards just because each was individually approved earlier; that is
	// exactly the backlog an attacker would build.
	if err := s.checkBreaker(ctx, w.NetCents); err != nil {
		return err
	}
	if s.clock.Now().Sub(w.RequestedAt) < s.cfg.Clearing {
		return ErrClearing
	}
	if flagged, err := s.repo.AgentFlagged(ctx, w.Agent); err != nil {
		return err
	} else if flagged {
		return ErrFlagged
	}
	// LAST gate before the transfer is built: can the payout wallet actually settle
	// this? Deliberately after every policy check, so an approval that would have been
	// refused on policy grounds is still refused on those grounds — "we won't pay this"
	// and "we can't pay this yet" are different answers and the first is the truer one.
	if err := s.checkFunding(w); err != nil {
		return err
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
		// Nothing was sent and the hold was returned — say both, because "failed" on
		// its own reads as money lost rather than money given back.
		s.traceFailed(ctx, w, paymenttrace.StageBroadcastFailed,
			"the payout could not be sent; your credits were returned to your balance",
			map[string]any{"error": err.Error(), "coins_returned": w.Coins})
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
	s.notify(ctx, w.Owner, "withdrawal_paid", w.PublicID, w.NetCents, w.Coins)
	// Stripe settles at the API call, so approval, broadcast and payment are one
	// instant. All three are still recorded: the diagram is the same shape on both
	// rails, and a reader should not have to know which rail ran to read it.
	s.traceOK(ctx, w, paymenttrace.StageApproved, map[string]any{"admin": adminUserID})
	s.traceOK(ctx, w, paymenttrace.StageBroadcast, map[string]any{"transfer": transferID})
	s.traceOK(ctx, w, paymenttrace.StagePaid, map[string]any{"transfer": transferID, "net_cents": w.NetCents})
	s.traceOK(ctx, w, paymenttrace.StageWithdrawNotified, nil)
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
	s.traceOK(ctx, w, paymenttrace.StageApproved, map[string]any{"admin": adminUserID})
	// Record the signature and move 'processing' → 'broadcasted' via a pre-broadcast
	// hook, BEFORE the tx is actually sent (M10). This closes the crash window where a
	// send happened but the signature wasn't recorded yet: after the hook runs, the
	// row is 'broadcasted' with the signature, and the confirm watcher owns resolution
	// from the chain. Because a send is only attempted AFTER this hook, a row still in
	// 'processing' provably had NO broadcast — so it is safe to release on recovery.
	recorded := false
	onSigned := func(sig string) error {
		ok, e := s.repo.SetStatus(ctx, w.PublicID, "processing", "broadcasted", sig, "")
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("payout: could not record broadcasted state")
		}
		recorded = true
		return nil
	}

	var sendErr error
	if pc, ok := s.xfer.(preCommitTransferrer); ok {
		_, sendErr = pc.TransferPreCommit(ctx, w.DestWallet, w.NetCents, "wd:"+w.PublicID, onSigned)
	} else {
		// Fallback for a non-pre-commit transferrer: send then record (legacy order).
		// Still ambiguous-safe: on a broadcast-ambiguous error the signed tx MAY have
		// landed, so record 'broadcasted' from the surfaced signature (recorded=true)
		// and never release below.
		var sig string
		sig, sendErr = s.xfer.Transfer(ctx, w.DestWallet, w.NetCents, "wd:"+w.PublicID)
		if sendErr == nil {
			sendErr = onSigned(sig)
		} else if amb := (*BroadcastAmbiguousError)(nil); errors.As(sendErr, &amb) && amb.Signature != "" {
			_ = onSigned(amb.Signature)
		}
	}

	if sendErr != nil {
		if recorded {
			// The signature is persisted and the row is 'broadcasted': the tx MAY be in
			// flight (send RPC error is ambiguous). NEVER release here — that would
			// double-pay if it landed. The confirm watcher settles it from the chain.
			s.log.Warn("payout: solana send errored after signature recorded; holding 'broadcasted' for on-chain confirmation",
				"id", w.PublicID, "error", sendErr)
			s.audit(ctx, adminUserID, "withdrawal_broadcasted", w.PublicID, map[string]any{"ambiguous": true})
			// Deliberately PENDING, not failed. The signature is recorded and the
			// transfer may well have landed; calling this a failure would tell the
			// user their payout died while their USDC was in flight.
			s.trace.Pending(ctx, w.Owner, paymenttrace.FlowWithdrawal, w.PublicID,
				paymenttrace.StageBroadcast,
				"the send returned an error after the transaction was signed — the chain is being checked",
				map[string]any{"ambiguous": true, "error": sendErr.Error()})
			return sendErr
		}
		// Failure BEFORE the signature was recorded ⇒ nothing was ever broadcast ⇒
		// safe to release the hold and fail for re-request.
		_ = s.bank.Release(ctx, w.PublicID, w.Agent, w.Coins)
		_, _ = s.repo.SetStatus(ctx, w.PublicID, "processing", "failed", "", sendErr.Error())
		s.audit(ctx, adminUserID, "withdrawal_failed", w.PublicID, map[string]any{"error": sendErr.Error()})
		s.traceFailed(ctx, w, paymenttrace.StageBroadcastFailed,
			"nothing was broadcast and your credits were returned to your balance",
			map[string]any{"error": sendErr.Error(), "coins_returned": w.Coins})
		return sendErr
	}
	// Sent, and the row was already moved to 'broadcasted' by the hook. Coins stay
	// held until ConfirmBroadcasted sees the tx finalize.
	s.audit(ctx, adminUserID, "withdrawal_broadcasted", w.PublicID,
		map[string]any{"net_cents": w.NetCents, "wallet": w.DestWallet})
	s.traceOK(ctx, w, paymenttrace.StageBroadcast,
		map[string]any{"net_cents": w.NetCents, "dest_wallet": w.DestWallet})
	// The coins are still HELD at this point — finality is what burns them. Saying so
	// explicitly stops the next stage reading as an unexplained delay.
	s.trace.Pending(ctx, w.Owner, paymenttrace.FlowWithdrawal, w.PublicID,
		paymenttrace.StagePaid, "waiting for Solana to finalize the payout", nil)
	return nil
}

// preCommitTransferrer is an optional Transferrer capability (implemented by the
// Solana rail) that invokes a hook with the signature before broadcasting, so the
// service can durably record 'broadcasted' pre-send. (M10)
type preCommitTransferrer interface {
	TransferPreCommit(ctx context.Context, destination string, amountCents int64, idemKey string, onSigned func(signature string) error) (string, error)
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
			// Not yet finalized. A live Solana tx confirms within seconds; its blockhash
			// is only valid ~60-90s. Confirm searches the full transaction history, so a
			// tx still un-finalized well past that window (deadBroadcastWindow) can NEVER
			// land — its blockhash has expired. Safe to release the held escrow (no
			// double-pay risk: a landed tx would have been found and finalized). Recent
			// rows just keep waiting. (M10)
			if !w.StatusChangedAt.IsZero() && s.clock.Now().Sub(w.StatusChangedAt) > deadBroadcastWindow {
				if err := s.bank.Release(ctx, w.PublicID, w.Agent, w.Coins); err != nil {
					s.log.Error("payout: expired-broadcast release", "id", w.PublicID, "error", err)
					continue
				}
				if _, err := s.repo.SetStatus(ctx, w.PublicID, "broadcasted", "failed", w.TransferID, "expired: never confirmed on-chain (blockhash lapsed)"); err != nil {
					s.log.Error("payout: expired-broadcast status", "id", w.PublicID, "error", err)
					continue
				}
				s.log.Warn("payout: released a withdrawal whose broadcast never confirmed (blockhash expired)",
					"id", w.PublicID, "sig", w.TransferID, "coins", w.Coins)
				s.audit(ctx, "solana:confirm", "withdrawal_failed", w.PublicID, map[string]any{"signature": w.TransferID, "reason": "broadcast_expired"})
				// The user watched a payout sit "sent" for an hour and then revert.
				// Without a recorded reason that is indistinguishable from us losing
				// their money, so the reason is stated in their own timeline.
				s.traceFailed(ctx, w, paymenttrace.StageOnchainFailed,
					"the payout was broadcast but never confirmed and has now expired; your credits were returned",
					map[string]any{"signature": w.TransferID, "coins_returned": w.Coins})
				settled++
			}
			continue
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
			s.notify(ctx, w.Owner, "withdrawal_paid", w.PublicID, w.NetCents, w.Coins)
			s.traceOK(ctx, w, paymenttrace.StagePaid,
				map[string]any{"signature": w.TransferID, "net_cents": w.NetCents})
			s.traceOK(ctx, w, paymenttrace.StageWithdrawNotified, nil)
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
			s.notify(ctx, w.Owner, "withdrawal_failed", w.PublicID, w.NetCents, w.Coins)
			s.traceFailed(ctx, w, paymenttrace.StageOnchainFailed,
				"the transfer failed on Solana; your credits were returned to your balance",
				map[string]any{"signature": w.TransferID, "coins_returned": w.Coins})
		}
		settled++
	}
	return settled, nil
}

// stuckProcessingGrace is how long a row may sit in 'processing' before the
// reconciliation sweep treats it as crashed-pre-broadcast and releases it. It only
// needs to exceed a normal claim→sign→record span (sub-second), but is generous to
// avoid racing a slow-but-live approve.
const stuckProcessingGrace = 2 * time.Minute

// deadBroadcastWindow is how long a 'broadcasted' withdrawal may stay un-finalized
// before the confirm watcher concludes its blockhash lapsed and the tx can never
// land — then it safely releases the escrow. It must comfortably exceed a Solana
// blockhash's validity (~60-90s), so 3 minutes is conservative.
const deadBroadcastWindow = 3 * time.Minute

// ReconcileStuckProcessing recovers Solana withdrawals stranded in 'processing'.
// A withdrawal is only in 'processing' for the brief moment between the atomic
// claim and recording the broadcast signature (→ 'broadcasted'); a crash in that
// window leaves it stuck with escrow frozen and NO recorded signature, so the
// confirm watcher (which scans 'broadcasted') can't advance it. Because the state
// is normally sub-second, any 'processing' row observed by this periodic sweep is
// anomalous and needs operator reconciliation (look up the hot wallet's recent
// USDC transfers to w.DestWallet to decide paid-vs-release).
//
// This sweep only ALERTS — it never auto-releases, because without the signed
// transaction's signature we cannot know whether the USDC already went out, and a
// blind release would risk a double payout. Safe automated recovery requires
// persisting the signed-tx signature at claim time (a follow-up refactor). Returns
// the number of stuck rows found.
func (s *Service) ReconcileStuckProcessing(ctx context.Context) (int, error) {
	items, err := s.repo.ListByStatus(ctx, "processing", 100)
	if err != nil {
		return 0, err
	}
	now := s.clock.Now()
	recovered := 0
	for _, w := range items {
		// A row is in 'processing' only between the atomic claim and the pre-broadcast
		// signature record; the network send happens strictly AFTER it leaves
		// 'processing' (M10). So a row still 'processing' past the grace provably never
		// broadcast — releasing its escrow can't double-pay. (A crashed send would have
		// already advanced the row to 'broadcasted' with its signature, handled by the
		// confirm watcher, not here.)
		if now.Sub(w.StatusChangedAt) < stuckProcessingGrace {
			continue
		}
		claimed, err := s.repo.SetStatus(ctx, w.PublicID, "processing", "failed", "", "stuck: released on reconciliation (never broadcast)")
		if err != nil {
			s.log.Error("payout: stuck-processing status write", "id", w.PublicID, "error", err)
			continue
		}
		if !claimed {
			continue // advanced concurrently (e.g. the approve finally recorded broadcasted)
		}
		if err := s.bank.Release(ctx, w.PublicID, w.Agent, w.Coins); err != nil {
			s.log.Error("payout: stuck-processing release", "id", w.PublicID, "error", err)
			continue
		}
		s.log.Warn("payout: released a withdrawal stuck in 'processing' (never broadcast)",
			"id", w.PublicID, "owner", w.Owner, "agent", w.Agent, "coins", w.Coins)
		s.audit(ctx, "system:reconcile", "withdrawal_failed", w.PublicID, map[string]any{"reason": "stuck_processing_released"})
		recovered++
	}
	s.m.stuckProcessing.Set(float64(len(items) - recovered))
	return recovered, nil
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
	s.traceFailed(ctx, w, paymenttrace.StageRejected,
		"a reviewer declined this payout; your credits were returned to your balance",
		map[string]any{"reason": reason, "coins_returned": w.Coins})
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
	requested       prometheus.Counter
	paid            prometheus.Counter
	paidCents       prometheus.Counter
	reversed        prometheus.Counter
	stuckProcessing prometheus.Gauge
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		requested:       prometheus.NewCounter(prometheus.CounterOpts{Name: "withdrawals_requested_total", Help: "Cash-out requests filed."}),
		paid:            prometheus.NewCounter(prometheus.CounterOpts{Name: "withdrawals_paid_total", Help: "Cash-outs paid to a bank."}),
		paidCents:       prometheus.NewCounter(prometheus.CounterOpts{Name: "withdrawals_paid_cents_total", Help: "Total cents paid out to users."}),
		reversed:        prometheus.NewCounter(prometheus.CounterOpts{Name: "withdrawals_reversed_total", Help: "Payouts reversed by Stripe and re-credited."}),
		stuckProcessing: prometheus.NewGauge(prometheus.GaugeOpts{Name: "withdrawals_stuck_processing", Help: "Withdrawals stranded in 'processing' (escrow frozen; needs reconciliation)."}),
	}
	reg.MustRegister(m.requested, m.paid, m.paidCents, m.reversed, m.stuckProcessing)
	return m
}
