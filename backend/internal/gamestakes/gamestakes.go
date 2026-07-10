// Package gamestakes serves the Super-Admin-configurable stake tiers per game
// (e.g. Mafia: Low / Mid / High) and resolves a chosen tier to its coin stake.
// It mirrors internal/walletadmin: an arena-hosted config surface set via a
// RequirePlatformOrAdmin endpoint with a short read-through cache, so a change
// takes effect within the TTL without a redeploy. Tiers only constrain WHICH
// stake enters the existing limits/escrow/settlement path — no money logic here.
package gamestakes

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/platform"
)

// maxTiers bounds how many bands a game may define (a guardrail on admin input).
const maxTiers = 10

// Tier is one admin-configured stake band for a game.
type Tier struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Coins    int64  `json:"coins"`
	Ordering int    `json:"ordering"`
	Enabled  bool   `json:"enabled"`
}

// GameTiers is a game's full tier set (admin view — includes disabled tiers).
type GameTiers struct {
	Game  string `json:"game"`
	Tiers []Tier `json:"tiers"`
}

// Repo persists per-game stake tiers.
type Repo interface {
	// ListTiers returns every tier for a game (incl. disabled), ordered.
	ListTiers(ctx context.Context, game string) ([]Tier, error)
	// ReplaceTiers atomically replaces a game's whole tier set.
	ReplaceTiers(ctx context.Context, game string, tiers []Tier) error
	// Audit records an admin mutation.
	Audit(ctx context.Context, actor, action, target string, detail []byte) error
}

// Service resolves and administers stake tiers, caching per-game reads briefly.
type Service struct {
	repo  Repo
	clock platform.Clock
	log   *slog.Logger
	ttl   time.Duration

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	tiers []Tier
	at    time.Time
}

// New builds the service with a 10s read cache.
func New(repo Repo, clock platform.Clock, log *slog.Logger) *Service {
	return &Service{repo: repo, clock: clock, log: log, ttl: 10 * time.Second, cache: map[string]cacheEntry{}}
}

// tiers returns a game's full tier set (all, incl. disabled), from cache when fresh.
func (s *Service) tiers(ctx context.Context, game string) ([]Tier, error) {
	s.mu.RLock()
	if e, ok := s.cache[game]; ok && s.clock.Now().Sub(e.at) < s.ttl {
		out := append([]Tier(nil), e.tiers...)
		s.mu.RUnlock()
		return out, nil
	}
	s.mu.RUnlock()

	all, err := s.repo.ListTiers(ctx, game)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[game] = cacheEntry{tiers: append([]Tier(nil), all...), at: s.clock.Now()}
	s.mu.Unlock()
	return all, nil
}

func (s *Service) invalidate(game string) {
	s.mu.Lock()
	delete(s.cache, game)
	s.mu.Unlock()
}

// List returns the ENABLED tiers for a game, ordered — the public menu a client
// or agent chooses from.
func (s *Service) List(ctx context.Context, game string) ([]Tier, error) {
	all, err := s.tiers(ctx, game)
	if err != nil {
		return nil, err
	}
	out := make([]Tier, 0, len(all))
	for _, t := range all {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out, nil
}

// HasTiers reports whether a game has at least one ENABLED tier. Callers require a
// valid tier when true, and fall back to the legacy free-form stake when false.
func (s *Service) HasTiers(ctx context.Context, game string) (bool, error) {
	enabled, err := s.List(ctx, game)
	if err != nil {
		return false, err
	}
	return len(enabled) > 0, nil
}

// Resolve maps a chosen tier key to its coin stake for a game, enforcing that the
// tier exists and is enabled. ErrGameNoTiers when the game defines none.
func (s *Service) Resolve(ctx context.Context, game, tierKey string) (int64, error) {
	all, err := s.tiers(ctx, game)
	if err != nil {
		return 0, err
	}
	if len(all) == 0 {
		return 0, ErrGameNoTiers
	}
	for _, t := range all {
		if t.Key == tierKey {
			if !t.Enabled {
				return 0, ErrTierDisabled
			}
			return t.Coins, nil
		}
	}
	return 0, ErrUnknownTier
}

// AdminGet returns a game's full tier set (incl. disabled) for the admin surface.
func (s *Service) AdminGet(ctx context.Context, game string) (GameTiers, error) {
	all, err := s.tiers(ctx, game)
	if err != nil {
		return GameTiers{}, err
	}
	return GameTiers{Game: game, Tiers: all}, nil
}

// AdminPut validates and atomically replaces a game's tier set, then audits and
// busts the cache. Validation (fail closed): a game key, ≤ maxTiers, unique
// non-empty keys, positive coins, and coins strictly increasing by ordering so
// Low < Mid < High is always true.
func (s *Service) AdminPut(ctx context.Context, actor, game string, tiers []Tier) error {
	game = strings.TrimSpace(game)
	if game == "" {
		return errInvalid("game is required")
	}
	if len(tiers) > maxTiers {
		return errInvalid("too many tiers")
	}
	// Normalize + validate each tier.
	seen := make(map[string]bool, len(tiers))
	for i := range tiers {
		t := &tiers[i]
		t.Key = strings.TrimSpace(t.Key)
		t.Label = strings.TrimSpace(t.Label)
		if t.Key == "" {
			return errInvalid("tier key is required")
		}
		if seen[t.Key] {
			return errInvalid("duplicate tier key: " + t.Key)
		}
		seen[t.Key] = true
		if t.Coins <= 0 {
			return errInvalid("tier coins must be positive: " + t.Key)
		}
		if t.Label == "" {
			t.Label = t.Key
		}
	}
	// Order by `ordering` then require strictly increasing coins.
	sort.SliceStable(tiers, func(i, j int) bool { return tiers[i].Ordering < tiers[j].Ordering })
	for i := 1; i < len(tiers); i++ {
		if tiers[i].Coins <= tiers[i-1].Coins {
			return errInvalid("tier coins must strictly increase by ordering (e.g. Low < Mid < High)")
		}
	}
	if err := s.repo.ReplaceTiers(ctx, game, tiers); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"game": game, "tiers": tiers})
	if err := s.repo.Audit(ctx, actor, "game_stakes_update", game, detail); err != nil {
		s.log.Warn("gamestakes: audit write failed", "game", game, "error", err)
	}
	s.invalidate(game)
	return nil
}
