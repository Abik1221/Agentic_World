package antifraud

import (
	"context"
	"time"
)

// Detector runs the periodic collusion/timing sweep. Safe to run on every
// instance: flags are recorded idempotently (NOT EXISTS guard in the store).
type Detector struct {
	svc      *Service
	interval time.Duration
}

// NewDetector builds the periodic detection job.
func (s *Service) NewDetector(interval time.Duration) *Detector {
	if interval <= 0 {
		interval = time.Hour
	}
	return &Detector{svc: s, interval: interval}
}

// Run blocks until ctx is cancelled, sweeping on each tick.
func (d *Detector) Run(ctx context.Context) {
	d.svc.log.Info("antifraud detector started", "interval", d.interval.String())
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			d.svc.log.Info("antifraud detector stopped")
			return
		case <-t.C:
			if err := d.svc.RunDetection(ctx); err != nil {
				d.svc.log.Error("antifraud detection error", "error", err)
			}
		}
	}
}
