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

// SeasonInfo describes a season's fixed window.
type SeasonInfo struct {
	Season    int       `json:"season"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Now       time.Time `json:"now"`
	Remaining string    `json:"remaining"` // human duration until the season ends
}

// SeasonBounds returns the [start, end) window for a season number.
func (s *Service) SeasonBounds(season int) (start, end time.Time) {
	start = seasonEpoch.Add(time.Duration(season) * s.cfg.SeasonLength)
	end = start.Add(s.cfg.SeasonLength)
	return start, end
}

// SeasonChampion is the winner of the most recently finalised season — the
// rank-1 agent of that season's final standings — with full stats + avatar.
// Champion is nil when no season has been finalised yet (or it had no matches).
type SeasonChampion struct {
	Season   int        `json:"season"`
	Champion *LeaderRow `json:"champion"`
}

// SeasonChampion returns the winner of the last finalised season. Standings are
// preserved per-season, so the champion is that season's rank 1.
func (s *Service) SeasonChampion(ctx context.Context) (SeasonChampion, error) {
	last, err := s.repo.LastRolledSeason(ctx)
	if err != nil {
		return SeasonChampion{}, err
	}
	res := SeasonChampion{Season: last}
	if last < 0 {
		return res, nil // no season finalised yet
	}
	rows, err := s.repo.Leaderboard(ctx, last, 0, 1)
	if err != nil {
		return SeasonChampion{}, err
	}
	if len(rows) > 0 {
		rows[0].Rank = 1
		res.Champion = &rows[0]
	}
	return res, nil
}

// CurrentSeasonInfo describes the ongoing season and how long is left in it.
func (s *Service) CurrentSeasonInfo() SeasonInfo {
	season := s.CurrentSeason()
	start, end := s.SeasonBounds(season)
	now := s.clock.Now()
	return SeasonInfo{
		Season: season, StartsAt: start, EndsAt: end, Now: now,
		Remaining: end.Sub(now).Round(time.Second).String(),
	}
}

// RollCompleted finalises every season that has ended but not yet been rolled:
// it records the roll (idempotently) and emits season.rolled with the champion
// (the top of that season's leaderboard, or empty if the season had no matches).
// Safe to call repeatedly and on every instance — the DB roll row is the guard.
func (s *Service) RollCompleted(ctx context.Context) error {
	last, err := s.repo.LastRolledSeason(ctx)
	if err != nil {
		return err
	}
	cur := s.CurrentSeason()
	for season := last + 1; season < cur; season++ {
		champion, err := s.championOf(ctx, season)
		if err != nil {
			return err
		}
		if _, err := s.repo.RollSeason(ctx, season, champion); err != nil {
			return err
		}
	}
	return nil
}

// championOf returns the top-ranked agent of a season, or "" if none played. It
// queries the repo directly with the exact season (Service.Leaderboard treats
// season 0 as "current", which would misresolve season 0 here).
func (s *Service) championOf(ctx context.Context, season int) (string, error) {
	rows, err := s.repo.Leaderboard(ctx, season, 0, 1)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].AgentPublicID, nil
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
