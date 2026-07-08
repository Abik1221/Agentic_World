package walletrecon

import (
	"context"
	"time"
)

// Worker runs reconciliation on a ticker (immediately, then every interval).
type Worker struct {
	svc      *Service
	interval time.Duration
}

// NewWorker builds the reconciliation worker; interval defaults to 1h.
func NewWorker(svc *Service, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = time.Hour
	}
	return &Worker{svc: svc, interval: interval}
}

// Run reconciles once immediately, then every interval, until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	w.tick(ctx)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.svc.log.Error("walletrecon worker panic", "recover", r)
		}
	}()
	if _, err := w.svc.Reconcile(ctx); err != nil {
		w.svc.log.Warn("walletrecon", "error", err)
	}
}
