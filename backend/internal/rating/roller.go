package rating

import (
	"context"
	"log/slog"
	"time"
)

// SeasonRoller finalises completed seasons on an interval: it detects when the
// date-derived season advances and rolls every not-yet-finalised prior season
// (recording it + emitting season.rolled with the champion). Idempotent and safe
// to run on every instance — the DB roll row is the guard. Mirrors the other
// background loops (single goroutine, ticks until ctx is cancelled).
type SeasonRoller struct {
	svc      *Service
	log      *slog.Logger
	interval time.Duration
}

// NewSeasonRoller builds the loop. interval<=0 defaults to 1 minute (seasons are
// long; a coarse tick is plenty and cheap).
func NewSeasonRoller(svc *Service, log *slog.Logger, interval time.Duration) *SeasonRoller {
	if interval <= 0 {
		interval = time.Minute
	}
	return &SeasonRoller{svc: svc, log: log, interval: interval}
}

// Run rolls completed seasons until ctx is cancelled. It rolls once immediately
// so a restart after a boundary finalises promptly.
func (r *SeasonRoller) Run(ctx context.Context) {
	r.log.Info("season roller started", "interval", r.interval.String())
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		if err := r.svc.RollCompleted(ctx); err != nil {
			r.log.Error("season roll failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
