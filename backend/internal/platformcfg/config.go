// Package platformcfg is the engine-side consumer of the platform configuration
// the Super Admin owns. The Super Admin is the source of truth; this package
// keeps a locally-cached, atomically-swappable snapshot that the rest of the
// engine reads instead of hardcoding business rules.
//
// It is transport-agnostic: it depends on a Source (implemented in internal/store
// over Redis) and never imports a driver. The wire contract lives in
// docs/architecture/platform-config-bus.md.
//
// Resilience contract: the engine must keep running on its last-known-good
// snapshot — or on built-in env defaults if it never saw one — even while the
// Super Admin is unreachable. Nothing here ever blocks or fails a match because
// config is stale or missing.
package platformcfg

import "time"

// Snapshot is the full config bundle the engine consumes. Every field is optional
// on the wire: an absent JSON key leaves the corresponding env default in place
// (see Provider.parse, which unmarshals over a copy of the defaults), so the two
// services deploy and evolve independently.
type Snapshot struct {
	Version     int64      `json:"version"`
	GeneratedAt time.Time  `json:"generated_at"`
	Season      *Season    `json:"active_season"`
	Points      Points     `json:"points"`
	Ranking     Ranking    `json:"ranking"`
	MatchRules  MatchRules `json:"match_rules"`
	Automation  Automation `json:"automation"`
	Rewards     []Reward   `json:"rewards"`
	Economy     Economy    `json:"economy"`
	// Flags is keyed by flag key; a missing key means "use the caller's default".
	Flags map[string]FeatureFlag `json:"feature_flags"`
	SDK   SDKRequirements        `json:"sdk_requirements"`
	Games []Game                 `json:"games"`
	// Suspended is the set of agent public ids the Super Admin has suspended.
	// The engine blocks them from ranked play (see IsSuspended). Carried as a
	// list on the wire; callers use the O(1) helper.
	Suspended []string `json:"suspended_agents"`
}

