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

	// ModelBenchmark aggregates a season's per-match benchmark facts by the model that
	// actually played each match (see the attribution tiers above), for one arena or —
	// when game is "" — for all of them, with a per-arena breakdown attached.
	//
	// The window is [start, end) over matches.finished_at rather than a season number,
	// because the facts it aggregates live on matches, which carry a timestamp and not
	// a season. Rows are returned unfiltered; minimum-sample policy is the service's.
	ModelBenchmark(ctx context.Context, season int, game string, start, end time.Time) ([]ModelStat, error)

	// AgentStanding returns one agent's place in an arena for the season (rank,
	// totals, declared model). found=false when the agent has no rating row.
	AgentStanding(ctx context.Context, season int, game, agentPublicID string) (standing Standing, found bool, err error)

	// ModelRunners lists the agents (with their owners) that played one model in the
	// window, resolved through the SAME attribution ladder as ModelBenchmark.
	ModelRunners(ctx context.Context, season int, game, provider, model string, start, end time.Time) ([]ModelRunner, error)

	// DeveloperModelSplit returns each developer's record on each model they ran — the
	// input to the skill-above-model edge.
	DeveloperModelSplit(ctx context.Context, season int, game string, start, end time.Time) ([]DevModelRow, error)
}

// ModelRunner is one agent running a model this season, with its owner — the "who is
// actually using this" row on a model's detail page.
//
// Every field is already public elsewhere (the leaderboard shows agent names and ELO;
// developer profiles are public pages). This is a different arrangement of that data,
// not a new disclosure.
type ModelRunner struct {
	AgentPublicID  string `json:"agent"`
	AgentName      string `json:"agent_name"`
	AgentSlug      string `json:"agent_slug"`
	AgentAvatarURL string `json:"agent_avatar_url,omitempty"`

	// The developer who chose it. Username may be empty for an account that has not
	// claimed a public handle — such a row still counts toward adoption but cannot be
	// linked to a profile.
	Username           string `json:"username,omitempty"`
	DisplayName        string `json:"display_name,omitempty"`
	DeveloperAvatarURL string `json:"developer_avatar_url,omitempty"`

	Matches int `json:"matches"`
	Wins    int `json:"wins"`
	Losses  int `json:"losses"`
	Ties    int `json:"ties"`
	// Elo is the agent's best rating among the arenas it played this model in.
	Elo int `json:"elo"`

	Tokens          int64   `json:"tokens"`
	Decisions       int64   `json:"decisions"`
	EstCostUSD      float64 `json:"est_cost_usd"`
	VerifiedCostUSD float64 `json:"verified_cost_usd"`

	// Derived by the service, on the same definitions as everywhere else.
	WinRate   float64 `json:"win_rate"`
	WinRateCI float64 `json:"win_rate_ci"`
}

// Model-attribution tiers, in descending order of trust. Every row on the model
// board carries the tier it was resolved at, because "the provider's own response
// said this model" and "the developer typed this model into a YAML file" are not
// the same claim and must not be presented as if they were.
const (
	// AttrVerified — the model name the upstream provider returned, read off the
	// response by the Pyyol gateway. The agent cannot misreport it.
	AttrVerified = "verified"
	// AttrObserved — the model the SDK reported for the call it actually made during
	// the match. Self-reported, but per-call and per-match.
	AttrObserved = "observed"
	// AttrDeclared — the manifest's `model:` block. Self-reported and static.
	AttrDeclared = "declared"
)

// attributionName maps the SQL rank (1=best) to its tier name.
func attributionName(rank int) string {
	switch rank {
	case 1:
		return AttrVerified
	case 2:
		return AttrObserved
	default:
		return AttrDeclared
	}
}

// Which USD figure a cost-per-match / cost-per-win column was divided from. Published
// alongside the numbers because "the gateway watched this cost $0.04" and "the agent
// says this cost $0.04" are not interchangeable claims.
const (
	CostVerified     = "verified"      // gateway-observed; the agent cannot understate it
	CostSelfReported = "self-reported" // SDK-metered
)

