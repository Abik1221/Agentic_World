package rating

import (
	"context"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// seasonEpoch anchors season numbering; seasons are fixed-length windows from here.
var seasonEpoch = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// Config tunes the rating system.
type Config struct {
	K            int           // ELO volatility factor (default 32)
	SeasonLength time.Duration // length of one season (default 30 days)
}

// MatchResult is the finalized outcome the match worker hands to Rate.
type MatchResult struct {
	MatchPublicID string
	WinnerSeat    int
	Agents        [2]string
	CoinsDelta    [2]int64
}

// LeaderboardPage is a paginated leaderboard slice.
type LeaderboardPage struct {
	Season     int         `json:"season"`
	Entries    []LeaderRow `json:"entries"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// Service applies rating changes and serves the leaderboard.
type Service struct {
	repo  Repo
	clock platform.Clock
	cfg   Config
	m     *metrics
}

// New builds the rating service.
func New(repo Repo, clock platform.Clock, cfg Config, reg *prometheus.Registry) *Service {
	if cfg.K <= 0 {
		cfg.K = 32
	}
	if cfg.SeasonLength <= 0 {
		cfg.SeasonLength = 30 * 24 * time.Hour
	}
	return &Service{repo: repo, clock: clock, cfg: cfg, m: newMetrics(reg)}
}

// CurrentSeason is the season number for now (date-derived; a new window starts a
// fresh ELO baseline while prior seasons' rows remain as the historical snapshot).
func (s *Service) CurrentSeason() int {
	d := s.clock.Now().Sub(seasonEpoch)
	if d < 0 {
		return 0
	}
	return int(d / s.cfg.SeasonLength)
}

// Elo returns an agent's current-season rating, or the 1200 baseline if it has
// no rating row yet (unrated agents matchmake from the baseline). Used by
// matchmaking to pair within a skill band.
func (s *Service) Elo(ctx context.Context, agentPublicID string) (int, error) {
	return s.repo.AgentElo(ctx, agentPublicID, s.CurrentSeason())
}

// Rate applies a finished match's rating change in the current season. Idempotent
// per match. Implements (via an adapter) match.Rater.
func (s *Service) Rate(ctx context.Context, res MatchResult) error {
	scoreA := ScoreForSeat0(res.WinnerSeat)
	applied, err := s.repo.ApplyMatch(ctx, ApplyInput{
		MatchPublicID: res.MatchPublicID,
		Season:        s.CurrentSeason(),
		Agents:        res.Agents,
		CoinsDelta:    res.CoinsDelta,
		WinnerSeat:    res.WinnerSeat,
		Compute:       func(a, b PlayerRating) (PlayerRating, PlayerRating) { return Glicko2(a, b, scoreA) },
	})
	if err != nil {
		return err
	}
	if applied {
		s.m.eloUpdates.Inc()
	}
	return nil
}

// Leaderboard returns one page of season standings (defaults: current season,
// limit 50, capped at 100). offset-based cursor.
func (s *Service) Leaderboard(ctx context.Context, season, offset, limit int) (LeaderboardPage, error) {
	if season <= 0 {
		season = s.CurrentSeason()
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.repo.Leaderboard(ctx, season, offset, limit)
	if err != nil {
		return LeaderboardPage{}, err
	}
	for i := range rows {
		rows[i].Rank = offset + i + 1
	}
	page := LeaderboardPage{Season: season, Entries: rows}
	if len(rows) == limit {
		page.NextCursor = strconv.Itoa(offset + limit)
	}
	return page, nil
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct{ eloUpdates prometheus.Counter }

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{eloUpdates: prometheus.NewCounter(prometheus.CounterOpts{
		Name: "elo_updates_total", Help: "Matches whose ELO change was applied.",
	})}
	reg.MustRegister(m.eloUpdates)
	return m
}
