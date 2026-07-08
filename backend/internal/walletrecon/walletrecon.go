// Package walletrecon is the Solana wallet-pipeline safety net (Beta wallet
// pipeline P6). On each tick it cross-checks the on-chain records against the
// double-entry ledger and flags drift — it NEVER auto-corrects, because drift
// means a bug and the correct response is alert + investigate (mirrors
// ledger.Reconciler). It checks three things:
//
//   - every credited on-chain deposit has a matching ledger top-up (no orphaned
//     credit, and total coins credited == ledger-side coins);
//   - no withdrawal is stuck 'broadcasted' (sent but never confirmed) past a
//     threshold — needs on-chain reconciliation;
//   - no withdrawal is stuck 'processing' (broadcast recorded-status write failed)
//     past a threshold — needs manual reconciliation.
package walletrecon

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Report is one reconciliation pass result.
type Report struct {
	Deposits          int64 // credited on-chain deposits
	DepositCoins      int64 // Σ coins recorded in solana_deposits
	LedgerSolanaCoins int64 // Σ coins credited to user wallets via solana top-ups
	OrphanDeposits    int64 // credited deposits with NO matching ledger top-up (CRITICAL)
	CoinDrift         int64 // DepositCoins − LedgerSolanaCoins (must be 0)
	StuckBroadcasted  int64 // solana withdrawals broadcast but unconfirmed past threshold
	StuckProcessing   int64 // solana withdrawals stuck mid-broadcast past threshold
}

// Healthy reports whether the pass found no drift or stuck items.
func (r Report) Healthy() bool {
	return r.OrphanDeposits == 0 && r.CoinDrift == 0 && r.StuckBroadcasted == 0 && r.StuckProcessing == 0
}

// Repo is the read-only reconciliation data port (pgx impl in internal/store).
type Repo interface {
	// DepositTotals returns count + Σcoins of credited on-chain deposits, and the
	// number with no matching ledger top-up (orphans).
	DepositTotals(ctx context.Context) (count, coins, orphans int64, err error)
	// LedgerSolanaCoins returns Σ coins credited to user wallets via solana top-ups.
	LedgerSolanaCoins(ctx context.Context) (int64, error)
	// StuckWithdrawals returns solana withdrawals in `status` older than the cutoff.
	StuckWithdrawals(ctx context.Context, status string, olderThan time.Duration) (int64, error)
}

// Service runs reconciliation.
type Service struct {
	repo       Repo
	log        *slog.Logger
	stuckAfter time.Duration
	m          *metrics
}

// New builds the service. stuckAfter defaults to 1h.
func New(repo Repo, stuckAfter time.Duration, log *slog.Logger, reg *prometheus.Registry) *Service {
	if stuckAfter <= 0 {
		stuckAfter = time.Hour
	}
	return &Service{repo: repo, log: log, stuckAfter: stuckAfter, m: newMetrics(reg)}
}

// Reconcile runs one pass, updates metrics, and loudly logs any drift/stuck items.
func (s *Service) Reconcile(ctx context.Context) (Report, error) {
	count, coins, orphans, err := s.repo.DepositTotals(ctx)
	if err != nil {
		return Report{}, err
	}
	ledgerCoins, err := s.repo.LedgerSolanaCoins(ctx)
	if err != nil {
		return Report{}, err
	}
	stuckB, err := s.repo.StuckWithdrawals(ctx, "broadcasted", s.stuckAfter)
	if err != nil {
		return Report{}, err
	}
	stuckP, err := s.repo.StuckWithdrawals(ctx, "processing", s.stuckAfter)
	if err != nil {
		return Report{}, err
	}
	r := Report{
		Deposits: count, DepositCoins: coins, LedgerSolanaCoins: ledgerCoins,
		OrphanDeposits: orphans, CoinDrift: coins - ledgerCoins,
		StuckBroadcasted: stuckB, StuckProcessing: stuckP,
	}

	if s.m != nil {
		s.m.orphanDeposits.Set(float64(r.OrphanDeposits))
		s.m.coinDrift.Set(float64(r.CoinDrift))
		s.m.stuckBroadcasted.Set(float64(r.StuckBroadcasted))
		s.m.stuckProcessing.Set(float64(r.StuckProcessing))
	}

	switch {
	case r.OrphanDeposits > 0 || r.CoinDrift != 0:
		// Money-integrity drift: page-worthy. Never auto-correct.
		s.log.Error("walletrecon: DRIFT DETECTED — investigate immediately",
			"orphan_deposits", r.OrphanDeposits, "coin_drift", r.CoinDrift,
			"deposit_coins", r.DepositCoins, "ledger_coins", r.LedgerSolanaCoins)
	case r.StuckBroadcasted > 0 || r.StuckProcessing > 0:
		s.log.Warn("walletrecon: stuck withdrawals need reconciliation",
			"broadcasted", r.StuckBroadcasted, "processing", r.StuckProcessing)
	default:
		s.log.Debug("walletrecon: clean", "deposits", r.Deposits, "coins", r.DepositCoins)
	}
	return r, nil
}

type metrics struct {
	orphanDeposits   prometheus.Gauge
	coinDrift        prometheus.Gauge
	stuckBroadcasted prometheus.Gauge
	stuckProcessing  prometheus.Gauge
}

func newMetrics(reg *prometheus.Registry) *metrics {
	if reg == nil {
		return nil
	}
	m := &metrics{
		orphanDeposits:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "wallet_recon_orphan_deposits", Help: "Credited on-chain deposits with no ledger top-up."}),
		coinDrift:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "wallet_recon_coin_drift", Help: "Deposit coins minus ledger-credited coins (should be 0)."}),
		stuckBroadcasted: prometheus.NewGauge(prometheus.GaugeOpts{Name: "wallet_recon_stuck_broadcasted", Help: "Solana withdrawals broadcast but unconfirmed past threshold."}),
		stuckProcessing:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "wallet_recon_stuck_processing", Help: "Solana withdrawals stuck mid-broadcast past threshold."}),
	}
	reg.MustRegister(m.orphanDeposits, m.coinDrift, m.stuckBroadcasted, m.stuckProcessing)
	return m
}
