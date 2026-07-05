package mafia

import (
	"context"
	"log/slog"
	"time"
)

// Sweeper drives phase timeouts for active Mafia tables.
type Sweeper struct {
	svc  *Service
	log  *slog.Logger
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
				sw.log.Warn("mafia sweeper", "error", err)
			} else if n > 0 {
				sw.log.Debug("mafia sweeper processed", "matches", n)
			}
		}
	}
}
