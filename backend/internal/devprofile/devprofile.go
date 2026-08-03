// Package devprofile serves the public DEVELOPER reputation surface: the permanent
// profile at /v1/developers/{handle} (P-Index, rank, streaks, arenas, agents,
// followers), the P-Index transparency breakdown, the developer's match history, and
// the @handle + developer↔developer follow graph. Reputation here is aggregated
// across all of a developer's agents — distinct from the per-agent profile in
// internal/profiles.
package devprofile

import (
	"context"
	"fmt"
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
	// wallets reads the developer's connected wallet for profile completion.
	// Optional — see SetWalletReader.
	wallets WalletReader
	// follows tells a developer they have a new follower (row + live push).
	// Optional and best-effort — see follownotify.go.
	follows FollowAnnouncer
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
	Season int    `json:"season"`
	Query  string `json:"q,omitempty"`
	Sort   string `json:"sort"`
	// Total was `len(entries)` — the size of the PAGE, under a field called "count".
	// A client rendering it said "20 developers" when there were four hundred, and could
	// not number pages at all. It is now the real match count, ignoring paging.
	Total   int            `json:"count"`
	Entries []DirectoryRow `json:"entries"`
	// Limit is echoed so a client can derive the page count without assuming the server
	// honoured the limit it asked for (it clamps).
	Limit      int `json:"limit"`
	Offset     int `json:"offset"`
	NextCursor int `json:"next_cursor,omitempty"`
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
	// The real total, so the client can number pages. Best-effort: a failed count leaves
	// Total at the page length, which is what it always was — degrading the pager is far
	// better than failing the whole directory over a COUNT.
	total, cerr := s.repo.DirectoryCount(ctx, season, q)
	if cerr != nil || total < len(rows) {
		total = offset + len(rows)
	}
	next := 0
	// Derived from the TOTAL, not from "the page came back full". A page that happens to
	// land exactly on the last row used to advertise a next cursor pointing at nothing, so
	// the UI offered one more page and then showed an empty list.
	if offset+len(rows) < total {
		next = offset + limit
	}
	return DirectoryPage{
		Season: season, Query: q, Sort: sort,
		Total: total, Entries: rows, Limit: limit, Offset: offset, NextCursor: next,
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

// Profile limits. Generous enough for a real name and a paragraph, bounded so a
// public field cannot be used as free storage or to break a layout.
const (
	maxDisplayName = 40
	maxBio         = 280
	maxAvatarURL   = 2048
)

// SetProfile writes the developer's PUBLIC identity and returns the stored result.
//
// Every field is optional: nil means "leave it alone", a pointer to "" means "clear it".
// A developer must be able to remove a name or a photo, but a caller editing one field
// must not be able to erase the other two by omission — which is what happened when
// these were plain strings (see the handler).
//
// Trimmed and length-bounded here rather than at the database, so the caller gets a
// specific error instead of a driver one.
func (s *Service) SetProfile(ctx context.Context, userPublicID string, displayName, bio, avatarURL *string) (Identity, error) {
	if displayName != nil {
		v := strings.TrimSpace(*displayName)
		if len([]rune(v)) > maxDisplayName {
			return Identity{}, httpx.NewError(http.StatusBadRequest, "invalid_display_name",
				fmt.Sprintf("display name must be %d characters or fewer", maxDisplayName))
		}
		displayName = &v
	}
	if bio != nil {
		v := strings.TrimSpace(*bio)
		if len([]rune(v)) > maxBio {
			return Identity{}, httpx.NewError(http.StatusBadRequest, "invalid_bio",
				fmt.Sprintf("bio must be %d characters or fewer", maxBio))
		}
		bio = &v
	}
	if avatarURL != nil {
		v := strings.TrimSpace(*avatarURL)
		if len(v) > maxAvatarURL {
			return Identity{}, httpx.NewError(http.StatusBadRequest, "invalid_avatar",
				"avatar URL is too long")
		}
		// An avatar is rendered in other developers' browsers, so the scheme is
		// allow-listed: javascript: and data: URLs in an <img src> are an XSS and an
		// exfiltration vector respectively, and neither has a legitimate use here.
		// http:// is refused too — a mixed-content avatar is a downgrade an attacker on
		// the path can rewrite. The upload endpoint writes avatar_url itself (see
		// DevProfileRepo.SetAvatarURL), so no legitimate flow needs this to be laxer.
		if v != "" && !strings.HasPrefix(v, "https://") {
			return Identity{}, httpx.NewError(http.StatusBadRequest, "invalid_avatar",
				"avatar must be an https:// URL")
		}
		avatarURL = &v
	}
	if err := s.repo.SetProfile(ctx, userPublicID, displayName, bio, avatarURL); err != nil {
		return Identity{}, err
	}
	// Read back rather than echo. The caller renders this, and the only account of what
	// is stored that cannot be wrong is the row itself.
	id, found, err := s.repo.ResolveHandle(ctx, userPublicID)
	if err != nil {
		return Identity{}, err
	}
	if !found {
		return Identity{}, httpx.NewError(http.StatusNotFound, "not_found", "no such developer")
	}
	return id, nil
}

// FollowState is what the client needs to render a follow control correctly: whether
// the viewer follows this developer, and the counts as they stand NOW.
//
// Returned by the read AND by both mutations, on purpose. The button and the number
// beside it come from one response, so they cannot disagree — the pattern that goes
// wrong is a POST that returns only `{following:true}` and leaves the client to guess
// the new count, which drifts the moment two tabs are open.
type FollowState struct {
	Following bool `json:"following"`
	Followers int  `json:"followers"`
	// Following count of the TARGET developer, so the same shape serves a profile
	// header without a second call.
	FollowingCount int `json:"following_count"`
	// IsSelf: the viewer IS this developer. Answered here because the client cannot work
	// it out — the session cookie carries no handle, and a public profile page is
	// cacheable so the server render cannot say either. Without it a developer is shown a
	// Follow button on their own profile, and their own new-follower events are ignored
	// as belonging to somebody else's page.
	IsSelf bool `json:"is_self"`
}

// FollowState reads the viewer's relationship to a developer plus their counts.
// viewerUserPublicID may be empty (signed out) — the counts are public, the
// relationship is then simply false.
func (s *Service) FollowState(ctx context.Context, viewerUserPublicID, handle string) (FollowState, error) {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil {
		return FollowState{}, err
	}
	if !found {
		return FollowState{}, httpx.NewError(http.StatusNotFound, "not_found", "no such developer")
	}
	return s.followState(ctx, viewerUserPublicID, id.UserPublicID)
}

func (s *Service) followState(ctx context.Context, viewer, target string) (FollowState, error) {
	followers, following, err := s.repo.FollowCounts(ctx, target)
	if err != nil {
		return FollowState{}, err
	}
	out := FollowState{Followers: followers, FollowingCount: following, IsSelf: viewer != "" && viewer == target}
	if viewer != "" && !out.IsSelf {
		if out.Following, err = s.repo.IsFollowing(ctx, viewer, target); err != nil {
			return FollowState{}, err
		}
	}
	return out, nil
}

// Follow / Unfollow manage the developer↔developer graph. handle is the target.
//
// Both are IDEMPOTENT and both return the resulting state. Idempotency matters for a
// button: a double-tap on a phone, or a retry after a dropped response, must not error
// or toggle twice — the second call simply confirms what is already true.
func (s *Service) Follow(ctx context.Context, followerUserPublicID, handle string) (FollowState, error) {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil {
		return FollowState{}, err
	}
	if !found {
		return FollowState{}, httpx.NewError(http.StatusNotFound, "not_found", "no such developer")
	}
	if id.UserPublicID == followerUserPublicID {
		return FollowState{}, httpx.NewError(http.StatusBadRequest, "self_follow", "you cannot follow yourself")
	}
	// Was this already a follow? Read BEFORE the write, so a repeat call does not
	// notify the followee a second time. Without this, mashing the button would send a
	// notification per tap.
	already, err := s.repo.IsFollowing(ctx, followerUserPublicID, id.UserPublicID)
	if err != nil {
		return FollowState{}, err
	}
	if err := s.repo.Follow(ctx, followerUserPublicID, id.UserPublicID); err != nil {
		return FollowState{}, err
	}
	state, err := s.followState(ctx, followerUserPublicID, id.UserPublicID)
	if err != nil {
		return FollowState{}, err
	}
	if !already {
		s.announceFollow(ctx, followerUserPublicID, id.UserPublicID, state.Followers)
	}
	return state, nil
}

func (s *Service) Unfollow(ctx context.Context, followerUserPublicID, handle string) (FollowState, error) {
	id, found, err := s.repo.ResolveHandle(ctx, handle)
	if err != nil {
		return FollowState{}, err
	}
	if !found {
		return FollowState{}, httpx.NewError(http.StatusNotFound, "not_found", "no such developer")
	}
	if err := s.repo.Unfollow(ctx, followerUserPublicID, id.UserPublicID); err != nil {
		return FollowState{}, err
	}
	// No event on unfollow, deliberately. "X stopped following you" is a notification
	// no platform sends, because it is information the recipient can do nothing with
	// and would rather not have.
	return s.followState(ctx, followerUserPublicID, id.UserPublicID)
}