// ModelStat is one model's aggregate performance for a season, across one arena or
// all of them.
//
// Two different denominators appear here on purpose, and conflating them is how a
// board starts lying:
//
//   - Matches — every benchmarked match the model played. The denominator for
//     ECONOMICS (tokens, cost, wall-clock), because a match burns tokens whether or
//     not its outcome was recorded.
//   - Games — matches whose per-seat result is known. The denominator for OUTCOMES
//     (win rate). Rows written before the result column existed count toward Matches
//     but not Games, so a win rate is never computed over matches it cannot see.
type ModelStat struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Attribution is the BEST tier at which this model was identified across its
	// matches (see AttrVerified/AttrObserved/AttrDeclared).
	Attribution string `json:"attribution"`
	// AttrRank is the numeric tier the store resolved (1=verified … 3=declared), the
	// input Attribution is rendered from. Not serialized: one tier on the wire, so a
	// client cannot read a name and a number that disagree.
	AttrRank int `json:"-"`
	// Class places this model on every comparison axis (vendor, family, open-weights
	// vs proprietary, hosted vs self-hosted). Derived, so a model first seen today is
	// classified today.
	Class ModelClass `json:"class"`

	// Agents is how many agents ran this model; Developers how many distinct PEOPLE
	// chose it. Adoption is the second number: one developer running twelve agents is
	// not twelve people betting on a model, and presenting agent count as popularity
	// would let a single prolific account look like a trend.
	Agents     int `json:"agents"`
	Developers int `json:"developers"`

	Matches  int     `json:"matches"`
	Games    int     `json:"games"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	Ties     int     `json:"ties"`
	AvgElo   int     `json:"avg_elo"`
	CoinsWon int64   `json:"coins_won"`
	WinRate  float64 `json:"win_rate"`
	// WinRateCI is the half-width of the 95% Wilson score interval on WinRate. A
	// leaderboard that prints 62% off eight games and 62% off eight hundred, in the
	// same column, invites a conclusion the data cannot support — this is the ± that
	// says which is which.
	WinRateCI float64 `json:"win_rate_ci"`
	// Preliminary marks a sample too small to rank on, mirroring how the established
	// arenas tag low-vote models. The row is still shown: hiding it would make the
	// board look complete when it is not.
	Preliminary bool `json:"preliminary"`

	// Verified is how much of this row's play was actually PROVEN LLM-backed, not whether
	// any of it was. Published because the attribution tier above is derived from it, so a
	// reader who distrusts our threshold can ignore the label and read the fraction. See
	// coverage.go for why the denominator is what makes the numerator safe to publish.
	Verified CoverageStat `json:"verified"`

	// ── time ──────────────────────────────────────────────────────────────────
	// PlaySeconds is summed REAL match wall-clock; TimedMatches is how many matches
	// contributed one (the correct denominator — a match with a broken clock must not
	// drag the average toward zero).
	PlaySeconds     float64 `json:"play_seconds"`
	TimedMatches    int     `json:"timed_matches"`
	AvgMatchSeconds float64 `json:"avg_match_seconds"`

	// ── decisions & move quality ──────────────────────────────────────────────
	Decisions         int64   `json:"decisions"`
	Legal             int64   `json:"legal"`
	Fallbacks         int64   `json:"fallbacks"`
	Illegal           int64   `json:"illegal"`
	Timeouts          int64   `json:"timeouts"`
	TransportErrors   int64   `json:"transport_errors"`
	LegalRate         float64 `json:"legal_rate"`
	FallbackRate      float64 `json:"fallback_rate"`
	DecisionsPerMatch float64 `json:"decisions_per_match"`
	AvgLatencyMs      int     `json:"avg_latency_ms"`
	MinLatencyMs      int     `json:"min_latency_ms"`
	MaxLatencyMs      int     `json:"max_latency_ms"`

	// ── tokens ────────────────────────────────────────────────────────────────
	// The split matters: "what does this model burn" is really two questions (how
	// much context it needs vs. how much it generates), and cached tokens are the
	// difference between a model that looks expensive and one that is.
	Tokens           int64 `json:"tokens"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	// Normalized token figures. Cumulative totals rank whoever played most; these
	// rank the model.
	TokensPerMin      float64 `json:"tokens_per_min"`
	TokensPerMatch    float64 `json:"tokens_per_match"`
	TokensPerDecision float64 `json:"tokens_per_decision"`
	TokensPerWin      float64 `json:"tokens_per_win"`

	// ── economics ─────────────────────────────────────────────────────────────
	// EstCostUSD is SELF-REPORTED (SDK-metered) and therefore gameable.
	// VerifiedCostUSD is gateway-observed and is not. Both are exposed rather than
	// blended, so a reader can see which of the two a model's cost claim rests on.
	EstCostUSD      float64 `json:"est_cost_usd"`
	VerifiedCostUSD float64 `json:"verified_cost_usd"`
	VerifiedCalls   int64   `json:"verified_calls"`
	CostPerMatch    float64 `json:"cost_per_match"`
	CostPerWin      float64 `json:"cost_per_win"`
	// CostBasis names which of the two totals above CostPerMatch/CostPerWin were
	// divided from (CostVerified or CostSelfReported); empty when no cost is known.
	CostBasis string `json:"cost_basis,omitempty"`

	// Intelligence is the 0..1000 score, computed with the SAME weights and
	// thresholds as the P-Index intelligence dimension (see migration 0051). Reusing
	// that formula matters: a second, differently-defined "intelligence" number on a
	// public page would contradict the one on developer profiles, and neither would
	// be trustworthy. 0 until the model has enough decisions to score honestly.
	Intelligence int `json:"intelligence"`

	// Arenas is the same metric set split per arena, present only on the all-arena
	// aggregate. A single blended row cannot answer "is this model good at Mafia or
	// just good at Goofspiel", which is the first thing a developer needs to know.
	Arenas []ArenaStat `json:"arenas,omitempty"`
}

