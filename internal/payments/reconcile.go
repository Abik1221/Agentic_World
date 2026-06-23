package payments

import (
	"context"
	"time"
)

// Reconciler runs Stripe↔ledger reconciliation on a fixed cadence (hourly by
// default), replaying any missed or failed webhooks. Safe to run on every
// instance — every coin move is idempotent.
type Reconciler struct {
	svc      *Service
	interval time.Duration
}

// NewReconciler builds the periodic reconciliation job.
func (s *Service) NewReconciler(interval time.Duration) *Reconciler {
	if interval <= 0 {
		interval = time.Hour
	}
	return &Reconciler{svc: s, interval: interval}
}

// Run blocks until ctx is cancelled, reconciling on each tick.
func (rc *Reconciler) Run(ctx context.Context) {
	rc.svc.log.Info("payments reconciler started", "interval", rc.interval.String())
	t := time.NewTicker(rc.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			rc.svc.log.Info("payments reconciler stopped")
			return
		case <-t.C:
			if n, err := rc.svc.Reconcile(ctx); err != nil {
				rc.svc.log.Error("payments reconcile error", "error", err)
			} else if n > 0 {
				rc.svc.log.Info("payments reconciled", "count", n)
			}
		}
	}
}
