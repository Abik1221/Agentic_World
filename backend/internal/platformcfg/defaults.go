package platformcfg

import (
	"time"

	"github.com/agent-arena/arena/internal/config"
)

// Defaults builds the snapshot the engine runs on before it ever receives one
// from the Super Admin (and the floor every wire snapshot is merged over, so any
// omitted field keeps a sane value). Values mirror the boot-time env config and
// the Super Admin's seeded defaults, so behavior is unchanged until real config
// arrives.
func Defaults(cfg *config.Config) *Snapshot {
	turn := int(cfg.MoveWindow / time.Second)
	if turn <= 0 {
		turn = 20
	}
	return &Snapshot{
		Version: 0,
		// No active season by default: ranked play stays gated until the Super
		// Admin publishes a live season. Sandbox/practice is unaffected.
		Season: nil,
		Points: Points{
			Win: 10, Loss: 2, Draw: 5,
			ParticipationBonus: 1, WinStreakBonus: 3,
			TimeoutPenalty: -5, DisconnectPenalty: -8,
			IllegalMovePenalty: -10, AbandonPenalty: -15,
			MaxPointsPerDay: 500,
		},
		Ranking: Ranking{
			WinWeight: 1.0, OpponentStrengthWeight: 0.5,
			ConsistencyWeight: 0.3, ActivityWeight: 0.2,
			WinRateWeight: 0.4, PenaltyWeight: 1.0,
		},
		MatchRules: MatchRules{
			MinCoins: 50, MaxCoins: 5000,
			MinPlayers: 2, MaxPlayers: 15,
			TurnTimeoutSec: turn, MatchTimeoutSec: 1200,
			ReconnectTimeoutSec: 60, AIResponseTimeoutSec: 8,
			MaxDailyRankedMatches: 100,
			CertificationRequired: true,
		},
		Automation: Automation{
			AutoRewardDistribution: true, AutoHallOfFameUpdate: true,
			AutoLeaderboardRefresh: true, AutoArchive: true,
			MaxSeasonsStored: 50,
		},
		Rewards: nil,
		// Seeded from local config so that an admin who has NOT configured a field
		// leaves our value untouched: the published snapshot unmarshals over these,
		// so absent-on-the-wire means "keep ours". Every accessor on Snapshot takes
		// its fallback from here.
		Economy: Economy{
			PlatformCommissionPct: cfg.RakePct,
			MinWithdrawalCents:    cfg.WithdrawMinCoins * cfg.CoinCents,
			WithdrawFeePct:        cfg.WithdrawSellFeePct,
			DepositFeePct:         cfg.DepositFeePct,
			MinPurchaseCents:      cfg.DepositMinUSDC * 100,
			MinStakeUSDCents:      cfg.MinStakeUSDCents,
		},
		Flags: map[string]FeatureFlag{},
		// Empty supported-versions => the engine keeps its own built-in manifest
		// gate until the Documentation Service publishes an authoritative list.
		SDK:   SDKRequirements{MinSDKVersions: map[string]string{}, LatestSDKVersions: map[string]string{}},
		Games: nil,
	}
}
