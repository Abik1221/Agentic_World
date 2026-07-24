package monopoly

import (
	"context"
	"log/slog"
	"time"
)

// Sweeper drives move-window timeouts for active Monopoly tables: an agent that
// misses its deadline is auto-played with the engine's deterministic default so
// a stalled human can never wedge a table.
type Sweeper struct {
	svc   *Service
	log   *slog.Logger
	every time.Duration
}

func NewSweeper(svc *Service, log *slog.Logger, every time.Duration) *Sweeper {
	if every <= 0 {
		every = time.Second
	}
	return &Sweeper{svc: svc, log: log, every: every}
}

func (sw *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(sw.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := sw.svc.SweepExpired(ctx, 32)
			if err != nil {
				sw.log.Warn("monopoly sweeper", "error", err)
			} else if n > 0 {
				sw.log.Debug("monopoly sweeper processed", "matches", n)
			}
			// Also abort staked waiting tables that never reached TargetPlayers, so an
			// agent isn't stuck in a lobby that can't fill.
			if w, werr := sw.svc.SweepStaleWaiting(ctx, 64); werr != nil {
				sw.log.Warn("monopoly sweeper (stale waiting)", "error", werr)
			} else if w > 0 {
				sw.log.Debug("monopoly sweeper aborted stale waiting tables", "tables", w)
			}
		}
	}
}
