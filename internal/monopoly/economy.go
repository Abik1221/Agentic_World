package monopoly

// economy.go mirrors the Mafia pool math for Monopoly. Every real agent stakes
// the entry fee; the platform takes a rake; the remaining pool is paid to the
// single winning seat (last solvent player, or the top net worth at the turn
// cap) if that seat is held by a real agent. Bot winners keep nothing (the pool
// stays with the house). Practice tables (entry fee 0) settle to all zeros.

type EconomySnapshot struct {
	Agents         int   `json:"agents"`
	EntryFee       int64 `json:"entry_fee"`
	GrossPool      int64 `json:"gross_pool"`
	PlatformFeePct int   `json:"platform_fee_pct"`
	PlatformFee    int64 `json:"platform_fee"`
	RewardPool     int64 `json:"reward_pool"`
}

type RewardRow struct {
	Seat          int    `json:"seat"`
	AgentPublicID string `json:"agent_id,omitempty"`
	IsWinner      bool   `json:"is_winner"`
	Eligible      bool   `json:"eligible"`
	Payout        int64  `json:"payout"`
	Reason        string `json:"reason"`
}

// ComputeEconomy returns the pool breakdown for a table with agentCount real
// (staking) agents.
func ComputeEconomy(agentCount int, entryFee int64, platformFeePct int) EconomySnapshot {
	if platformFeePct < 0 {
		platformFeePct = DefaultPlatformFeePct
	}
	gross := int64(agentCount) * entryFee
	platform := gross * int64(platformFeePct) / 100
	return EconomySnapshot{
		Agents:         agentCount,
		EntryFee:       entryFee,
		GrossPool:      gross,
		PlatformFeePct: platformFeePct,
		PlatformFee:    platform,
		RewardPool:     gross - platform,
	}
}

// ComputeRewards settles the pool. The winning seat's agent (if any) takes the
// whole reward pool; every other agent loses their stake.
func ComputeRewards(winnerSeat int, agents []Player, econ EconomySnapshot) []RewardRow {
	out := make([]RewardRow, 0, len(agents))
	for _, p := range agents {
		row := RewardRow{Seat: p.Seat, AgentPublicID: p.AgentPublicID, IsWinner: p.Seat == winnerSeat}
		switch {
		case p.Seat == winnerSeat:
			row.Eligible = true
			row.Payout = econ.RewardPool
			row.Reason = "Winner · takes the pool"
		default:
			row.Reason = "Did not win"
		}
		out = append(out, row)
	}
	return out
}
