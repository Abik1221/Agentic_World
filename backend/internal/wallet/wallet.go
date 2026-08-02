package wallet

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/paymenttrace"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// Realtime event kinds emitted by this package. They mirror the notification
// `kind` values so the live toast and the persisted bell entry never disagree.
const (
	eventCoinsStaked     = "coins_staked"
	eventCoinsAllocated  = "coins_allocated"
	eventCoinsToppedUp   = "coins_topped_up"
	eventBalanceAdjusted = "balance_adjusted"
)

// Service implements both match.Wallet (Stake/Settle/Refund) and match.Limits
// (CheckJoin), and backs the `/v1/wallet` reads. A single instance is handed to
// the match service for both ports. Safe for concurrent use (stateless over its
// ledger + repo dependencies).
type Service struct {
	ledger ledgerPort
	repo   Repo
	clock  platform.Clock
	cfg    Config
	gate   PayoutGate
	events EventSink             // realtime balance push; nil ⇒ none
	trace  *paymenttrace.Service // payment-flow log; nil ⇒ none
	m      *metrics
}

// EventSink pushes a realtime "your balance just moved" signal to one user's open
// browser streams. Entirely optional: nil means no push, and NOTHING about the
// ledger, the API responses or the persisted notification feed changes. It is a
// display accelerator, never a source of truth.
type EventSink interface {
	// Go runs fn on a bounded background pool. Non-blocking; drops under saturation.
	// Publishers use it because resolving an agent's owner is a database round trip,
	// and no money path should pay for one to refresh a browser.
	Go(fn func(ctx context.Context))
	// Publish sends one TRANSIENT event to a user's streams. Best-effort, and it
	// leaves no record — for balance movements the user does not need to find again
	// later (a stake, a rebalance they just submitted).
	Publish(ctx context.Context, userPublicID, kind, ref string, payload map[string]any)
	// Notify writes a DURABLE notification and pushes it live, idempotent per
	// (user, kind, ref). For money that arrives on its own schedule — a card
	// top-up settling, a subscription grant, an admin adjustment — where "I never
	// saw a confirmation" must not be true five minutes later either.
	//
	// It returns an error because the payment trace records whether the user was
	// actually told, and a stage that claims "you were notified" without checking
	// is worse than no stage at all: it closes the one line of enquiry that would
	// have found the bug.
	Notify(ctx context.Context, userPublicID, kind, ref string, payload map[string]any) error
}

// SetEventSink installs the realtime push channel. Optional; see EventSink.
func (s *Service) SetEventSink(e EventSink) { s.events = e }

// signalUser pushes a transient balance signal, off the caller's goroutine.
func (s *Service) signalUser(userPublicID, kind, ref string, payload map[string]any) {
	if s.events == nil || userPublicID == "" {
		return
	}
	s.events.Go(func(ctx context.Context) {
		s.events.Publish(ctx, userPublicID, kind, ref, payload)
	})
}

// notifyUser records a durable notification and pushes it, off the caller's
// goroutine. Off-path because it writes to the database and the caller is a
// ledger transaction that must not be lengthened for a UI concern.
//
// `traceFlow` is optional: when set, the outcome of the notification is recorded
// as the flow's final stage — truthfully, after the write, so a payment whose
// confirmation never reached the user is visibly incomplete rather than silently
// marked done.
func (s *Service) notifyUser(userPublicID, kind, ref, traceFlow, traceStage string, payload map[string]any) {
	if s.events == nil || userPublicID == "" {
		return
	}
	s.events.Go(func(ctx context.Context) {
		err := s.events.Notify(ctx, userPublicID, kind, ref, payload)
		if traceFlow == "" {
			return
		}
		if err != nil {
			s.trace.Failed(ctx, userPublicID, traceFlow, ref, traceStage,
				"the credits landed but the confirmation could not be delivered",
				map[string]any{"error": err.Error()})
			return
		}
		s.trace.OK(ctx, userPublicID, traceFlow, ref, traceStage, nil)
	})
}

// SetTracer wires the payment-flow log. Optional; nil-safe and purely diagnostic.
func (s *Service) SetTracer(t *paymenttrace.Service) { s.trace = t }

// signalAgentOwners resolves each agent's owner and pushes to them. The lookup runs
// on the sink's pool, so a stake transaction never waits on it.
func (s *Service) signalAgentOwners(kind, ref string, payload map[string]any, agents ...string) {
	if s.events == nil {
		return
	}
	for _, agent := range agents {
		if agent == "" {
			continue
		}
		s.events.Go(func(ctx context.Context) {
			owner, err := s.repo.OwnerOf(ctx, agent)
			if err != nil || owner == "" {
				return
			}
			body := make(map[string]any, len(payload)+1)
			for k, v := range payload {
				body[k] = v
			}
			body["agent"] = agent
			s.events.Publish(ctx, owner, kind, ref+":"+agent, body)
		})
	}
}

// PayoutGate decides whether a match's settlement may pay out now. A false result
// means a hold is in force (escrow retained); the gate records the hold itself.
// Default: AllowAllGate. The anti-fraud service supplies the real gate (Stage 9).
type PayoutGate interface {
	Allow(ctx context.Context, matchPublicID string) (bool, error)
}

// AllowAllGate is the default no-op gate (every payout proceeds).
type AllowAllGate struct{}

func (AllowAllGate) Allow(context.Context, string) (bool, error) { return true, nil }

// New constructs the wallet service and registers its collectors on reg.
func New(l ledgerPort, repo Repo, clock platform.Clock, cfg Config, reg *prometheus.Registry) *Service {
	if cfg.SessionWindow <= 0 {
		cfg.SessionWindow = 6 * time.Hour
	}
	if cfg.CoinCents <= 0 {
		cfg.CoinCents = 1
	}
	return &Service{ledger: l, repo: repo, clock: clock, cfg: cfg, gate: AllowAllGate{}, m: newMetrics(reg)}
}

// SetPayoutGate installs the anti-fraud payout gate after construction. This
// breaks the wallet↔anti-fraud construction cycle (the gate needs the wallet as
// its settler, and the wallet needs the gate).
func (s *Service) SetPayoutGate(g PayoutGate) { s.gate = g }

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	staked         prometheus.Counter
	rake           prometheus.Counter
	heldPayouts    prometheus.Counter
	chargebackDebt prometheus.Counter
	limitBlock     *prometheus.CounterVec
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		staked: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "coins_staked_total",
			Help: "Total coins escrowed across all stakes.",
		}),
		rake: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rake_total",
			Help: "Total coins accrued to platform revenue as rake.",
		}),
		heldPayouts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "payout_holds_total",
			Help: "Settlements held by the anti-fraud payout gate.",
		}),
		chargebackDebt: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "chargeback_debt_coins_total",
			Help: "Coins booked as un-recovered chargeback debt.",
		}),
		limitBlock: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "limit_block_total",
			Help: "Joins blocked by a server-enforced spending limit, by limit name.",
		}, []string{"limit"}),
	}
	reg.MustRegister(m.staked, m.rake, m.heldPayouts, m.chargebackDebt, m.limitBlock)
	return m
}
