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
	SeasonLength time.Duration // length of one season (default 30 days)
}

// Arena game keys. The rating subject is (agent, game, season): each arena keeps an
// independent rating. New arenas simply use a new key — no schema change.
const (
	GameGoofspiel = "goofspiel"
	GameMafia     = "mafia"
	GameMonopoly  = "monopoly"
)

// Rating algorithms. 1v1 arenas use Glicko-2; N-player arenas use TrueSkill.
const (
	AlgoGlicko2   = "glicko2"
	AlgoTrueSkill = "trueskill"
)

// PlayerResult is one seat's finished-match outcome. Placement is the finishing rank
// (1 = best); equal placements mean a tie between those agents (e.g. a winning
// faction in Mafia all share placement 1).
type PlayerResult struct {
	AgentPublicID string
	Seat          int
	Placement     int
	CoinsDelta    int64
}

// MatchResult is the finalized outcome the match worker hands to Rate. Game selects
// the arena; the player count selects the algorithm (2 → Glicko-2, >2 → TrueSkill).
type MatchResult struct {
	MatchPublicID string
	Game          string
	Players       []PlayerResult
}

// LeaderboardPage is a paginated leaderboard slice.
type LeaderboardPage struct {
	Season     int         `json:"season"`
	Game       string      `json:"game"`
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
	rows, err := s.repo.Leaderboard(ctx, GameGoofspiel, last, 0, 1)
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

// ForceRollCurrentSeason finalises the CURRENT season immediately — recording its
// champion and marking it rolled — regardless of the calendar. This is a DEV/TEST
// affordance so the season-champion surface can be exercised without waiting for a
// real season boundary; it is exposed only behind the dev gate. Idempotent per
// season (RollSeason is a no-op if already rolled).
func (s *Service) ForceRollCurrentSeason(ctx context.Context) (season int, champion string, err error) {
	cur := s.CurrentSeason()
	champion, err = s.championOf(ctx, cur)
	if err != nil {
		return 0, "", err
	}
	if _, err = s.repo.RollSeason(ctx, cur, champion); err != nil {
		return 0, "", err
	}
	return cur, champion, nil
}

// championOf returns the top-ranked agent of a season, or "" if none played. It
// queries the repo directly with the exact season (Service.Leaderboard treats
// season 0 as "current", which would misresolve season 0 here).
func (s *Service) championOf(ctx context.Context, season int) (string, error) {
	rows, err := s.repo.Leaderboard(ctx, GameGoofspiel, season, 0, 1)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].AgentPublicID, nil
}

// Elo returns an agent's current-season rating in the given arena, or the 1500
// baseline if it has no rating row yet — unrated agents matchmake from the baseline.
// Used by matchmaking to pair within a skill band.
func (s *Service) Elo(ctx context.Context, agentPublicID, game string) (int, error) {
	if game == "" {
		game = GameGoofspiel
	}
	return s.repo.AgentElo(ctx, agentPublicID, game, s.CurrentSeason())
}

// Rate applies a finished match's rating change to the match's arena in the current
// season. Idempotent per match. 2-player matches use Glicko-2; N-player (>2) matches
// use TrueSkill. Implements (via an adapter) match.Rater.
func (s *Service) Rate(ctx context.Context, res MatchResult) error {
	if len(res.Players) < 2 {
		return nil // nothing to rate (need at least two participants)
	}
	game := res.Game
	if game == "" {
		game = GameGoofspiel
	}
	// The algorithm is fixed PER ARENA, not per match: Goofspiel (always 1v1) uses
	// Glicko-2; every other arena uses TrueSkill. Choosing by arena (not by the
	// per-match player count) prevents two algorithms from writing incompatible
	// state (elo/rd/vol vs mu/sigma) to the same rating row and clobbering it.
	algo := AlgoTrueSkill
	compute := TrueSkillApply
	if game == GameGoofspiel && len(res.Players) == 2 {
		algo = AlgoGlicko2
		compute = glicko2Apply
	}
	players := make([]ApplyPlayer, len(res.Players))
	for i, p := range res.Players {
		players[i] = ApplyPlayer{
			AgentPublicID: p.AgentPublicID, Seat: p.Seat,
			Placement: p.Placement, CoinsDelta: p.CoinsDelta,
		}
	}
	applied, err := s.repo.ApplyMatch(ctx, ApplyInput{
		MatchPublicID: res.MatchPublicID,
		Game:          game,
		Season:        s.CurrentSeason(),
		Algo:          algo,
		Players:       players,
		Compute:       compute,
	})
	if err != nil {
		return err
	}
	if applied {
		s.m.eloUpdates.Inc()
	}
	return nil
}

