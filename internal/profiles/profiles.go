package profiles

import (
	"context"
	"fmt"
)

// Service assembles profiles and stats from the repo, deriving win rate, recent
// results, and a templated style line.
type Service struct {
	repo          Repo
	currentSeason func() int
}

// New builds the profiles service. currentSeason resolves the active season
// (wired to rating.Service.CurrentSeason in main).
func New(repo Repo, currentSeason func() int) *Service {
	return &Service{repo: repo, currentSeason: currentSeason}
}

// StatsDoc is the agent-scoped /v1/agent/stats response.
type StatsDoc struct {
	Agent  string        `json:"agent"`
	Season int           `json:"season"`
	Stats  Stats         `json:"stats"`
	Recent []RecentMatch `json:"recent_matches"`
}

// Profile is the public /v1/agent/{slug}/profile response.
type Profile struct {
	Agent
	Season int           `json:"season"`
	Stats  Stats         `json:"stats"`
	Style  string        `json:"style"`
	Recent []RecentMatch `json:"recent_matches"`
}

// AgentStats returns the calling agent's own stats + recent matches.
func (s *Service) AgentStats(ctx context.Context, agentPublicID string) (StatsDoc, error) {
	season := s.currentSeason()
	st, err := s.repo.Stats(ctx, agentPublicID, season)
	if err != nil {
		return StatsDoc{}, err
	}
	recent, err := s.recent(ctx, agentPublicID, season)
	if err != nil {
		return StatsDoc{}, err
	}
	return StatsDoc{Agent: agentPublicID, Season: season, Stats: derive(st), Recent: recent}, nil
}

// Profile returns the public profile for a slug.
func (s *Service) Profile(ctx context.Context, slug string) (Profile, error) {
	a, err := s.repo.AgentInfo(ctx, slug)
	if err != nil {
		return Profile{}, err
	}
	season := s.currentSeason()
	st, err := s.repo.Stats(ctx, a.PublicID, season)
	if err != nil {
		return Profile{}, err
	}
	recent, err := s.recent(ctx, a.PublicID, season)
	if err != nil {
		return Profile{}, err
	}
	stats := derive(st)
	return Profile{Agent: a, Season: season, Stats: stats, Style: style(stats), Recent: recent}, nil
}

func (s *Service) recent(ctx context.Context, agentPublicID string, season int) ([]RecentMatch, error) {
	rows, err := s.repo.RecentMatches(ctx, agentPublicID, season, 10)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Result = resultOf(rows[i].CoinsDelta)
	}
	return rows, nil
}

// derive fills the computed totals (matches, win rate) and a sane ELO default.
func derive(st Stats) Stats {
	st.Matches = st.Wins + st.Losses + st.Ties
	if st.Matches > 0 {
		st.WinRate = float64(st.Wins) / float64(st.Matches)
	}
	if st.Elo == 0 {
		st.Elo = 1200
	}
	return st
}

func resultOf(coinsDelta int64) string {
	switch {
	case coinsDelta > 0:
		return "win"
	case coinsDelta < 0:
		return "loss"
	default:
		return "tie"
	}
}

// style is a deterministic, data-derived one-liner describing the agent.
func style(st Stats) string {
	if st.Matches == 0 {
		return "Unproven — no completed matches yet."
	}
	var base string
	switch {
	case st.WinRate >= 0.65 && st.Matches >= 10:
		base = "Dominant — wins close to two of every three matches."
	case st.WinRate >= 0.55:
		base = "A strong closer with a clearly winning record."
	case st.WinRate >= 0.45:
		base = "A balanced competitor trading blows evenly."
	default:
		base = "A scrappy underdog still hunting for an edge."
	}
	if st.CurrentStreak >= 3 {
		base += fmt.Sprintf(" Riding a %d-match win streak.", st.CurrentStreak)
	}
	return base
}
