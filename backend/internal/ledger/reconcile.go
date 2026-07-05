package ledger

import (
	"context"
	"log/slog"
	"time"
)

// Reconciler is the books-balance safety net: on each tick it asserts
// wallet.balance == Σ entries for every wallet and loudly flags any drift. It
// never auto-corrects — drift means a bug, and the response is freeze + page
// (see docs/architecture/security.md, "never auto-correct").
type Reconciler struct {
	svc      *Service
	log      *slog.Logger
	interval time.Duration
}

// NewReconciler builds the periodic reconciliation job.
func (s *Service) NewReconciler(log *slog.Logger, interval time.Duration) *Reconciler {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &Reconciler{svc: s, log: log, interval: interval}
}

// Run blocks until ctx is cancelled, reconciling once immediately and then on
// every tick.
func (rc *Reconciler) Run(ctx context.Context) {
	rc.log.Info("ledger reconciler started", "interval", rc.interval.String())
	if _, err := rc.RunOnce(ctx); err != nil {
		rc.log.Error("reconcile error", "error", err)
	}
	t := time.NewTicker(rc.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			rc.log.Info("ledger reconciler stopped")
			return
		case <-t.C:
			if _, err := rc.RunOnce(ctx); err != nil {
				rc.log.Error("reconcile error", "error", err)
			}
		}
	}
}

// RunOnce performs a single reconciliation pass and returns the number of
// drifted wallets (0 == healthy). Each drift increments the imbalance metric.
func (rc *Reconciler) RunOnce(ctx context.Context) (int, error) {
	drifts, err := rc.svc.repo.Reconcile(ctx)
	if err != nil {
		return 0, err
	}
	for _, d := range drifts {
		rc.svc.m.imbalance.Inc()
		rc.log.Error("LEDGER DRIFT DETECTED — freeze and investigate",
			"wallet_id", d.WalletID, "kind", d.Kind,
			"cached_balance", d.Balance, "entry_sum", d.EntrySum,
			"delta", d.Balance-d.EntrySum)
	}
	if len(drifts) == 0 {
		rc.log.Debug("ledger reconciled clean")
	}
	return len(drifts), nil
}
