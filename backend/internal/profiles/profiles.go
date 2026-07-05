package profiles

import (
	"context"
	"fmt"
	"time"
)

// Service assembles profiles and stats from the repo, deriving win rate, recent
// results, and a templated style line.
type Service struct {
	repo          Repo
	currentSeason func() int
	manifest      Manifest // optional: certification + manifest card for the profile
}

// New builds the profiles service. currentSeason resolves the active season
// (wired to rating.Service.CurrentSeason in main).
func New(repo Repo, currentSeason func() int) *Service {
	return &Service{repo: repo, currentSeason: currentSeason}
}

// SetManifest installs the manifest card provider (call once during wiring). When
// unset, profiles omit the certification/manifest block.
func (s *Service) SetManifest(m Manifest) { s.manifest = m }

// Manifest provides an agent's public manifest facts for its profile card. The
// concrete implementation adapts manifest.Service (wired in main); a nil Card
// result means the agent has no public/verified manifest yet.
type Manifest interface {
	Card(ctx context.Context, agentPublicID string) (*ManifestCard, error)
}

// ManifestCard is the certification + declared-capability block on a profile.
type ManifestCard struct {
	Certified    bool       `json:"certified"`
	AgentVersion string     `json:"agent_version,omitempty"`
	Games        []string   `json:"games,omitempty"`
	Model        *ModelInfo `json:"model,omitempty"`
}

// ModelInfo is always developer-declared (the platform cannot verify a remote
// model), surfaced as "Built using GPT-5.5" / "Powered by Claude".
type ModelInfo struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Declared  bool   `json:"developer_declared"`
	Reasoning bool   `json:"reasoning"`
}

// SeasonElo is one season's standing for an agent (ranking progression / #10).
type SeasonElo struct {
	Season int `json:"season"`
	Elo    int `json:"elo"`
	Wins   int `json:"wins"`
	Losses int `json:"losses"`
	Ties   int `json:"ties"`
}

// Badge is one earned achievement on the profile.
type Badge struct {
	Code      string    `json:"code"`
	AwardedAt time.Time `json:"awarded_at"`
}

// StatsDoc is the agent-scoped /v1/agent/stats response.
type StatsDoc struct {
	Agent  string        `json:"agent"`
	Season int           `json:"season"`
	Stats  Stats         `json:"stats"`
	Recent []RecentMatch `json:"recent_matches"`
}

// Profile is the public /v1/agent/{slug|id}/profile response.
type Profile struct {
	Agent
	Season        int           `json:"season"`
	Stats         Stats         `json:"stats"`
	Style         string        `json:"style"`
	Recent        []RecentMatch `json:"recent_matches"`
	Manifest      *ManifestCard `json:"manifest,omitempty"`       // certification + declared capabilities
	SeasonHistory []SeasonElo   `json:"season_history,omitempty"` // ELO progression across seasons
	Badges        []Badge       `json:"badges,omitempty"`         // earned achievements (reputation)
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
	p := Profile{Agent: a, Season: season, Stats: stats, Style: style(stats), Recent: recent}

	// Certification + declared-capability card (the wedge, on the profile).
	if s.manifest != nil {
		card, err := s.manifest.Card(ctx, a.PublicID)
		if err != nil {
			return Profile{}, err
		}
		p.Manifest = card
	}

	// ELO progression across seasons (#10 improvement-over-time).
	history, err := s.repo.SeasonHistory(ctx, a.PublicID)
	if err != nil {
		return Profile{}, err
	}
	p.SeasonHistory = history

	badges, err := s.repo.Badges(ctx, a.PublicID)
	if err != nil {
		return Profile{}, err
	}
	p.Badges = badges

	return p, nil
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
