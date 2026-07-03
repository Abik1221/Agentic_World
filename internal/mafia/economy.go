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
	Seat           int    `json:"seat"`
	AgentPublicID  string `json:"agent_id,omitempty"`
	Team           string `json:"team"`
	Alive          bool   `json:"alive"`
	OnWinningTeam  bool   `json:"on_winning_team"`
	Eligible       bool   `json:"eligible"`
	Payout         int64  `json:"payout"`
	Reason         string `json:"reason"`
}

// ComputeEconomy mirrors computeEconomy() in mafiaEconomy.ts.
func ComputeEconomy(agentCount int, entryFee int64, platformFeePct int) EconomySnapshot {
	if entryFee <= 0 {
		entryFee = DefaultEntryFee
	}
	if platformFeePct <= 0 {
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
}

// ComputeRewards mirrors computeRewards() in mafiaEconomy.ts.
func ComputeRewards(winner string, seats []SeatInfo, econ EconomySnapshot) []RewardRow {
	var winners []SeatInfo
	for _, s := range seats {
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
