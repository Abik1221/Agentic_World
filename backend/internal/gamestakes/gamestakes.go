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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/platform"
)

// maxTiers bounds how many bands a game may define (a guardrail on admin input).
const maxTiers = 10

// DefaultMinStakeUSDCents is the floor on a real-money entry fee: $5.
//
// A floor exists because the platform's cut is a percentage. Below a certain stake
// the rake rounds to nothing while the match still costs real inference spend, so a
// very cheap table is a table the platform runs at a loss — and, worse, it is the
// cheapest possible way for someone to farm ranked activity.
const DefaultMinStakeUSDCents int64 = 500

// Tier is one admin-configured stake band for a game.
//
// COINS ARE CANONICAL. The ledger, escrow, and settlement are all coin-denominated
// and integral, so that is what is stored. USDCents is the same amount expressed in
// the unit an operator actually thinks in, derived through the coin peg on read and
// accepted instead of coins on write. Storing dollars and converting later would mean
// a change to the peg silently re-priced every table; deriving it means a tier keeps
// its exact coin value and moves with the peg exactly as every balance does.
type Tier struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Coins int64  `json:"coins"`
	// USDCents is derived on read. On write it is an ALTERNATIVE to Coins: send one
	// or the other, never both with conflicting values.
	USDCents int64 `json:"usd_cents"`
	Ordering int   `json:"ordering"`
	Enabled  bool  `json:"enabled"`
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
	// coinCents is the peg: the face value of one coin, in cents. Used only to
	// present and accept stakes in dollars; it never enters money movement.
	coinCents int64
	// minStakeUSDCents is the floor on a paid tier. Zero disables the floor, which
	// is what sandbox/practice deployments want. Used only when minStakeSource is nil.
	minStakeUSDCents int64
	// minStakeSource supplies the LIVE admin-configured floor. The floor is policy,
	// not a constant, so an operator can move it without a redeploy. Nil ⇒ the static
	// value above.
	minStakeSource func() int64

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	tiers []Tier
	at    time.Time
}

// New builds the service with a 10s read cache.
func New(repo Repo, clock platform.Clock, log *slog.Logger) *Service {
	return &Service{
		repo: repo, clock: clock, log: log, ttl: 10 * time.Second,
		coinCents:        1,
		minStakeUSDCents: DefaultMinStakeUSDCents,
		cache:            map[string]cacheEntry{},
	}
}

// SetCoinCents wires the coin→USD peg so the admin surface can speak dollars.
// Ignored if non-positive, since a zero peg would make every stake free.
func (s *Service) SetCoinCents(cents int64) {
	if cents > 0 {
		s.coinCents = cents
	}
}

// SetMinStakeUSDCents overrides the static paid-tier floor. Zero removes it.
func (s *Service) SetMinStakeUSDCents(cents int64) {
	if cents >= 0 {
		s.minStakeUSDCents = cents
	}
}

// SetMinStakeSource wires the live admin-configured floor, checked at write time so a
// change takes effect on the next save rather than the next deploy.
func (s *Service) SetMinStakeSource(f func() int64) { s.minStakeSource = f }

// minStake resolves the floor for a write happening now. A negative published value
// is nonsense and falls back; zero is a deliberate "no floor" and is honoured.
func (s *Service) minStake() int64 {
	if s.minStakeSource != nil {
		if c := s.minStakeSource(); c >= 0 {
			return c
		}
	}
	return s.minStakeUSDCents
}

// usdCents converts a coin stake to cents. Exact: the config gate requires
// 100 % coinCents == 0, so a coin is always a whole number of cents.
func (s *Service) usdCents(coins int64) int64 { return coins * s.coinCents }

