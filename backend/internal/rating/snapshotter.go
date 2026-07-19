package rating

import (
	"context"
	"log/slog"
	"time"
)

// RankSnapshotter records a daily rank snapshot per (game, season, agent) so the
// leaderboard can show a rank trend (movement since the last snapshot). Idempotent
// per day (the DB UNIQUE(taken_on) key is the guard), so it is safe to run on
// every instance and to tick more often than daily. Mirrors the other background
// loops (single goroutine, ticks until ctx is cancelled).
type RankSnapshotter struct {
	svc      *Service
	log      *slog.Logger
	interval time.Duration
}

// NewRankSnapshotter builds the loop. interval<=0 defaults to 6h — a coarse tick
// is plenty since only the first write per day sticks (the rest no-op on conflict).
func NewRankSnapshotter(svc *Service, log *slog.Logger, interval time.Duration) *RankSnapshotter {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return &RankSnapshotter{svc: svc, log: log, interval: interval}
}

// Run snapshots ranks until ctx is cancelled, once immediately on start.
func (w *RankSnapshotter) Run(ctx context.Context) {
	w.log.Info("rank snapshotter started", "interval", w.interval.String())
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		if n, err := w.svc.SnapshotRanks(ctx); err != nil {
			w.log.Error("rank snapshot failed", "error", err)
		} else if n > 0 {
			w.log.Info("rank snapshot written", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
