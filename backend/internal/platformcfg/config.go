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
//
// Until now every field here except the commission was WRITE-ONLY: the Super Admin
// published them, the bus carried them, this struct held them, and no code read them.
// The operator could set a coin price or a minimum withdrawal and nothing changed,
// because the arena served its own env vars instead. The accessors below are the read
// side, and every one of them is bounded — these values arrive from another service,
// so a corrupt or hostile publisher must not be able to set a 100% fee or a zero
// minimum and drain the platform.
type Economy struct {
	PlatformCommissionPct int   `json:"platform_commission_pct"`
	CoinPriceCentsPer100  int64 `json:"coin_price_cents_per_100"`
	MinPurchaseCents      int64 `json:"min_purchase_cents"`
	MaxPurchaseCents      int64 `json:"max_purchase_cents"`
	MinWithdrawalCents    int64 `json:"min_withdrawal_cents"`
	MaxWithdrawalCents    int64 `json:"max_withdrawal_cents"`
	ReferralRewardCoins   int64 `json:"referral_reward_coins"`
	// WithdrawFeePct is the platform's cut on a cash-out. Same shape and same
	// reasoning as PlatformCommissionPct: the operator sets a percentage.
	WithdrawFeePct int `json:"withdraw_fee_pct"`
	// DepositFeePct is the cut on money coming IN. The charge existed but was
	// env-only, so the admin owned the fee on the way out and not the one on the
	// way in — an odd split for a screen whose whole job is the economy.
	DepositFeePct int `json:"deposit_fee_pct"`
	// MinStakeUSDCents is the paid-table floor the stake editor enforces. Admin-owned
	// so the floor is a policy decision rather than a constant compiled into a binary.
	MinStakeUSDCents int64 `json:"min_stake_usd_cents"`
}

// WithdrawFeePct is the live cash-out fee. Read when a withdrawal is REQUESTED; the
// resulting fee is persisted on the withdrawal row and payout settles from that, so
// changing the fee never re-prices a cash-out someone already asked for.
//
// Bounded 0..50: a fee above half the balance is indistinguishable from confiscation,
// and this value crosses a service boundary. 0 is legitimate (a fee-free promotion).
func (s *Snapshot) WithdrawFeePct(fallback int) int {
	if s == nil {
		return fallback
	}
	p := s.Economy.WithdrawFeePct
	if p < 0 || p > 50 {
		return fallback
	}
	return p
}

// DepositFeePct is the live entry fee. Bounded 0..50 like every other fee crossing
// the bus: above half is indistinguishable from confiscation, and 0 is a legitimate
// setting (a fee-free deposit promotion).
func (s *Snapshot) DepositFeePct(fallback int) int {
	if s == nil {
		return fallback
	}
	p := s.Economy.DepositFeePct
	if p < 0 || p > 50 {
		return fallback
	}
	return p
}

// MinWithdrawalCoins converts the admin's dollar minimum into coins at the given peg.
// Zero or negative (unset, or a nonsense peg) falls back — a zero minimum would let
// someone spam dust withdrawals, each of which costs a real on-chain fee the platform
// absorbs.
func (s *Snapshot) MinWithdrawalCoins(fallback, coinCents int64) int64 {
	if s == nil || coinCents <= 0 || s.Economy.MinWithdrawalCents <= 0 {
		return fallback
	}
	return s.Economy.MinWithdrawalCents / coinCents
}

// MinDepositCents is the smallest accepted top-up. Unset falls back.
func (s *Snapshot) MinDepositCents(fallback int64) int64 {
	if s == nil || s.Economy.MinPurchaseCents <= 0 {
		return fallback
	}
	return s.Economy.MinPurchaseCents
}

// MaxDepositCents is the largest accepted top-up. Unset, or below the minimum (which
// would reject every deposit), falls back.
func (s *Snapshot) MaxDepositCents(fallback int64) int64 {
	if s == nil || s.Economy.MaxPurchaseCents <= 0 {
		return fallback
	}
	if s.Economy.MinPurchaseCents > 0 && s.Economy.MaxPurchaseCents < s.Economy.MinPurchaseCents {
		return fallback
	}
	return s.Economy.MaxPurchaseCents
}

// MinStakeUSDCents is the admin-owned paid-table floor.
//
// No absence heuristic here, deliberately: the defaults snapshot is seeded from local
// config and the published snapshot unmarshals OVER it, so a field the admin never
// set is simply never on the wire and our own value survives untouched. That is the
// same presence mechanism the commission relies on. Inferring "unset" from a zero —
// or worse, from other fields also being zero — would make a legitimate 0 (a
// deliberately floorless sandbox) impossible to express.
//
// Only a negative value is rejected, because it is not a policy anyone can mean.
func (s *Snapshot) MinStakeUSDCents(fallback int64) int64 {
	if s == nil || s.Economy.MinStakeUSDCents < 0 {
		return fallback
	}
	return s.Economy.MinStakeUSDCents
}

// CommissionPct is the live platform rake, as a percentage, for a NEW match.
//
// Read at match creation only. The resulting percentage is persisted on the match row
// and settlement reads it back from there, so an admin changing the fee never
// retroactively alters a table that is already being played — a player is charged the
// rake that was displayed when they sat down, which is the only defensible rule.
//
// The bound is a safety rail on a value that arrives over the config bus from another
// service: anything negative or above 50% is treated as corrupt and the caller's own
// default is used instead. A misconfigured or hostile publisher must not be able to
// set a 100% rake and take the whole pot.
func (s *Snapshot) CommissionPct(fallback int) int {
	if s == nil {
		return fallback
	}
	p := s.Economy.PlatformCommissionPct
	if p < 0 || p > 50 {
		return fallback
	}
	return p
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
