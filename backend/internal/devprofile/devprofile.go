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
	Developer Identity         `json:"developer"`
	Season    int              `json:"season"`
	PIndex    *pindex.Snapshot `json:"p_index"`
	Stats     Stats            `json:"stats"`
	// Sandbox is unrated PRACTICE, carried in its own field so no consumer can mistake
	// it for competitive record. It shows effort; it never contributes to reputation.
	Sandbox       SandboxStats `json:"sandbox"`
	Arenas        []ArenaStat  `json:"arenas"`
	Agents        []AgentCard  `json:"agents"`
	RecentMatches []MatchRow   `json:"recent_matches"`
	Achievements  []Badge      `json:"achievements"`
	Followers     int          `json:"followers"`
	Following     int          `json:"following"`
	// TotalEarningsUSD is the developer's lifetime net winnings (coins earned across
	// all of their agents and seasons) converted to US dollars via the coin peg.
	TotalEarningsUSD float64 `json:"total_earnings_usd"`
	// Structural economics: how many LLM tokens the developer's agents burned and how
	// efficiently they convert to wins (tokens per win), plus the models they lean on.
	TotalTokens  int64        `json:"total_tokens,omitempty"`
	TokensPerWin int64        `json:"tokens_per_win,omitempty"` // TotalTokens / wins (0 if no wins)
	TopModels    []ModelUsage `json:"top_models,omitempty"`
}

// ModelUsage is one LLM model a developer runs, with how many of their agents declare
// it — powers the "most-used models" strip on the profile.
type ModelUsage struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Agents   int    `json:"agents"`
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
	// Practice activity. A failure here must not blank a developer's whole profile —
	// competitive record is the important half, so sandbox degrades to zero.
	if sb, err := s.repo.SandboxActivity(ctx, id.UserPublicID); err == nil {
		p.Sandbox = sb
	}

	if p.Agents, err = s.repo.Agents(ctx, id.UserPublicID, season); err != nil {
		return DeveloperProfile{}, true, err
	}
	if p.RecentMatches, err = s.repo.RecentMatches(ctx, id.UserPublicID, 10, 0); err != nil {
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

	// Token economics + most-used models (best-effort: benchmark facts may be absent
	// early, so a zero/empty result is fine — never fail the profile over it).
	if tokens, wins, terr := s.repo.TokenEfficiency(ctx, id.UserPublicID); terr == nil {
		p.TotalTokens = tokens
		if wins > 0 {
			p.TokensPerWin = tokens / int64(wins)
		}
	}
	if models, merr := s.repo.TopModels(ctx, id.UserPublicID, 5); merr == nil {
		p.TopModels = models
	}

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
// Matches returns a page of a developer's match history, newest first. offset is the
// opaque cursor (0 for the first page); nextCursor is >0 when a further page likely
// exists (a full page came back), else 0. Bounded so a caller can't request an
// unbounded scan.
func (s *Service) Matches(ctx context.Context, handle string, limit, offset int) (rows []MatchRow, nextCursor int, found bool, err error) {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil || !found {
		return nil, 0, found, err
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	rows, err = s.repo.RecentMatches(ctx, id.UserPublicID, limit, offset)
	if err != nil {
		return nil, 0, true, err
	}
	if len(rows) == limit {
		nextCursor = offset + limit // a full page ⇒ there may be more
	}
	return rows, nextCursor, true, nil
}

// LeaderboardPage is a page of the developer leaderboard.
type LeaderboardPage struct {
	Season     int         `json:"season"`
	Segment    string      `json:"segment"`
	Window     string      `json:"window"`
	Entries    []LeaderRow `json:"entries"`
	NextCursor int         `json:"next_cursor,omitempty"` // >0 ⇒ pass as ?cursor= for the next page; absent ⇒ last page
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
	next := 0
	if len(rows) == limit {
		next = offset + limit // a full page ⇒ there may be more
	}
	return LeaderboardPage{Season: season, Segment: segment, Window: window, Entries: rows, NextCursor: next}, nil
}

// DirectoryPage is a page of the public developer directory.
type DirectoryPage struct {
	Season     int            `json:"season"`
	Query      string         `json:"q,omitempty"`
	Sort       string         `json:"sort"`
	Total      int            `json:"count"`
	Entries    []DirectoryRow `json:"entries"`
	NextCursor int            `json:"next_cursor,omitempty"`
}

// Directory lists public developers, optionally filtered by a free-text query over
// @handle / display name / public id.
//
// This is deliberately NOT the leaderboard: the leaderboard inner-joins
// developer_pindex, so a developer who has signed up and claimed a handle but never
// played a ranked match is invisible there. The directory left-joins it, so every
// public developer is discoverable from day one and search finds them.
func (s *Service) Directory(ctx context.Context, q, sort string, season, limit, offset int) (DirectoryPage, error) {
	if season <= 0 {
		season = s.season()
	}
	if sort != "recent" {
		sort = "top"
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q = strings.TrimPrefix(strings.TrimSpace(q), "@")
	if len(q) > 64 {
		q = q[:64]
	}
	rows, err := s.repo.Directory(ctx, season, q, sort, limit, offset)
	if err != nil {
		return DirectoryPage{}, err
	}
	if rows == nil {
		rows = []DirectoryRow{} // always marshal as [], never null
	}
	next := 0
	if len(rows) == limit {
		next = offset + limit
	}
	return DirectoryPage{
		Season: season, Query: q, Sort: sort,
		Total: len(rows), Entries: rows, NextCursor: next,
	}, nil
}

// Spotlight is the single developer featured on the landing page.
type Spotlight struct {
	Developer DirectoryRow `json:"developer"`
	TopGame   string       `json:"top_game,omitempty"`
	// Reason explains which rule picked them: "top_p_index" (highest-rated developer
	// who has actually played), "most_active", or "newest" (nobody has played yet).
	Reason string `json:"reason"`
}

// Spotlight picks the developer to feature on the landing page: the highest-ranked
// developer who has actually played a match; if nobody has played yet, the newest
// public developer — so the slot is never empty once a single developer exists.
func (s *Service) Spotlight(ctx context.Context, season int) (Spotlight, bool, error) {
	if season <= 0 {
		season = s.season()
	}
	rows, err := s.repo.Directory(ctx, season, "", "top", 1, 0)
	if err != nil {
		return Spotlight{}, false, err
	}
	if len(rows) == 0 {
		return Spotlight{}, false, nil
	}
	row := rows[0]
	out := Spotlight{Developer: row}
	switch {
	case row.Ranked && row.Matches > 0:
		out.Reason = "top_p_index"
	case row.Matches > 0:
		out.Reason = "most_active"
	default:
		out.Reason = "newest"
	}
	// Their busiest arena — best-effort; an empty game just hides the label.
	if st, _, serr := s.repo.Stats(ctx, row.Developer, season); serr == nil {
		out.TopGame = st.FavoriteArena
	}
	return out, true, nil
}

// Me returns the caller's own public identity (handle, display name, avatar) so the
// dashboard can show the handle they already claimed instead of an empty field.
func (s *Service) Me(ctx context.Context, userPublicID string) (Identity, bool, error) {
	return s.repo.ResolveHandle(ctx, userPublicID)
}

// SetUsername claims/updates the caller's public @handle (validated + unique).
func (s *Service) SetUsername(ctx context.Context, userPublicID, username string) error {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	// ONE validator, shared with the live availability check (see username.go). Two
	// implementations of "is this allowed" is how a field shows a green tick and then
	// the save is rejected.
	if reason := validateUsernameShape(username); reason != "" {
		return httpx.NewError(http.StatusBadRequest, "invalid_username", reason)
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
