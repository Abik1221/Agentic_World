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

	// ModelBenchmark aggregates the season's competitive results by the agents'
	// DECLARED model (provider+model from their manifest — self-reported, never
	// verified). Only models with >= minGames total games are returned.
	ModelBenchmark(ctx context.Context, season, minGames int) ([]ModelStat, error)

	// AgentStanding returns one agent's place in the season (rank, totals, declared
	// model). found=false when the agent has no rating row this season.
	AgentStanding(ctx context.Context, season int, agentPublicID string) (standing Standing, found bool, err error)
}

// ModelStat is one declared model's aggregate performance for a season. The
// provider/model are developer-declared (a "claimed" model), so the UI labels
// them as such. WinRate/Games are computed by the service.
type ModelStat struct {
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	Agents   int     `json:"agents"`
	Games    int     `json:"games"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	Ties     int     `json:"ties"`
	AvgElo   int     `json:"avg_elo"`
	CoinsWon int64   `json:"coins_won"`
	WinRate  float64 `json:"win_rate"`
}

// Standing is one agent's season position (for "your rank this season").
type Standing struct {
	Season        int    `json:"season"`
	Rank          int    `json:"rank"`
	Total         int    `json:"total"`
	AgentPublicID string `json:"agent"`
	Name          string `json:"name"`
	Elo           int    `json:"elo"`
	Wins          int    `json:"wins"`
	Losses        int    `json:"losses"`
	Ties          int    `json:"ties"`
	CoinsEarned   int64  `json:"coins_earned"`
	Streak        int    `json:"current_streak"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
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
