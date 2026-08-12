package match

import (
	"context"
	"log/slog"
	"time"
)

// ReadySweeper drives tables sitting in ready_check toward a decision.
//
// Without it a paired table would wait forever: readiness is collected by agents calling
// /ready, but nothing else notices that a window expired, that a seat is out of asks, or that
// everyone has answered and the table should now escrow and start. This is the only thing that
// moves a table OUT of ready_check, which is why CreatePaired must not create one until this
// is running.
//
// Runs FAST — the ready window is measured in seconds, not minutes, and a developer watching a
// terminal is waiting on it. That is the opposite trade-off from the queue-orphan sweep, which
// runs every minute because nothing is blocked on it.
type ReadySweeper struct {
	svc    *Service
	lister ReadyLister
	log    *slog.Logger
	every  time.Duration
	batch  int
}

// ReadyLister finds tables to drive. Satisfied by *store.MatchRepo.
type ReadyLister interface {
	ReadyCheckMatches(ctx context.Context, limit int) ([]string, error)
}

func NewReadySweeper(svc *Service, lister ReadyLister, log *slog.Logger, every time.Duration) *ReadySweeper {
	if every <= 0 {
		every = time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &ReadySweeper{svc: svc, lister: lister, log: log, every: every, batch: 32}
}

func (sw *ReadySweeper) Run(ctx context.Context) {
	t := time.NewTicker(sw.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sw.tick(ctx)
		}
	}
}

func (sw *ReadySweeper) tick(ctx context.Context) {
	ids, err := sw.lister.ReadyCheckMatches(ctx, sw.batch)
	if err != nil {
		sw.log.Warn("ready sweeper: could not list tables", "error", err)
		return
	}
	for _, id := range ids {
		// One table's failure must not stop the rest. A stuck table holds its own agents;
		// aborting the sweep would hold everyone's, which turns one bad row into an outage.
		if _, err := sw.svc.ReadyTick(ctx, id); err != nil {
			sw.log.Warn("ready sweeper: tick failed", "match", id, "error", err)
		}
	}
}
