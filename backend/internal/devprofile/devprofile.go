// Package devprofile serves the public DEVELOPER reputation surface: the permanent
// profile at /v1/developers/{handle} (P-Index, rank, streaks, arenas, agents,
// followers), the P-Index transparency breakdown, the developer's match history, and
// the @handle + developer↔developer follow graph. Reputation here is aggregated
// across all of a developer's agents — distinct from the per-agent profile in
// internal/profiles.
package devprofile

import (
	"context"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/pindex"
)

// PIndexView is the P-Index snapshot + its latest transparency breakdown.
type PIndexView struct {
	*pindex.Snapshot
	LatestBreakdown []pindex.Contribution `json:"latest_breakdown,omitempty"`
}

// DeveloperProfile is the permanent public profile.
type DeveloperProfile struct {
	Developer     Identity         `json:"developer"`
	Season        int              `json:"season"`
	PIndex        *pindex.Snapshot `json:"p_index"`
	Stats         Stats            `json:"stats"`
	Arenas        []ArenaStat      `json:"arenas"`
	Agents        []AgentCard      `json:"agents"`
	RecentMatches []MatchRow       `json:"recent_matches"`
	Achievements  []Badge          `json:"achievements"`
	Followers     int              `json:"followers"`
	Following     int              `json:"following"`
	// TotalEarningsUSD is the developer's lifetime net winnings (coins earned across
	// all of their agents and seasons) converted to US dollars via the coin peg.
	TotalEarningsUSD float64 `json:"total_earnings_usd"`
}

// Service assembles developer profiles from the repo + the P-Index service.
type Service struct {
	repo      Repo
	pindex    *pindex.Service
	season    func() int
	coinCents int64 // face value of one coin in cents (peg); defaults to 1
}

// New builds the service.
func New(repo Repo, pindexSvc *pindex.Service, season func() int) *Service {
	return &Service{repo: repo, pindex: pindexSvc, season: season, coinCents: 1}
}

