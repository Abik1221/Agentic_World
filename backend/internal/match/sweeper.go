package match

import (
	"context"
	"log/slog"
	"time"
)

// Sweeper periodically advances matches whose move window has expired, forcing a
// deterministic random card for any seat that failed to act. It is safe to run on
// every instance concurrently: each match is processed under its per-match lock,
// so exactly one instance progresses a given match at a time.
type Sweeper struct {
	svc      *Service
	log      *slog.Logger
	interval time.Duration
	batch    int
}

func NewSweeper(svc *Service, log *slog.Logger, interval time.Duration) *Sweeper {
	if interval <= 0 {
		interval = time.Second
	}
	return &Sweeper{svc: svc, log: log, interval: interval, batch: 200}
}

// Run blocks until ctx is cancelled, sweeping expired matches each tick.
func (s *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	s.log.Info("match sweeper started", "interval", s.interval.String())
	for {
		select {
		case <-ctx.Done():
			s.log.Info("match sweeper stopped")
			return
		case <-t.C:
			if n, err := s.svc.SweepExpired(ctx, s.batch); err != nil {
				s.log.Error("sweep error", "error", err)
			} else if n > 0 {
				s.log.Debug("swept expired matches", "count", n)
			}
		}
	}
}
