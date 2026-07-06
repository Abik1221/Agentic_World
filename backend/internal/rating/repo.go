package rating

import "context"

// Repo persists ratings. ApplyMatch performs the read-modify-write atomically
// under row locks; Leaderboard is a read-only ranked query.
type Repo interface {
	// ApplyMatch records a match's rating change in one transaction, idempotently:
	// the rating_updates marker is inserted first, and if the match was already
	// rated the call is a no-op returning applied=false. It locks both agents'
	// rating rows, calls in.Compute(currentEloA, currentEloB) for the new ELOs,
	// and updates ELO + W/L/T + streak + coins_earned.
	ApplyMatch(ctx context.Context, in ApplyInput) (applied bool, err error)
	// Leaderboard returns season standings ordered by ELO desc, paginated by offset.
	Leaderboard(ctx context.Context, season, offset, limit int) ([]LeaderRow, error)
	// AgentElo returns the agent's ELO for the season, or 1200 if it has no row yet.
	AgentElo(ctx context.Context, agentPublicID string, season int) (int, error)

	// LastRolledSeason returns the highest finalised season, or -1 if none.
	LastRolledSeason(ctx context.Context) (int, error)

	// RollSeason finalises a completed season exactly once (idempotent on season):
	// it records the roll and, iff newly rolled, emits a season.rolled event in the
	// same transaction. champion may be "" (no matches). Returns whether it rolled.
	RollSeason(ctx context.Context, season int, champion string) (rolled bool, err error)
}

// ApplyInput is the resolved rating update for one finished match. Index 0/1 are
// seats A/B. Compute keeps the Glicko-2 formula in this package, out of the store:
// the store reads both agents' current (rating, RD, volatility), hands them in,
// and writes back what Compute returns.
type ApplyInput struct {
	MatchPublicID string
	Season        int
	Agents        [2]string
	CoinsDelta    [2]int64
	WinnerSeat    int
	Compute       func(a, b PlayerRating) (PlayerRating, PlayerRating)
}

// LeaderRow is one leaderboard entry (Rank is filled in by the service).
type LeaderRow struct {
	Rank          int    `json:"rank"`
	AgentPublicID string `json:"agent"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	AvatarURL     string `json:"avatar_url,omitempty"`
	Elo           int    `json:"elo"`
	Wins          int    `json:"wins"`
	Losses        int    `json:"losses"`
	Ties          int    `json:"ties"`
	CoinsEarned   int64  `json:"coins_earned"`
	Streak        int    `json:"current_streak"`
}