// SetCoinCents wires the coin→USD peg (config CoinCents) used to price lifetime
// earnings. A non-positive value is ignored so the safe 1¢ default stands.
func (s *Service) SetCoinCents(cents int64) {
	if cents > 0 {
		s.coinCents = cents
	}
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{3,30}$`)

// Profile assembles the full public profile for a handle (username or user public id).
func (s *Service) Profile(ctx context.Context, handle string) (DeveloperProfile, bool, error) {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil || !found {
		return DeveloperProfile{}, found, err
	}
	season := s.season()
	var p DeveloperProfile
	p.Developer = id
	p.Season = season

	stats, arenas, err := s.repo.Stats(ctx, id.UserPublicID, season)
	if err != nil {
		return DeveloperProfile{}, true, err
	}
	p.Stats, p.Arenas = stats, arenas

	if p.Agents, err = s.repo.Agents(ctx, id.UserPublicID, season); err != nil {
		return DeveloperProfile{}, true, err
	}
	if p.RecentMatches, err = s.repo.RecentMatches(ctx, id.UserPublicID, 10); err != nil {
		return DeveloperProfile{}, true, err
	}
	if p.Achievements, err = s.repo.Badges(ctx, id.UserPublicID); err != nil {
		return DeveloperProfile{}, true, err
	}
	if p.Followers, p.Following, err = s.repo.FollowCounts(ctx, id.UserPublicID); err != nil {
		return DeveloperProfile{}, true, err
	}
	// Lifetime net winnings (all agents, all seasons) priced in USD via the peg.
	coins, err := s.repo.LifetimeCoinsEarned(ctx, id.UserPublicID)
	if err != nil {
		return DeveloperProfile{}, true, err
	}
	cents := s.coinCents
	if cents <= 0 {
		cents = 1
	}
	p.TotalEarningsUSD = math.Round(float64(coins*cents)) / 100 // coins*cents = whole cents
	if snap, ok, err := s.pindex.Get(ctx, id.UserPublicID, season); err != nil {
		return DeveloperProfile{}, true, err
	} else if ok {
		p.PIndex = &snap
	}
	return p, true, nil
}

// PIndex returns the developer's P-Index snapshot + the latest breakdown — the
// transparency surface ("why it changed / what to improve").
func (s *Service) PIndex(ctx context.Context, handle string) (PIndexView, bool, error) {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil || !found {
		return PIndexView{}, found, err
	}
	snap, ok, err := s.pindex.Get(ctx, id.UserPublicID, s.season())
	if err != nil {
		return PIndexView{}, true, err
	}
	view := PIndexView{}
	if ok {
		view.Snapshot = &snap
	}
	hist, err := s.pindex.History(ctx, id.UserPublicID, 1)
	if err != nil {
		return PIndexView{}, true, err
	}
	if len(hist) > 0 {
		view.LatestBreakdown = hist[0].Breakdown
	}
	return view, true, nil
}

// Matches returns the developer's match history (rating before/after/delta + replay).
func (s *Service) Matches(ctx context.Context, handle string, limit int) ([]MatchRow, bool, error) {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil || !found {
		return nil, found, err
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.repo.RecentMatches(ctx, id.UserPublicID, limit)
	return rows, true, err
}

// LeaderboardPage is a page of the developer leaderboard.
type LeaderboardPage struct {
	Season  int         `json:"season"`
	Segment string      `json:"segment"`
	Window  string      `json:"window"`
	Entries []LeaderRow `json:"entries"`
}

// Leaderboard ranks developers by P-Index. window ∈ {all, weekly, monthly}; segment
// ∈ {all, individual, student, startup, company}. Ranks displayed are the position
// within the filtered board (the row also carries the overall season global_rank).
func (s *Service) Leaderboard(ctx context.Context, window, segment string, season, limit, offset int) (LeaderboardPage, error) {
	if season <= 0 {
		season = s.season()
	}
	if segment == "" {
		segment = "all"
	}
	windowDays := 0
	switch window {
	case "weekly":
		windowDays = 7
	case "monthly":
		windowDays = 30
	default:
		window = "all"
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.repo.Leaderboard(ctx, season, segment, windowDays, limit, offset)
	if err != nil {
		return LeaderboardPage{}, err
	}
	for i := range rows {
		rows[i].Rank = offset + i + 1
	}
	return LeaderboardPage{Season: season, Segment: segment, Window: window, Entries: rows}, nil
}

// SetUsername claims/updates the caller's public @handle (validated + unique).
func (s *Service) SetUsername(ctx context.Context, userPublicID, username string) error {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if !usernameRe.MatchString(username) {
		return httpx.NewError(http.StatusBadRequest, "invalid_username", "username must be 3–30 chars: letters, digits, underscore")
	}
	// A public id (usr_…/agt_…) is a valid username shape and appears in URLs, so a
	// username must not impersonate one — otherwise a handle could resolve to two
	// rows (see ResolveHandle) and hijack another developer's profile/follows.
	if lower := strings.ToLower(username); strings.HasPrefix(lower, "usr_") || strings.HasPrefix(lower, "agt_") {
		return httpx.NewError(http.StatusBadRequest, "invalid_username", "username may not start with a reserved id prefix")
	}
	if err := s.repo.SetUsername(ctx, userPublicID, username); err != nil {
		return err
	}
	return nil
}

// Follow / Unfollow manage the developer↔developer graph. handle is the target.
func (s *Service) Follow(ctx context.Context, followerUserPublicID, handle string) error {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil {
		return err
	}
	if !found {
		return httpx.NewError(http.StatusNotFound, "not_found", "no such developer")
	}
	if id.UserPublicID == followerUserPublicID {
		return httpx.NewError(http.StatusBadRequest, "self_follow", "you cannot follow yourself")
	}
	return s.repo.Follow(ctx, followerUserPublicID, id.UserPublicID)
}

func (s *Service) Unfollow(ctx context.Context, followerUserPublicID, handle string) error {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil {
		return err
	}
	if !found {
		return httpx.NewError(http.StatusNotFound, "not_found", "no such developer")
	}
	return s.repo.Unfollow(ctx, followerUserPublicID, id.UserPublicID)
}
