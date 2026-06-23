// Package profiles serves public agent profiles, the agent-scoped stats endpoint,
// and recent-match history. Headline stats come from the ratings aggregate
// (Stage 7); the style line is a deterministic template over those numbers. All
// reads are cacheable; nothing here is a new source of truth.
package profiles

import (
	"context"
	"time"
)

// Repo supplies the read-side facts for profiles and stats.
type Repo interface {
	// AgentInfo resolves a public slug to its agent identity (ErrNotFound if none).
	AgentInfo(ctx context.Context, slug string) (Agent, error)
	// Stats returns the season rating aggregate for an agent (zero-value + 1200 ELO
	// when the agent has no rated matches this season).
	Stats(ctx context.Context, agentPublicID string, season int) (Stats, error)
	// RecentMatches returns the agent's most recent finished matches, newest first.
	RecentMatches(ctx context.Context, agentPublicID string, season, limit int) ([]RecentMatch, error)
}

// Agent is the public identity shown on a profile.
type Agent struct {
	PublicID          string `json:"agent"`
	Name              string `json:"name"`
	Slug              string `json:"slug"`
	XHandle           string `json:"x_handle,omitempty"`
	Status            string `json:"status"`
	VerificationLevel string `json:"verification_level"`
}

// Stats is the season rating aggregate plus derived totals.
type Stats struct {
	Matches       int     `json:"matches"`
	Wins          int     `json:"wins"`
	Losses        int     `json:"losses"`
	Ties          int     `json:"ties"`
	WinRate       float64 `json:"win_rate"`
	Elo           int     `json:"elo"`
	CoinsEarned   int64   `json:"coins_earned"`
	CurrentStreak int     `json:"current_streak"`
	BestStreak    int     `json:"best_streak"`
}

// RecentMatch is one row of an agent's match history.
type RecentMatch struct {
	MatchID     string    `json:"match_id"`
	Result      string    `json:"result"` // win | loss | tie
	YourScore   int       `json:"your_score"`
	OppScore    int       `json:"opp_score"`
	CoinsDelta  int64     `json:"coins_delta"`
	Opponent    string    `json:"opponent"`
	OpponentElo int       `json:"opponent_elo"`
	FinishedAt  time.Time `json:"finished_at"`
}
