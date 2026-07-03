package mafia_test

import (
	"testing"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/mafia"
)

func TestComputeEconomyMatchesFrontend(t *testing.T) {
	econ := mafia.ComputeEconomy(12, 100, 10)
	if econ.GrossPool != 1200 || econ.PlatformFee != 120 || econ.RewardPool != 1080 {
		t.Fatalf("economy = %+v, want gross=1200 fee=120 reward=1080", econ)
	}
}

func TestComputeRewardsWinningSurvivorsOnly(t *testing.T) {
	econ := mafia.ComputeEconomy(12, 100, 10)
	seats := []mafia.SeatInfo{
		{Seat: 1, Team: mf.TeamTown, Alive: true},
		{Seat: 2, Team: mf.TeamMafia, Alive: false},
		{Seat: 3, Team: mf.TeamTown, Alive: true},
		{Seat: 4, Team: mf.TeamTown, Alive: false},
	}
	rewards := mafia.ComputeRewards(mf.TeamTown, seats, econ)
	share := int64(540) // 1080 / 2
	for _, r := range rewards {
		switch r.Seat {
		case 1, 3:
			if !r.Eligible || r.Payout != share {
				t.Fatalf("seat %d: eligible=%v payout=%d want %d", r.Seat, r.Eligible, r.Payout, share)
			}
		case 2:
			if r.Payout != 0 || r.Reason != "Losing team" {
				t.Fatalf("seat 2: %+v", r)
			}
		case 4:
			if r.Payout != 0 || r.Reason != "Eliminated before victory" {
				t.Fatalf("seat 4: %+v", r)
			}
		}
	}
}
