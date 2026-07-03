package wallet

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
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
	m      *metrics
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
	staked        prometheus.Counter
	rake          prometheus.Counter
	heldPayouts   prometheus.Counter
	chargebackDebt prometheus.Counter
	limitBlock    *prometheus.CounterVec
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
