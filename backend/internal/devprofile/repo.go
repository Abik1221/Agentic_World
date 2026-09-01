package devprofile

import (
	"context"
	"time"
)

// Identity is a developer's public identity card.
type Identity struct {
	UserPublicID string `json:"developer"`
	Username     string `json:"username,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	// Bio is the developer's own description of how their agent plays.
	//
	// It was WRITEABLE and unreadable: SetProfile persisted it (onto the oldest
	// non-house agent, the only table with the column) and nothing ever selected it
	// back. So the profile editor had no way to show what was saved — on a second
	// device it rendered an empty box over a stored value — and the public profile
	// could not display a bio at all. Returned here so the write has a matching read.
	Bio            string    `json:"bio,omitempty"`
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

// SandboxStats is practice activity — matches played against the house.
//
// A SEPARATE TYPE from Stats, deliberately, and that separation is the whole design.
// Sandbox is unrated: it never touches the ratings table, never moves coins, and must
// never influence reputation, or the P-Index becomes farmable for free against
// deterministic bots and the "verified" signal collapses.
//
// Keeping them as distinct types rather than a flag on one struct means the two can
// never be accidentally summed. A future change that wants a combined total has to say
// so explicitly, in a place a reviewer will see, instead of a boolean quietly flipping
// somewhere and practice wins leaking into a public record.
//
// It exists at all because effort should be visible: a developer who has practised 200
// matches currently has nothing on their profile showing that work.
type SandboxStats struct {
	TotalMatches int `json:"total_matches"`
	// ByGame is practice matches per arena, so the profile can show WHERE the work
	// went rather than one opaque number.
	ByGame map[string]int `json:"by_game,omitempty"`
	// LastPlayed powers "practising recently" — activity is only interesting if it is
	// current, and a stale count reads as abandoned.
	LastPlayed *time.Time `json:"last_played,omitempty"`
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

// DirectoryRow is one developer in the public directory. Unlike LeaderRow it is NOT
// gated on having a P-Index snapshot: a developer who signed up and claimed a handle
// but has never played is still discoverable (Ranked=false, PIndex=0). This is what
// the /u search page lists.
type DirectoryRow struct {
	Developer   string  `json:"developer"`
	Username    string  `json:"username,omitempty"`
	DisplayName string  `json:"display_name,omitempty"`
	AvatarURL   string  `json:"avatar_url,omitempty"`
	Country     string  `json:"country,omitempty"`
	Segment     string  `json:"segment"`
	PIndex      float64 `json:"p_index"`     // 0 when never computed
	GlobalRank  int     `json:"global_rank"` // 0 when unranked
	// Ranked is "appears on the developer leaderboard": a P-Index snapshot this season AND
	// at least one agent that proved a model call. NOT merely "has been computed" — a
	// developer whose agents never routed a proven call keeps a P-Index but is not published,
	// the same way an agent that stakes without verifying keeps its Elo and stays off the
	// ladder. The board's filter and this flag read one predicate so they cannot disagree.
	Ranked   bool      `json:"ranked"`
	Matches  int       `json:"matches"`
	Wins     int       `json:"wins"`
	Agents   int       `json:"agents"`
	JoinedAt time.Time `json:"joined_at"`
	// MatchedAgent is the agent whose name matched the search, when that is why this
	// row was returned. Empty for an unfiltered list or a handle/name match. The UI
	// shows it so a result that looks nothing like the query still explains itself.
	MatchedAgent string `json:"matched_agent,omitempty"`
}

// Repo reads the developer reputation surface.
type Repo interface {
	// ResolveHandle finds a developer by username OR user public id.
	ResolveHandle(ctx context.Context, handle string) (Identity, bool, error)
	// AllUsernames lists every claimed username, LOWERCASED, for the availability
	// Bloom filter.
	//
	// Usernames only, not public ids: validateUsernameShape already rejects anything
	// starting usr_/agt_, so a candidate that reaches the uniqueness lookup can never
	// be a public id. Including them would only inflate the filter.
	//
	// Lowercased because users.username is CITEXT — `username = $1` matches
	// case-insensitively, so the filter has to normalise the same way or it would
	// report "Alice" absent while the database considers it taken.
	AllUsernames(ctx context.Context) ([]string, error)
	Stats(ctx context.Context, userPublicID string, season int) (Stats, []ArenaStat, error)
	// SandboxActivity returns unrated practice counts. Never mixed into Stats.
	SandboxActivity(ctx context.Context, userPublicID string) (SandboxStats, error)
	// LifetimeCoinsEarned sums net coins earned across all of the developer's
	// (non-house) agents and every season — the basis for total USD earnings.
	LifetimeCoinsEarned(ctx context.Context, userPublicID string) (int64, error)
	Agents(ctx context.Context, userPublicID string, season int) ([]AgentCard, error)
	RecentMatches(ctx context.Context, userPublicID string, limit, offset int) ([]MatchRow, error)
	// TokenEfficiency returns the developer's lifetime LLM tokens burned across their
	// (non-house) agents and how many of those benchmarked matches were wins, so the
	// service can show tokens-per-win. Zero when no benchmark facts exist yet.
	TokenEfficiency(ctx context.Context, userPublicID string) (tokens int64, wins int, err error)
	// TopModels returns the developer's most-declared LLM models (by agent count).
	TopModels(ctx context.Context, userPublicID string, limit int) ([]ModelUsage, error)
	FollowCounts(ctx context.Context, userPublicID string) (followers, following int, err error)
	Badges(ctx context.Context, userPublicID string) ([]Badge, error)
	// Leaderboard ranks developers by P-Index for a season, filtered by segment
	// ("all" = every segment) and window (0 = all-time; 7/30 = active in the last
	// N days).
	Leaderboard(ctx context.Context, season int, segment string, windowDays, limit, offset int) ([]LeaderRow, error)
	// LeaderboardExcluded counts developers who WOULD be on that board but for having no
	// agent that ever proved a model call. Same season/segment/window filters, so the count
	// describes the board actually being looked at rather than the platform as a whole.
	LeaderboardExcluded(ctx context.Context, season int, segment string, windowDays int) (int, error)
	// TopModel returns the season's leading model by win rate over VERIFIED play.
	// (model, false, nil) when no verified match exists yet — an absent leader is a
	// real answer on a platform that has not been played on.
	TopModel(ctx context.Context, season int) (SeasonModel, bool, error)
	// Directory lists PUBLIC developers matching q (blank = everyone), whether or not
	// they have a P-Index yet. sort is "top" (played-first, then P-Index) or "recent"
	// (newest signups first).
	// exclude omits one developer (by public id) from the results; empty omits nobody.
	// It exists so the signed-in visitor's own row can be lifted out of the list without
	// the page count or the total going wrong.
	Directory(ctx context.Context, season int, q, sort string, limit, offset int, exclude string) ([]DirectoryRow, error)
	// DirectoryRowFor returns one developer's own directory row — the same shape and the
	// same query as a list row, so the card at the top of the page and the rows in it can
	// never describe a record differently. found=false when they are not public yet.
	DirectoryRowFor(ctx context.Context, season int, developerID string) (DirectoryRow, bool, error)
	// DirectoryCount is how many developers match `q`, ignoring paging — so the UI can
	// number pages and say "412 developers" instead of only offering "load more".
	DirectoryCount(ctx context.Context, season int, q, exclude string) (int, error)
	SetUsername(ctx context.Context, userPublicID, username string) error
	// SetProfile writes the developer's PUBLIC identity — the name, bio and avatar
	// other developers see on their profile. These columns existed since 0019 and were
	// read on every profile response, but nothing could ever write them: the client
	// kept all three in localStorage, so a developer's own browser showed one identity
	// and everyone else saw an empty one. Clearing site data, or simply opening the
	// site on a second machine, lost it entirely.
	//
	// Each field is optional: nil leaves the stored value untouched. Plain strings
	// here meant an omitted field arrived as "" and cleared the column, so a client
	// editing one field silently erased the other two.
	SetProfile(ctx context.Context, userPublicID string, displayName, bio, avatarURL *string) error
	Follow(ctx context.Context, followerUserPublicID, followeeUserPublicID string) error
	Unfollow(ctx context.Context, followerUserPublicID, followeeUserPublicID string) error
	// IsFollowing answers "does the viewer already follow this developer".
	//
	// Nothing could ask this before, which is why the Follow button always rendered
	// "Follow": the client initialised its state to false and had no way to learn
	// otherwise, so following someone appeared to work and then undid itself on the
	// next page load. The row was there the whole time.
	IsFollowing(ctx context.Context, followerUserPublicID, followeeUserPublicID string) (bool, error)

	// FollowList returns one page of a developer's followers ("followers") or the
	// developers they follow ("following"), newest edge first. Rows are the same shape as
	// a directory row so one card component renders a directory entry and a follower
	// identically — two shapes for the same thing is how two lists come to disagree.
	//
	// It lists EVERY follower, including accounts with no @handle and no agent yet: a
	// follower is a follower whether or not they have finished setting up. Enrichment
	// (P-Index, record) is absent rather than exclusionary for those.
	FollowList(ctx context.Context, season int, userPublicID, direction string, limit, offset int) ([]DirectoryRow, error)
	// FollowListCount is the total for that page's pager, counted with the same predicate
	// as FollowList so the count and the list cannot disagree.
	FollowListCount(ctx context.Context, userPublicID, direction string) (int, error)

	// AgentFollowers lists the developers following one AGENT (the `follows` table, which
	// maps user → agent), plus its total.
	AgentFollowers(ctx context.Context, season int, agentPublicID string, limit, offset int) ([]DirectoryRow, error)
	AgentFollowerCount(ctx context.Context, agentPublicID string) (int, error)
}
