package autoplay

import (
	"context"
	"time"
)

// Ticker runs Service.Tick on an interval until the context is cancelled. It
// mirrors the arena's other background loops (matchmaker, sweepers) so it plugs
// into the same launch(...) supervisor in cmd/server.
type Ticker struct {
	svc      *Service
	interval time.Duration
}

func NewTicker(svc *Service, interval time.Duration) *Ticker {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Ticker{svc: svc, interval: interval}
}

// Run blocks, reconciling every interval, until ctx is done.
func (t *Ticker) Run(ctx context.Context) {
	tk := time.NewTicker(t.interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			t.svc.Tick(ctx)
		}
	}
}
