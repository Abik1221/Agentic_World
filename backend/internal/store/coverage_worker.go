package store

import (
	"context"
	"log/slog"
	"time"
)

// CoverageWorker keeps agent_match_coverage current.
//
// It runs often and does little: each tick advances the watermark by at most one batch, so a cold
// database with months of history backfills over many ticks instead of attempting 23 GB in one
// statement and being killed partway with the watermark unmoved.
type CoverageWorker struct {
	repo  *CoverageRepo
	every time.Duration
	batch time.Duration
	log   *slog.Logger
}

// NewCoverageWorker builds the worker. batch is how much match-finish time one tick may absorb.
func NewCoverageWorker(repo *CoverageRepo, every, batch time.Duration, log *slog.Logger) *CoverageWorker {
	if every <= 0 {
		every = time.Minute
	}
	if batch <= 0 {
		batch = 24 * time.Hour
	}
	return &CoverageWorker{repo: repo, every: every, batch: batch, log: log}
}

func (w *CoverageWorker) Run(ctx context.Context) {
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		// Once immediately, so a fresh deploy starts closing the gap rather than waiting a tick.
		res, err := w.repo.Refresh(ctx, w.batch)
		switch {
		case err != nil:
			// Logged, never fatal. A stale rollup renders coverage as unknown, which the board
			// already handles; a crashed worker would leave it stale forever.
			w.log.Warn("coverage rollup refresh failed", "error", err)
		case res.Seats > 0:
			w.log.Info("coverage rollup advanced",
				"seats", res.Seats, "through", res.Through, "backfill", res.FullRebuild)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
