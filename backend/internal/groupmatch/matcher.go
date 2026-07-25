package groupmatch

import (
	"context"
	"time"
)

// Matcher is the background pooling loop. On each tick, for every grouped game it
// scans the waiting queue, groups by bid, and greedily forms full distinct-owner
// groups whose ratings sit within a wait-widened band (anchored on the oldest
// waiter). It mirrors the 2-player matcher's safety: ClaimGroup (waiting -> claimed,
// all-or-nothing) happens BEFORE any table is created/escrowed, so two instances
// racing the same snapshot can never both create a table for the same agents.
type Matcher struct {
	svc      *Service
	interval time.Duration
}

func (s *Service) NewMatcher() *Matcher {
	return &Matcher{svc: s, interval: s.cfg.Interval}
}

func (m *Matcher) Run(ctx context.Context) {
	m.svc.log.Info("group matchmaker started", "interval", m.interval.String())
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.tick(ctx); err != nil {
				m.svc.log.Error("group matchmaker tick failed", "error", err)
			}
		}
	}
}

// tick forms every group it can, per game, in one pass.
func (m *Matcher) tick(ctx context.Context) error {
	now := m.svc.clock.Now()
	var depth int
	for game := range m.svc.creators {
		entries, err := m.svc.repo.WaitingByGame(ctx, game, 1000)
		if err != nil {
			return err
		}
		depth += len(entries)
		target := m.svc.creators[game].SeatTarget()
		if target < 2 {
			continue // misconfigured game — never try to form a 0/1-seat table
		}
		// Group by bid; entries arrive ordered by (bid, enqueued_at).
		byBid := map[int64][]Entry{}
		for _, e := range entries {
			byBid[e.Bid] = append(byBid[e.Bid], e)
		}
		for _, pool := range byBid {
			m.formGroups(ctx, game, pool, target, now)
		}
	}
	m.svc.m.depth.Set(float64(depth))
	return nil
}

// formGroups greedily forms full groups within one (game, bid) pool. For each still-
// unused anchor (oldest first) it collects the earliest later entries with distinct
// owners whose rating is within the anchor's wait-widened band, until it has `target`
// members, then claims + creates the table.
func (m *Matcher) formGroups(ctx context.Context, game string, pool []Entry, target int, now time.Time) {
	used := make([]bool, len(pool))
	for i := 0; i < len(pool); i++ {
		if used[i] {
			continue
		}
		anchor := pool[i]
		band := bandFor(anchor, now, m.svc.cfg)
		group := []int{i}
		owners := map[string]bool{anchor.OwnerPublicID: true}
		for j := i + 1; j < len(pool) && len(group) < target; j++ {
			if used[j] {
				continue
			}
			c := pool[j]
			if owners[c.OwnerPublicID] {
				continue // never seat two agents of the same owner at one table
			}
			if abs(c.Elo-anchor.Elo) > band {
				continue
			}
			group = append(group, j)
			owners[c.OwnerPublicID] = true
		}
		if len(group) < target {
			continue // not enough compatible distinct-owner agents yet — wait for more
		}

		agentIDs := make([]string, len(group))
		seats := make([]Seat, len(group))
		for k, idx := range group {
			agentIDs[k] = pool[idx].AgentPublicID
			seats[k] = Seat{AgentPublicID: pool[idx].AgentPublicID, OwnerPublicID: pool[idx].OwnerPublicID}
		}

		// Reserve the whole group BEFORE creating/escrowing any table. If the claim
		// doesn't land (another tick/instance took one), skip — never create on an
		// unclaimed group.
		claimed, err := m.svc.repo.ClaimGroup(ctx, agentIDs)
		if err != nil {
			m.svc.log.Error("groupmatch claim failed", "game", game, "error", err)
			continue
		}
		if !claimed {
			continue
		}
		matchID, err := m.svc.creators[game].CreateStartedTable(ctx, seats, anchor.Bid)
		if err != nil {
			// A member couldn't afford / join, or a transient failure. Release the
			// claim so all re-enter the pool; any partially-created waiting table is
			// reaped by the game's waiting-lobby TTL sweeper (no stake escrowed yet).
			if rerr := m.svc.repo.ReleaseGroup(ctx, agentIDs); rerr != nil {
				m.svc.log.Error("groupmatch release failed", "game", game, "error", rerr)
			}
			m.svc.log.Warn("groupmatch table creation failed", "game", game, "error", err)
			continue
		}
		if err := m.svc.repo.MarkMatchedGroup(ctx, agentIDs, matchID); err != nil {
			// The table is live and every seat escrowed; rows stay 'claimed' (not
			// re-formed — WaitingByGame only returns 'waiting'), so no double-create.
			// The agents just won't read match_id from the queue until reconciliation.
			m.svc.log.Error("groupmatch mark-matched failed (table live, rows left claimed)", "game", game, "match", matchID, "error", err)
		}
		m.svc.m.tables.Inc()
		for _, idx := range group {
			used[idx] = true
		}
	}
}

// bandFor is the rating half-width allowed for an anchor right now: it widens the
// longer the anchor has waited, so a long-waiting agent eventually matches anyone.
func bandFor(anchor Entry, now time.Time, cfg Config) int {
	wait := now.Sub(anchor.EnqueuedAt)
	steps := 0
	if cfg.StepInterval > 0 {
		steps = int(wait / cfg.StepInterval)
	}
	band := cfg.BaseBand + steps*cfg.BandStep
	if band > cfg.MaxBand {
		band = cfg.MaxBand
	}
	return band
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