// coinsFromUSD converts cents to coins, reporting whether the amount lands exactly
// on a coin boundary. It deliberately does NOT round: silently turning $5.01 into
// $5.00 is money quietly changing under an operator who typed a specific number, and
// the fix is to tell them rather than to pick for them.
func (s *Service) coinsFromUSD(cents int64) (int64, bool) {
	if s.coinCents <= 0 || cents%s.coinCents != 0 {
		return 0, false
	}
	return cents / s.coinCents, true
}

// withUSD returns a copy of the tiers with USDCents populated for display.
func (s *Service) withUSD(in []Tier) []Tier {
	out := make([]Tier, len(in))
	for i, t := range in {
		t.USDCents = s.usdCents(t.Coins)
		out[i] = t
	}
	return out
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
	// Priced in dollars as well as coins so a client renders "$5" without having to
	// know the peg — the peg is ours to keep, not every caller's to reimplement.
	return s.withUSD(out), nil
}

// LowestEnabledCoins returns the cheapest enabled tier for a game. ok is false when
// the game has no enabled tiers, which callers use to fall back rather than to
// silently browse a stake nobody configured.
func (s *Service) LowestEnabledCoins(ctx context.Context, game string) (int64, bool) {
	enabled, err := s.List(ctx, game)
	if err != nil || len(enabled) == 0 {
		return 0, false
	}
	// List returns tiers ordered by `ordering`, and AdminPut enforces that coins
	// strictly increase with it, so the first entry is the cheapest by construction.
	return enabled[0].Coins, true
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

// ResolveStake applies the stake policy for a play request (create/join/queue),
// returning the coin stake to use:
//   - tier set                              → resolve it (the tier's coins ARE the stake)
//   - no tier, entryFee > 0, game has tiers → ErrTierRequired (free-form disallowed)
//   - no tier, entryFee > 0, no tiers       → entryFee (legacy free-form back-compat)
//   - no tier, entryFee == 0                → 0 (no-stakes practice/sandbox)
//
// The returned stake still passes the agent's own budget gate (wallet.CheckJoin)
// downstream — tiers pick WHICH stake, limits decide whether the agent can afford it.
func (s *Service) ResolveStake(ctx context.Context, game, tier string, entryFee int64) (int64, error) {
	if strings.TrimSpace(tier) != "" {
		return s.Resolve(ctx, game, strings.TrimSpace(tier))
	}
	if entryFee > 0 {
		has, err := s.HasTiers(ctx, game)
		if err != nil {
			return 0, err
		}
		if has {
			return 0, ErrTierRequired
		}
		return entryFee, nil
	}
	return 0, nil // no tier, no fee → no-stakes practice
}

// AdminGet returns a game's full tier set (incl. disabled) for the admin surface.
func (s *Service) AdminGet(ctx context.Context, game string) (GameTiers, error) {
	all, err := s.tiers(ctx, game)
	if err != nil {
		return GameTiers{}, err
	}
	return GameTiers{Game: game, Tiers: s.withUSD(all)}, nil
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

		// Dollars are the admin's unit; coins are ours. Accept either, and when the
		// operator sent dollars, convert exactly or refuse — see coinsFromUSD.
		if t.Coins <= 0 && t.USDCents > 0 {
			coins, exact := s.coinsFromUSD(t.USDCents)
			if !exact {
				return errInvalid("tier " + t.Key + ": amount does not land on a whole coin at the current coin price")
			}
			t.Coins = coins
		}
		if t.Coins <= 0 {
			return errInvalid("tier coins must be positive: " + t.Key)
		}
		// The floor is checked on the RESOLVED coin value, not on whatever the caller
		// sent, so it cannot be bypassed by submitting coins instead of dollars.
		if floor := s.minStake(); floor > 0 && s.usdCents(t.Coins) < floor {
			return errInvalid("tier " + t.Key + ": entry fee is below the $" +
				strconv.FormatInt(floor/100, 10) + " minimum")
		}
		// Keep the echoed value consistent with what was actually stored, so the admin
		// UI redisplays the real figure rather than the one that was submitted.
		t.USDCents = s.usdCents(t.Coins)
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
