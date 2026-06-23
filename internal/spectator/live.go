package spectator

import (
	"context"
	"sync"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/platform"
)

// EventLog supplies a match's full event log for Last-Event-ID resume. Satisfied
// by store.MatchRepo (its LoadEvents method).
type EventLog interface {
	LoadEvents(ctx context.Context, matchPublicID string) ([]gs.Event, error)
}

// Repo backs the live arena lists. Reads only (active matches + day counters).
type Repo interface {
	LiveMatches(ctx context.Context) ([]LiveMatch, error)
	LiveStats(ctx context.Context) (LiveStats, error)
}

// LiveMatch is one row of the live arena feed.
type LiveMatch struct {
	MatchID     string   `json:"match_id"`
	Agents      []string `json:"agents"`
	Bid         int64    `json:"bid"`
	Round       int      `json:"round"`
	TotalRounds int      `json:"total_rounds"`
	Scores      [2]int   `json:"scores"`
}

// LiveStats is the arena ticker.
type LiveStats struct {
	MatchesToday      int64 `json:"matches_today"`
	CoinsWageredToday int64 `json:"coins_wagered_today"`
	BiggestWinToday   int64 `json:"biggest_win_today"`
}

// Live serves the cached live-match list and stats ticker. The short caches
// collapse bursts of public reads into one query per window (CDN-frontable too).
type Live struct {
	repo     Repo
	clock    platform.Clock
	matchTTL time.Duration
	statsTTL time.Duration

	mu         sync.Mutex
	matches    []LiveMatch
	matchesExp time.Time
	matchesOK  bool
	stats      LiveStats
	statsExp   time.Time
	statsOK    bool
}

// NewLive builds the cached live-reads service (2s matches, 5s stats by default).
func NewLive(repo Repo, clock platform.Clock) *Live {
	return &Live{repo: repo, clock: clock, matchTTL: 2 * time.Second, statsTTL: 5 * time.Second}
}

// Matches returns the live match list, refreshing at most once per matchTTL.
func (l *Live) Matches(ctx context.Context) ([]LiveMatch, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	if l.matchesOK && now.Before(l.matchesExp) {
		return l.matches, nil
	}
	v, err := l.repo.LiveMatches(ctx)
	if err != nil {
		return nil, err
	}
	l.matches, l.matchesExp, l.matchesOK = v, now.Add(l.matchTTL), true
	return v, nil
}

// Stats returns the live ticker, refreshing at most once per statsTTL.
func (l *Live) Stats(ctx context.Context) (LiveStats, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	if l.statsOK && now.Before(l.statsExp) {
		return l.stats, nil
	}
	v, err := l.repo.LiveStats(ctx)
	if err != nil {
		return LiveStats{}, err
	}
	l.stats, l.statsExp, l.statsOK = v, now.Add(l.statsTTL), true
	return v, nil
}
