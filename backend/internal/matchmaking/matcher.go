package matchmaking

import (
	"context"
	"time"
)

// Matcher is the background pairing loop. On each tick it scans the waiting queue,
// groups by bid, and greedily pairs compatible agents (different owner, ratings
// within a wait-widened band, oldest first). It mirrors the match sweeper's shape:
// a single goroutine, safe to run on every instance — each pairing first does an
// atomic ClaimPair (waiting -> claimed, all-or-nothing, row-locked) and only then
// escrows via CreatePaired, so two instances racing the same waiting snapshot can
// never both escrow the same pair (the loser's ClaimPair returns false).
type Matcher struct {
	svc      *Service
	interval time.Duration
}

// NewMatcher builds the loop from the service's configured interval.
func (s *Service) NewMatcher() *Matcher {
	return &Matcher{svc: s, interval: s.cfg.Interval}
}

// Run ticks until ctx is cancelled.
func (m *Matcher) Run(ctx context.Context) {
	m.svc.log.Info("matchmaker started", "interval", m.interval.String())
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.tick(ctx); err != nil {
				m.svc.log.Error("matchmaker tick failed", "error", err)
			}
		}
	}
}

// tick pairs everyone it can in one pass.
func (m *Matcher) tick(ctx context.Context) error {
	entries, err := m.svc.repo.Waiting(ctx, 1000)
	if err != nil {
		return err
	}
	m.svc.m.depth.Set(float64(len(entries)))
	now := m.svc.clock.Now()

	// Group by bid (entries arrive ordered by bid, then enqueued_at).
	byBid := map[int64][]Entry{}
	for _, e := range entries {
		byBid[e.Bid] = append(byBid[e.Bid], e)
	}
	for _, pool := range byBid {
		m.pairPool(ctx, pool, now)
	}
	return nil
}

// pairPool greedily pairs within one bid pool. For each still-unpaired entry
// (oldest first), it takes the earliest later entry with a different owner whose
// rating is within the band allowed by the longer-waiting of the two.
func (m *Matcher) pairPool(ctx context.Context, pool []Entry, now time.Time) {
	used := make([]bool, len(pool))
	for i := 0; i < len(pool); i++ {
		if used[i] {
			continue
		}
		a := pool[i]
		for j := i + 1; j < len(pool); j++ {
			if used[j] {
				continue
			}
			b := pool[j]
			if a.OwnerPublicID == b.OwnerPublicID {
				continue // never pair an owner against themselves
			}
			if !withinBand(a, b, now, m.svc.cfg) {
				continue
			}
			// Reserve BOTH queue rows before escrowing any stake. If the claim
			// doesn't land (another tick/instance already took one of them), skip
			// to the next candidate — never escrow on an unclaimed pair.
			claimed, err := m.svc.repo.ClaimPair(ctx, a.AgentPublicID, b.AgentPublicID)
			if err != nil {
				m.svc.log.Error("matchmaker claim failed", "a", a.AgentPublicID, "b", b.AgentPublicID, "error", err)
				continue
			}
			if !claimed {
				continue
			}
			matchID, err := m.svc.pairer.CreatePaired(ctx, a.AgentPublicID, a.OwnerPublicID, b.AgentPublicID, b.OwnerPublicID, a.Bid)
			if err != nil {
				// One side can't currently afford/limits/eligibility — release the
				// claim so both re-enter the pool and are retried next tick.
				if rerr := m.svc.repo.ReleasePair(ctx, a.AgentPublicID, b.AgentPublicID); rerr != nil {
					m.svc.log.Error("matchmaker release failed", "a", a.AgentPublicID, "b", b.AgentPublicID, "error", rerr)
				}
				m.svc.log.Warn("matchmaker pairing failed", "a", a.AgentPublicID, "b", b.AgentPublicID, "error", err)
				continue
			}
			if err := m.svc.repo.MarkMatched(ctx, a.AgentPublicID, b.AgentPublicID, matchID); err != nil {
				// The match exists and both stakes are escrowed; the rows stay
				// 'claimed' (not re-paired — Waiting() only returns 'waiting'), so
				// there is no double escrow. The agents just won't read match_id
				// from the queue until reconciliation. Log loudly.
				m.svc.log.Error("matchmaker mark-matched failed (match live, rows left claimed)", "match", matchID, "error", err)
			}
			m.svc.m.paired.Inc()
			used[i], used[j] = true, true
			break
		}
	}
}

// withinBand reports whether two entries' ratings are close enough to pair right
// now. The allowed band widens with the LONGER wait of the two, so a long-waiting
// agent can be matched against a fresher one (whichever side has waited longer
// relaxes the requirement).
func withinBand(a, b Entry, now time.Time, cfg Config) bool {
	wait := now.Sub(a.EnqueuedAt)
	if w := now.Sub(b.EnqueuedAt); w > wait {
		wait = w
	}
	steps := 0
	if cfg.StepInterval > 0 {
		steps = int(wait / cfg.StepInterval)
	}
	band := cfg.BaseBand + steps*cfg.BandStep
	if band > cfg.MaxBand {
		band = cfg.MaxBand
	}
	d := a.Elo - b.Elo
	if d < 0 {
		d = -d
	}
	return d <= band
}
