package payout

import (
	"context"
	"log/slog"
	"time"
)

// ConfirmWatcher drives Service.ConfirmBroadcasted on a ticker: it finalizes
// broadcast Solana withdrawals (burn on success, release on failure). Run it in
// its own goroutine (go w.Run(ctx)); no-op if no confirmer is set.
type ConfirmWatcher struct {
	svc   *Service
	log   *slog.Logger
	every time.Duration
}

// NewConfirmWatcher builds the watcher; `every` defaults to 15s.
func NewConfirmWatcher(svc *Service, log *slog.Logger, every time.Duration) *ConfirmWatcher {
	if every <= 0 {
		every = 15 * time.Second
	}
	return &ConfirmWatcher{svc: svc, log: log, every: every}
}

// Run confirms broadcast withdrawals until ctx is cancelled. A panic in a single
// pass is recovered so the worker survives a malformed RPC response.
func (w *ConfirmWatcher) Run(ctx context.Context) {
	t := time.NewTicker(w.every)
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

func (w *ConfirmWatcher) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("payout confirm watcher panic", "recover", r)
		}
	}()
	n, err := w.svc.ConfirmBroadcasted(ctx)
	if err != nil {
		w.log.Warn("payout confirm watcher", "error", err)
		return
	}
	if n > 0 {
		w.log.Debug("payout confirm watcher settled", "count", n)
	}
	// Recover any withdrawal stranded in 'processing' — provably pre-broadcast, so its
	// escrow is safely released back to the user (M10).
	if recovered, err := w.svc.ReconcileStuckProcessing(ctx); err != nil {
		w.log.Warn("payout stuck-processing sweep", "error", err)
	} else if recovered > 0 {
		w.log.Warn("payout: recovered stuck 'processing' withdrawals (released)", "count", recovered)
	}
}
