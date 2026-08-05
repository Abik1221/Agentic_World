package mafia

import mf "github.com/agent-arena/arena/internal/engine/mafia"

// Economy defaults — must match Frontend/lib/mafiaEconomy.ts.
const (
	DefaultEntryFee       int64 = 100
	DefaultPlatformFeePct int   = 10
)

// EconomySnapshot is the public pool breakdown for spectators.
type EconomySnapshot struct {
	Agents         int   `json:"agents"`
	EntryFee       int64 `json:"entry_fee"`
	GrossPool      int64 `json:"gross_pool"`
	PlatformFeePct int   `json:"platform_fee_pct"`
	PlatformFee    int64 `json:"platform_fee"`
	RewardPool     int64 `json:"reward_pool"`
}

// RewardRow is one agent's settlement line.
type RewardRow struct {
	Seat          int    `json:"seat"`
	AgentPublicID string `json:"agent_id,omitempty"`
	Team          string `json:"team"`
	Alive         bool   `json:"alive"`
	OnWinningTeam bool   `json:"on_winning_team"`
	Eligible      bool   `json:"eligible"`
	Payout        int64  `json:"payout"`
	Reason        string `json:"reason"`
}

// ComputeEconomy mirrors computeEconomy() in mafiaEconomy.ts.
//
// agentCount must be the number of STAKING seats, not the roster size. Since group
// matchmaking can fill a short table with house bots, len(Match.Players) is no longer
// the same number: a bot staked nothing, so counting it here would compute a reward
// pool larger than the coins actually escrowed and settle a deficit against the
// platform. Callers pass len(HumanPlayers(...)).
func ComputeEconomy(agentCount int, entryFee int64, platformFeePct int) EconomySnapshot {
	// A FREE table has a zero economy — full stop.
	//
	// This used to silently rewrite entryFee 0 -> DefaultEntryFee (100), so a practice
	// table reported a 1200-coin gross pool, a 120-coin rake and real per-seat payouts.
	// The ledger was correctly skipped, but those phantom numbers were persisted to
	// match_players.coins_delta and then surfaced as fact: on the public spectator
	// economy endpoint, in the owner's lifetime-winnings tile, and in the spending-limit
	// engine (NetSince has no bid > 0 filter), where an unstaked practice "win" could
	// trip a take-profit stop. Monopoly already guarded this correctly; Mafia did not.
	//
	// Nothing was stolen — but it is exactly the fabricated data a free sandbox must not
	// produce, so zero in means zero out.
	if entryFee <= 0 {
		return EconomySnapshot{Agents: agentCount, PlatformFeePct: 0}
	}
	// Only a NEGATIVE percentage is nonsense; 0 is a legitimate rake-free table an
	// operator may configure (Monopoly already allows it).
	if platformFeePct < 0 {
		platformFeePct = DefaultPlatformFeePct
	}
	gross := int64(agentCount) * entryFee
	platform := gross * int64(platformFeePct) / 100
	return EconomySnapshot{
		Agents: agentCount, EntryFee: entryFee, GrossPool: gross,
		PlatformFeePct: platformFeePct, PlatformFee: platform, RewardPool: gross - platform,
	}
}

// SeatInfo is one seated agent with hidden role (server-side only until reveal).
type SeatInfo struct {
	Seat          int
	AgentPublicID string
	OwnerPublicID string
	Role          string
	Team          string
	Alive         bool
	// IsHouse marks an engine-driven filler seat. It staked nothing, so it can never
	// be paid from a pool it did not contribute to. See ComputeRewards.
	IsHouse bool
}

// ComputeRewards mirrors computeRewards() in mafiaEconomy.ts.
func ComputeRewards(winner string, seats []SeatInfo, econ EconomySnapshot) []RewardRow {
	var winners []SeatInfo
	for _, s := range seats {
		// A house filler is excluded from the winners set BEFORE the share is divided,
		// not just from the payout. Excluding it only at payout time would divide the
		// pool by a denominator that includes it and silently shrink every real
		// winner's share, leaving the difference to fall through to the platform as
		// unpaid remainder.
		if s.IsHouse {
			continue
		}
		if s.Alive && s.Team == winner {
			winners = append(winners, s)
		}
	}
	share := int64(0)
	if len(winners) > 0 {
		share = econ.RewardPool / int64(len(winners))
	}
	out := make([]RewardRow, 0, len(seats))
	for _, s := range seats {
		row := RewardRow{
			Seat: s.Seat, AgentPublicID: s.AgentPublicID, Team: s.Team,
			Alive: s.Alive, OnWinningTeam: s.Team == winner,
		}
		switch {
		case s.IsHouse:
			// Checked first: a house bot on the winning team that survived would
			// otherwise fall through to the paid branch and move real coins to the
			// system owner's wallet.
			row.Reason = "House bot · not staked"
		case s.Team != winner:
			row.Reason = "Losing team"
		case !s.Alive:
			row.Reason = "Eliminated before victory"
		default:
			row.Eligible = true
			row.Payout = share
			row.Reason = "Winning team · survived"
		}
		out = append(out, row)
	}
	return out
}

// SeatsFromState builds seat infos from persisted roles + alive map.
func SeatsFromState(seats []SeatInfo, st mf.State) []SeatInfo {
	out := make([]SeatInfo, len(seats))
	copy(out, seats)
	for i := range out {
		out[i].Alive = st.Alive[out[i].Seat]
		out[i].Role = st.Roles[out[i].Seat]
		out[i].Team = mf.TeamOf(out[i].Role)
	}
	return out
}
