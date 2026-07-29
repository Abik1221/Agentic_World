package rating

import (
	"context"
	"time"
)

// Repo persists ratings. ApplyMatch performs the read-modify-write atomically
// under row locks; Leaderboard is a read-only ranked query.
type Repo interface {
	// ApplyMatch records a match's rating change in one transaction, idempotently:
	// the rating_updates marker is inserted first, and if the match was already
	// rated the call is a no-op returning applied=false. It locks both agents'
	// rating rows, calls in.Compute(currentEloA, currentEloB) for the new ELOs,
	// and updates ELO + W/L/T + streak + coins_earned.
	ApplyMatch(ctx context.Context, in ApplyInput) (applied bool, err error)
	// Leaderboard returns an arena's season standings ordered by ELO desc, paginated.
	Leaderboard(ctx context.Context, game string, season, offset, limit int) ([]LeaderRow, error)
	// SnapshotRanks records today's rank per (game, season, agent) for trend deltas.
	// Idempotent per day (UNIQUE on taken_on). Returns rows written.
	SnapshotRanks(ctx context.Context, takenOn time.Time) (int, error)
	// AgentElo returns the agent's ELO in the arena for the season, or 1500 if none.
	AgentElo(ctx context.Context, agentPublicID, game string, season int) (int, error)

	// LastRolledSeason returns the highest finalised season, or -1 if none.
	LastRolledSeason(ctx context.Context) (int, error)

	// RollSeason finalises a completed season exactly once (idempotent on season):
	// it records the roll and, iff newly rolled, emits a season.rolled event in the
	// same transaction. champion may be "" (no matches). Returns whether it rolled.
	RollSeason(ctx context.Context, season int, champion string) (rolled bool, err error)

	// ModelBenchmark aggregates the season's competitive results in an arena by the
	// agents' DECLARED model (provider+model from their manifest — self-reported,
	// never verified). Only models with >= minGames total games are returned.
	ModelBenchmark(ctx context.Context, season int, game string, minGames int) ([]ModelStat, error)

	// AgentStanding returns one agent's place in an arena for the season (rank,
	// totals, declared model). found=false when the agent has no rating row.
	AgentStanding(ctx context.Context, season int, game, agentPublicID string) (standing Standing, found bool, err error)
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
	// Structural economics per model, aggregated from agent_match_benchmark across the
	// model's agents (0 until benchmark facts exist). AvgLatencyMs is mean ms per
	// decision — the "low latency" signal; EstCostUSD/Tokens are cumulative spend.
	AvgLatencyMs int     `json:"avg_latency_ms"`
	EstCostUSD   float64 `json:"est_cost_usd"`
	Tokens       int64   `json:"tokens"`

	// Raw counters behind the derived rates below. Exposed so a caller can audit the
	// arithmetic instead of taking a score on faith.
	Legal       int64   `json:"legal"`
	Fallbacks   int64   `json:"fallbacks"`
	Decisions   int64   `json:"decisions"`
	PlaySeconds float64 `json:"play_seconds"`

	// TokensPerMin is token throughput per minute of REAL match wall-clock time —
	// the practical "what will this model cost me to run" signal. 0 when no finished
	// match has contributed a duration yet.
	TokensPerMin float64 `json:"tokens_per_min"`
	// LegalRate and FallbackRate are the model's move quality: how often it produced a
	// legal move, and how often the engine had to substitute one because it did not.
	LegalRate    float64 `json:"legal_rate"`
	FallbackRate float64 `json:"fallback_rate"`
	// Intelligence is the 0..1000 score, computed with the SAME weights and
	// thresholds as the P-Index intelligence dimension (see migration 0051). Reusing
	// that formula matters: a second, differently-defined "intelligence" number on a
	// public page would contradict the one on developer profiles, and neither would
	// be trustworthy. 0 until the model has enough decisions to score honestly.
	Intelligence int `json:"intelligence"`
}

// Standing is one agent's season position (for "your rank this season").
type Standing struct {
	Season        int    `json:"season"`
	Game          string `json:"game"`
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

// RatingState is an agent's persisted rating for one arena, spanning both
// algorithms. Elo is the DISPLAYED rating for both (Glicko r, or a transform of the
// TrueSkill mean) so leaderboards sort uniformly regardless of algorithm. RD/Vol are
// Glicko-only; Mu/Sigma are TrueSkill-only.
type RatingState struct {
	Elo   int
	RD    float64
	Vol   float64
	Mu    float64
	Sigma float64
}

// ApplyPlayer is one participant in a rating update. Placement is the finishing rank
// (1 = best); equal placements are a tie.
type ApplyPlayer struct {
	AgentPublicID string
	Seat          int
	Placement     int
	CoinsDelta    int64
}

// ApplyInput is the resolved rating update for one finished match, generalized to N
// players and any arena. Compute keeps the rating math (Glicko-2 / TrueSkill) in this
// package, out of the store: the store reads each agent's current RatingState
// (aligned to Players by index), hands them + the placements to Compute, and writes
// back the new states Compute returns (same order).
type ApplyInput struct {
	MatchPublicID string
	Game          string
	Season        int
	Algo          string
	Players       []ApplyPlayer
	Compute       func(cur []RatingState, placements []int) []RatingState
}

// LeaderRow is one leaderboard entry (Rank is filled in by the service).
type LeaderRow struct {
	Rank          int     `json:"rank"`
	AgentPublicID string  `json:"agent"`
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	AvatarURL     string  `json:"avatar_url,omitempty"`
	Elo           int     `json:"elo"`
	RD            float64 `json:"rd"`    // Glicko rating deviation (lower = more certain)
	Trend         int     `json:"trend"` // rank movement since the last snapshot (+ = moved up)
	Wins          int     `json:"wins"`
	Losses        int     `json:"losses"`
	Ties          int     `json:"ties"`
	CoinsEarned   int64   `json:"coins_earned"`
	Streak        int     `json:"current_streak"`
}