// ArenaStat is one model's performance in ONE arena — the per-game breakdown behind
// an aggregate ModelStat. Deliberately a subset: the figures that differ by arena and
// that a reader would otherwise have to take on trust.
type ArenaStat struct {
	Game            string  `json:"game"`
	Agents          int     `json:"agents"`
	Developers      int     `json:"developers"`
	Matches         int     `json:"matches"`
	Games           int     `json:"games"`
	Wins            int     `json:"wins"`
	Losses          int     `json:"losses"`
	Ties            int     `json:"ties"`
	WinRate         float64 `json:"win_rate"`
	WinRateCI       float64 `json:"win_rate_ci"`
	AvgElo          int     `json:"avg_elo"`
	CoinsWon        int64   `json:"coins_won"`
	Decisions       int64   `json:"decisions"`
	Tokens          int64   `json:"tokens"`
	TokensPerMatch  float64 `json:"tokens_per_match"`
	AvgLatencyMs    int     `json:"avg_latency_ms"`
	AvgMatchSeconds float64 `json:"avg_match_seconds"`
	EstCostUSD      float64 `json:"est_cost_usd"`
	VerifiedCostUSD float64 `json:"verified_cost_usd"`
	LegalRate       float64 `json:"legal_rate"`

	// Raw inputs the store reads and the service divides. Exported only because the
	// two live in different packages; never serialized — the API exposes the derived
	// figure, and a second copy of the same fact on the wire is a second thing that
	// can disagree with it.
	PlaySecondsInternal  float64 `json:"-"`
	TimedMatchesInternal int     `json:"-"`
	LegalInternal        int64   `json:"-"`
}

// Standing is one agent's season position (for "your rank this season").
type Standing struct {
	Season int    `json:"season"`
	Game   string `json:"game"`
	// Rank and Total count the PUBLISHED population only — the agents that appear on the
	// ladder — so this number can be compared with what the board shows.
	Rank  int `json:"rank"`
	Total int `json:"total"`
	// Ranked reports whether this agent is on the published ladder at all.
	//
	// An agent that never routes a model call may play staked tables and win coins; it is
	// excluded from ranked surfaces because the arena cannot say a model chose its moves. That
	// exclusion has to be VISIBLE here, on the agent's own card. Silently omitting a developer
	// from the ladder while still showing them a rank is the one outcome this policy must not
	// produce — they would go looking for themselves and find a gap, with nothing telling them
	// why or what to do about it.
	//
	// When false, Rank is this agent's position among published agents had it been published,
	// which is what makes it actionable: it is the rank verifying would earn.
	Ranked        bool   `json:"ranked"`
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
	// Attribution is where Provider/Model came from, and it is not decoration.
	//
	// This card used to read them straight out of agent_manifests — the developer's own YAML —
	// and print them with no tag, so a model nobody ever confirmed looked identical to one the
	// provider's API named. The model board has always been careful to call that "claimed";
	// the agent's own standing was not.
	Attribution string `json:"attribution,omitempty"`
	// Verified is how much of this agent's play was proven LLM-backed. Published for the same
	// reason as on the model board: the tier is a summary of a threshold, and a reader who
	// cannot see the fraction is being asked to take the threshold on trust.
	Verified CoverageStat `json:"verified"`
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
