package devprofile

import (
	"context"
	"time"
)

// Identity is a developer's public identity card.
type Identity struct {
	UserPublicID   string    `json:"developer"`
	Username       string    `json:"username,omitempty"`
	DisplayName    string    `json:"display_name,omitempty"`
	AvatarURL      string    `json:"avatar_url,omitempty"`
	Country        string    `json:"country,omitempty"`
	Segment        string    `json:"segment"`
	DeveloperSince time.Time `json:"developer_since"`
}

// Stats is the developer's aggregate record for a season (summed across their agents).
type Stats struct {
	TotalMatches     int    `json:"total_matches"`
	Wins             int    `json:"wins"`
	Losses           int    `json:"losses"`
	Draws            int    `json:"draws"`
	CurrentWinStreak int    `json:"current_win_streak"`
	LongestWinStreak int    `json:"longest_win_streak"`
	FavoriteArena    string `json:"favorite_arena,omitempty"`
}

// ArenaStat is the developer's standing in one arena (best agent's rating + summed record).
type ArenaStat struct {
	Game    string `json:"game"`
	Rating  int    `json:"rating"`
	Wins    int    `json:"wins"`
	Losses  int    `json:"losses"`
	Ties    int    `json:"ties"`
	Matches int    `json:"matches"`
}

// AgentCard is one of the developer's public agents.
type AgentCard struct {
	PublicID   string `json:"agent"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Status     string `json:"status"`
	BestRating int    `json:"best_rating"`
}

// MatchRow is one match in the developer's history, with the rating movement it caused.
type MatchRow struct {
	Match        string     `json:"match"`
	Game         string     `json:"game"`
	Agent        string     `json:"agent"`
	RatingBefore int        `json:"rating_before"`
	RatingAfter  int        `json:"rating_after"`
	RatingDelta  int        `json:"rating_delta"`
	Rank         int        `json:"rank_in_match"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	ReplayURL    string     `json:"replay_url"`
}

// Badge is one earned developer achievement.
type Badge struct {
	Code      string    `json:"code"`
	Label     string    `json:"label"`
	AwardedAt time.Time `json:"awarded_at"`
}

// LeaderRow is one entry on the developer leaderboard.
type LeaderRow struct {
	Rank        int     `json:"rank"`
	Developer   string  `json:"developer"`
	Username    string  `json:"username,omitempty"`
	DisplayName string  `json:"display_name,omitempty"`
	AvatarURL   string  `json:"avatar_url,omitempty"`
	Country     string  `json:"country,omitempty"`
	Segment     string  `json:"segment"`
	PIndex      float64 `json:"p_index"`
	GlobalRank  int     `json:"global_rank"`
}

// Repo reads the developer reputation surface.
type Repo interface {
	// ResolveHandle finds a developer by username OR user public id.
	ResolveHandle(ctx context.Context, handle string) (Identity, bool, error)
	Stats(ctx context.Context, userPublicID string, season int) (Stats, []ArenaStat, error)
	Agents(ctx context.Context, userPublicID string, season int) ([]AgentCard, error)
	RecentMatches(ctx context.Context, userPublicID string, limit int) ([]MatchRow, error)
	FollowCounts(ctx context.Context, userPublicID string) (followers, following int, err error)
	Badges(ctx context.Context, userPublicID string) ([]Badge, error)
	// Leaderboard ranks developers by P-Index for a season, filtered by segment
	// ("all" = every segment) and window (0 = all-time; 7/30 = active in the last
	// N days).
	Leaderboard(ctx context.Context, season int, segment string, windowDays, limit, offset int) ([]LeaderRow, error)
	SetUsername(ctx context.Context, userPublicID, username string) error
	Follow(ctx context.Context, followerUserPublicID, followeeUserPublicID string) error
	Unfollow(ctx context.Context, followerUserPublicID, followeeUserPublicID string) error
}