// Season is the active competitive window. Nil when no season is live, in which
// case ranked play is rejected (the engine is the referee, not the scheduler).
type Season struct {
	Code                string `json:"code"`
	Name                string `json:"name"`
	Number              int    `json:"number"`
	Status              string `json:"status"` // draft|scheduled|live|paused|completed|archived
	RankedEnabled       bool   `json:"ranked_enabled"`
	RegistrationEnabled bool   `json:"registration_enabled"`
	// Dates are opaque strings (the Super Admin stores them free-form): the engine
	// gates on Status/RankedEnabled, not on parsing these, so a non-RFC3339 value
	// can never fail the whole snapshot parse.
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

// Points is the configurable per-match scoring inputs (season_settings=points).
type Points struct {
	Win                int `json:"win"`
	Loss               int `json:"loss"`
	Draw               int `json:"draw"`
	ParticipationBonus int `json:"participation_bonus"`
	WinStreakBonus     int `json:"win_streak_bonus"`
	TimeoutPenalty     int `json:"timeout_penalty"`
	DisconnectPenalty  int `json:"disconnect_penalty"`
	IllegalMovePenalty int `json:"illegal_move_penalty"`
	AbandonPenalty     int `json:"abandon_penalty"`
	MaxPointsPerDay    int `json:"max_points_per_day"`
}

// Ranking is the configurable season-score formula weights (season_settings=ranking).
type Ranking struct {
	WinWeight              float64 `json:"win_weight"`
	OpponentStrengthWeight float64 `json:"opponent_strength_weight"`
	ConsistencyWeight      float64 `json:"consistency_weight"`
	ActivityWeight         float64 `json:"activity_weight"`
	WinRateWeight          float64 `json:"win_rate_weight"`
	PenaltyWeight          float64 `json:"penalty_weight"`
}

// MatchRules gates ranked matches (season_settings=match_rules). Timeouts are
// carried as seconds on the wire; use the Duration helpers below.
type MatchRules struct {
	MinCoins              int64 `json:"min_coins"`
	MaxCoins              int64 `json:"max_coins"`
	MinPlayers            int   `json:"min_players"`
	MaxPlayers            int   `json:"max_players"`
	TurnTimeoutSec        int   `json:"turn_timeout_sec"`
	MatchTimeoutSec       int   `json:"match_timeout_sec"`
	ReconnectTimeoutSec   int   `json:"reconnect_timeout_sec"`
	AIResponseTimeoutSec  int   `json:"ai_response_timeout_sec"`
	MaxDailyRankedMatches int   `json:"max_daily_ranked_matches"`
	CertificationRequired bool  `json:"certification_required"`
}

// TurnTimeout is the per-move window as a duration (0 => caller's default).
func (m MatchRules) TurnTimeout() time.Duration { return time.Duration(m.TurnTimeoutSec) * time.Second }

// AIResponseTimeout is the agent response deadline as a duration.
func (m MatchRules) AIResponseTimeout() time.Duration {
	return time.Duration(m.AIResponseTimeoutSec) * time.Second
}

// Automation toggles the season lifecycle jobs (season_settings=automation).
type Automation struct {
	AutoRewardDistribution bool `json:"auto_reward_distribution"`
	AutoHallOfFameUpdate   bool `json:"auto_hall_of_fame_update"`
	AutoLeaderboardRefresh bool `json:"auto_leaderboard_refresh"`
	AutoArchive            bool `json:"auto_archive"`
	MaxSeasonsStored       int  `json:"max_seasons_stored"`
}

// Reward is one payout tier granted when a season ends (season_rewards).
type Reward struct {
	Tier        string `json:"tier"`
	RewardType  string `json:"reward_type"` // coins|credits|badge|verification|hall_of_fame|avatar|frame|beta_access
	Amount      int64  `json:"amount"`
	Description string `json:"description"`
	Ordering    int    `json:"ordering"`
}

// Economy is the coin economy + platform fees (economy_config, typed).
type Economy struct {
	PlatformCommissionPct int   `json:"platform_commission_pct"`
	CoinPriceCentsPer100  int64 `json:"coin_price_cents_per_100"`
	MinPurchaseCents      int64 `json:"min_purchase_cents"`
	MaxPurchaseCents      int64 `json:"max_purchase_cents"`
	MinWithdrawalCents    int64 `json:"min_withdrawal_cents"`
	MaxWithdrawalCents    int64 `json:"max_withdrawal_cents"`
	ReferralRewardCoins   int64 `json:"referral_reward_coins"`
}

// FeatureFlag is a runtime toggle with an optional rollout descriptor.
type FeatureFlag struct {
	Enabled bool   `json:"enabled"`
	Rollout string `json:"rollout"`
}

// SDKRequirements is the Documentation-Service-driven version compatibility gate.
type SDKRequirements struct {
	SupportedManifestVersions []string `json:"supported_manifest_versions"`
	// MinSDKVersions is the minimum supported SDK version per language
	// ("python"/"js"); a connecting agent below it is refused. Empty ⇒ no floor.
	MinSDKVersions map[string]string `json:"min_sdk_versions"`
	// LatestSDKVersions is the newest published SDK version per language; the
	// gateway echoes it so the SDK can print a one-line "upgrade available" notice.
	LatestSDKVersions map[string]string `json:"latest_sdk_versions"`
	DocsBaseURL       string            `json:"docs_base_url"`
}

// Game is a registered game and its live engine version.
type Game struct {
	Code   string `json:"code"`
	Engine string `json:"engine"`
	Live   bool   `json:"live"`
}

// HasActiveSeason reports whether a live season exists.
func (s *Snapshot) HasActiveSeason() bool { return s.Season != nil && s.Season.Status == "live" }

// RankedAllowed reports whether ranked matches may run right now: a live season
// with ranked play enabled. The engine rejects ranked matches otherwise.
func (s *Snapshot) RankedAllowed() bool { return s.HasActiveSeason() && s.Season.RankedEnabled }

// IsSuspended reports whether an agent has been suspended by the Super Admin.
// The list is small (moderation actions), so a linear scan is fine and avoids
// building a set on every read; callers hit this once per match entry.
func (s *Snapshot) IsSuspended(agentPublicID string) bool {
	for _, id := range s.Suspended {
		if id == agentPublicID {
			return true
		}
	}
	return false
}

// FlagEnabled reports whether a feature flag is on; unknown flags fall back to def.
func (s *Snapshot) FlagEnabled(key string, def bool) bool {
	if f, ok := s.Flags[key]; ok {
		return f.Enabled
	}
	return def
}

// ManifestVersionSupported reports whether a manifest spec version is accepted.
// An empty supported-list means "don't gate here" (the engine keeps its own).
func (s *Snapshot) ManifestVersionSupported(v string) bool {
	if len(s.SDK.SupportedManifestVersions) == 0 {
		return true
	}
	for _, sv := range s.SDK.SupportedManifestVersions {
		if sv == v {
			return true
		}
	}
	return false
}
