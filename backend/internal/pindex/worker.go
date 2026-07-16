package pindex

import (
	"context"
	"log/slog"
	"time"
)

// Worker drains the P-Index dirty set on an interval, recomputing affected
// developers and refreshing the season ranking. Single goroutine, ticks until ctx is
// cancelled — mirrors the other background loops (season roller, sweepers). Safe to
// run on every instance: recompute is idempotent and the dirty set is claimed
// per-row.
type Worker struct {
	svc      *Service
	log      *slog.Logger
	interval time.Duration
	batch    int
}

// NewWorker builds the loop. interval<=0 defaults to 5s (recompute is cheap and we
// want a developer's P-Index to move within seconds of a match).
func NewWorker(svc *Service, log *slog.Logger, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Worker{svc: svc, log: log, interval: interval, batch: 100}
}

// Run recomputes dirty developers until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("pindex recompute worker started", "interval", w.interval.String())
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		if err := w.svc.RunRecompute(ctx, w.batch); err != nil {
			w.log.Error("pindex recompute batch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