// glicko2Apply is the 2-player Compute closure: it derives seat 0's score from the
// two placements and runs the existing, unchanged Glicko-2 update, leaving the
// TrueSkill (mu/sigma) fields untouched.
func glicko2Apply(cur []RatingState, placements []int) []RatingState {
	scoreA := 0.5
	switch {
	case placements[0] < placements[1]:
		scoreA = 1
	case placements[0] > placements[1]:
		scoreA = 0
	}
	a := PlayerRating{Elo: cur[0].Elo, RD: cur[0].RD, Vol: cur[0].Vol}
	b := PlayerRating{Elo: cur[1].Elo, RD: cur[1].RD, Vol: cur[1].Vol}
	na, nb := Glicko2(a, b, scoreA)
	return []RatingState{
		{Elo: na.Elo, RD: na.RD, Vol: na.Vol, Mu: cur[0].Mu, Sigma: cur[0].Sigma},
		{Elo: nb.Elo, RD: nb.RD, Vol: nb.Vol, Mu: cur[1].Mu, Sigma: cur[1].Sigma},
	}
}

// Leaderboard returns one page of season standings (defaults: current season,
// limit 50, capped at 100). offset-based cursor.
func (s *Service) Leaderboard(ctx context.Context, game string, season, offset, limit int) (LeaderboardPage, error) {
	if game == "" {
		game = GameGoofspiel
	}
	if season <= 0 {
		season = s.CurrentSeason()
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.repo.Leaderboard(ctx, game, season, offset, limit)
	if err != nil {
		return LeaderboardPage{}, err
	}
	for i := range rows {
		rows[i].Rank = offset + i + 1
	}
	page := LeaderboardPage{Season: season, Game: game, Entries: rows}
	if len(rows) == limit {
		page.NextCursor = strconv.Itoa(offset + limit)
	}
	return page, nil
}

// BenchmarkPage is the "which model wins" board for the current season.
type BenchmarkPage struct {
	Season int         `json:"season"`
	Game   string      `json:"game"`
	Models []ModelStat `json:"models"`
}

// ModelBenchmark ranks declared models by season performance. minGames defaults
// to 1 (a model must have actually played). Games + WinRate are computed here so
// the store stays a plain aggregation.
func (s *Service) ModelBenchmark(ctx context.Context, game string, minGames int) (BenchmarkPage, error) {
	if game == "" {
		game = GameGoofspiel
	}
	if minGames <= 0 {
		minGames = 1
	}
	season := s.CurrentSeason()
	models, err := s.repo.ModelBenchmark(ctx, season, game, minGames)
	if err != nil {
		return BenchmarkPage{}, err
	}
	for i := range models {
		m := &models[i]
		m.Games = m.Wins + m.Losses + m.Ties
		if decisive := m.Wins + m.Losses; decisive > 0 {
			m.WinRate = float64(m.Wins) / float64(decisive)
		}
	}
	return BenchmarkPage{Season: season, Game: game, Models: models}, nil
}

// Standing returns an agent's rank + totals in the given arena for the current season.
func (s *Service) Standing(ctx context.Context, agentPublicID, game string) (Standing, bool, error) {
	if game == "" {
		game = GameGoofspiel
	}
	return s.repo.AgentStanding(ctx, s.CurrentSeason(), game, agentPublicID)
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
