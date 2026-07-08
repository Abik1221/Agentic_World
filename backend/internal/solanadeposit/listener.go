package solanadeposit

import (
	"context"
	"log/slog"
	"time"
)

// Listener drives Service.Poll on a ticker: an independent worker that watches
// the chain for incoming deposits, credits them, and expires stale sessions. It
// never blocks the request path. Run it in its own goroutine (go l.Run(ctx)).
type Listener struct {
	svc   *Service
	log   *slog.Logger
	every time.Duration
}

// NewListener builds the deposit listener. `every` defaults to 15s.
func NewListener(svc *Service, log *slog.Logger, every time.Duration) *Listener {
	if every <= 0 {
		every = 15 * time.Second
	}
	return &Listener{svc: svc, log: log, every: every}
}

// Run polls until ctx is cancelled. A panic in a single pass is recovered so the
// worker survives a malformed RPC response.
func (l *Listener) Run(ctx context.Context) {
	t := time.NewTicker(l.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.tick(ctx)
		}
	}
}

func (l *Listener) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("deposit listener panic", "recover", r)
		}
	}()
	n, err := l.svc.Poll(ctx)
	if err != nil {
		l.log.Warn("deposit listener", "error", err)
		return
	}
	if n > 0 {
		l.log.Debug("deposit listener credited", "count", n)
	}
}
