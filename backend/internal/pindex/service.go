package pindex

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/platform"
)

// Service orchestrates P-Index recompute: it turns rating.updated facts into dirty
// marks, drains the dirty set through the pure engine, and persists results. It
// never blocks the match/rating hot path — enqueue is cheap; recompute runs on a
// background worker.
type Service struct {
	repo   Repo
	engine *Engine
	season func() int
	clock  platform.Clock
	log    *slog.Logger
}

// New builds the P-Index service. season supplies the current season number
// (rating.Service.CurrentSeason); clock supplies the recompute "as of" time.
func New(repo Repo, season func() int, clock platform.Clock, log *slog.Logger) *Service {
	return &Service{repo: repo, engine: NewEngine(), season: season, clock: clock, log: log}
}

// OnRatingUpdated is the event handler for rating.updated: it marks every affected
// developer dirty so the worker recomputes their P-Index. Idempotent (the dirty set
// is a keyed upsert), so at-least-once delivery is safe.
func (s *Service) OnRatingUpdated(ctx context.Context, ev events.Event) error {
	var payload struct {
		Agents []struct {
			Agent string `json:"agent"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return err
	}
	agents := make([]string, 0, len(payload.Agents))
	for _, a := range payload.Agents {
		if a.Agent != "" {
			agents = append(agents, a.Agent)
		}
	}
	if len(agents) == 0 {
		return nil
	}
	return s.repo.EnqueueDirtyByAgents(ctx, agents)
}

// RunRecompute drains up to `batch` dirty developers, recomputes each, and — if any
// were recomputed — refreshes the season ranking. Designed to be called on a ticker
// by a background worker. Failures leave a developer dirty for the next tick.
func (s *Service) RunRecompute(ctx context.Context, batch int) error {
	items, err := s.repo.PeekDirty(ctx, batch)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	season := s.season()
	recomputed := 0
	for _, it := range items {
		if err := s.Recompute(ctx, it.UserPublicID, season); err != nil {
			s.log.Warn("pindex recompute failed; leaving dirty", "developer", it.UserPublicID, "err", err)
			continue
		}
		// Clear only if no re-enqueue bumped the token during recompute; otherwise
		// the developer stays dirty and is reprocessed next tick (no lost update).
		if _, err := s.repo.ClearDirty(ctx, it.UserPublicID, it.Token); err != nil {
			s.log.Warn("pindex clear-dirty failed", "developer", it.UserPublicID, "err", err)
			continue
		}
		recomputed++
	}
	if recomputed > 0 {
		if err := s.repo.Rank(ctx, season); err != nil {
			return err
		}
	}
	return nil
}

// Recompute computes and persists one developer's P-Index for a season. Pure engine
// over repo-assembled inputs; the write (snapshot + history + pindex.updated event)
// is transactional in the repo.
func (s *Service) Recompute(ctx context.Context, userPublicID string, season int) error {
	cfg, err := s.repo.ActiveConfig(ctx)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	in, err := s.repo.Inputs(ctx, userPublicID, season, now)
	if err != nil {
		return err
	}
	in.UserPublicID = userPublicID
	in.Season = season
	in.AsOf = now
	res := s.engine.Compute(in, cfg)
	return s.repo.Save(ctx, userPublicID, season, res, in.Hash(), now)
}

// Get returns a developer's P-Index snapshot for a season (0 season ⇒ current).
func (s *Service) Get(ctx context.Context, userPublicID string, season int) (Snapshot, bool, error) {
	if season <= 0 {
		season = s.season()
	}
	return s.repo.Get(ctx, userPublicID, season)
}

// History returns a developer's recent recompute history (transparency trail).
func (s *Service) History(ctx context.Context, userPublicID string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	return s.repo.History(ctx, userPublicID, limit)
}
