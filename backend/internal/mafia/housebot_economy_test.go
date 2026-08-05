package mafia_test

import (
	"testing"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/mafia"
)

// A table the group matcher had to fill with house bots settles ONLY the coins its real
// agents staked. This is the invariant that keeps escrow balanced: the pool is computed
// from the staking seats, not from the roster.
func TestBotFilledTablePoolCountsStakersOnly(t *testing.T) {
	// Four real agents at 100 each on a twelve-seat table.
	econ := mafia.ComputeEconomy(4, 100, 10)
	if econ.GrossPool != 400 || econ.PlatformFee != 40 || econ.RewardPool != 360 {
		t.Fatalf("economy = %+v, want gross=400 fee=40 reward=360 (NOT a 12-seat pool)", econ)
	}
}

// A surviving house bot on the winning team must not be paid, and must not dilute the
// real winners' share by sitting in the denominator.
func TestComputeRewardsExcludesHouseSeats(t *testing.T) {
	econ := mafia.ComputeEconomy(4, 100, 10) // reward pool 360
	seats := []mafia.SeatInfo{
		{Seat: 1, Team: mf.TeamTown, Alive: true},                 // real winner
		{Seat: 2, Team: mf.TeamTown, Alive: true},                 // real winner
		{Seat: 3, Team: mf.TeamTown, Alive: true, IsHouse: true},  // bot, survived, winning team
		{Seat: 4, Team: mf.TeamMafia, Alive: true, IsHouse: true}, // bot, losing team
	}
	rewards := mafia.ComputeRewards(mf.TeamTown, seats, econ)

	const wantShare = int64(180) // 360 split TWO ways, not three
	var paid int64
	for _, r := range rewards {
		switch r.Seat {
		case 1, 2:
			if !r.Eligible || r.Payout != wantShare {
				t.Errorf("seat %d: eligible=%v payout=%d, want an eligible %d", r.Seat, r.Eligible, r.Payout, wantShare)
			}
		case 3, 4:
			if r.Eligible || r.Payout != 0 {
				t.Errorf("house seat %d must never be paid, got eligible=%v payout=%d", r.Seat, r.Eligible, r.Payout)
			}
			if r.Reason != "House bot · not staked" {
				t.Errorf("house seat %d reason = %q", r.Seat, r.Reason)
			}
		}
		paid += r.Payout
	}
	// Nothing may be paid out beyond the pool the humans funded.
	if paid > econ.RewardPool {
		t.Fatalf("paid %d from a %d pool — escrow would not balance", paid, econ.RewardPool)
	}
}

// A bot on the winning team must not shrink what the humans receive. Guards the specific
// mistake of filtering house seats at payout time but not before dividing the share.
func TestHouseSeatDoesNotDiluteWinners(t *testing.T) {
	// One real agent staked 100; the other seat is a filler. Rake-free to keep the
	// arithmetic obvious.
	econ := mafia.ComputeEconomy(1, 100, 0)
	withBot := mafia.ComputeRewards(mf.TeamTown, []mafia.SeatInfo{
		{Seat: 1, Team: mf.TeamTown, Alive: true},
		{Seat: 2, Team: mf.TeamTown, Alive: true, IsHouse: true},
	}, econ)
	if withBot[0].Payout != 100 {
		t.Fatalf("the sole real winner should take the whole 100 pool, got %d", withBot[0].Payout)
	}
}

// When the only survivors on the winning team are BOTS, no seat is eligible for a payout.
// That matters because it is the precondition finalize's no-winner guard keys on: an empty
// payout set makes it refund every staker instead of posting the whole pool to the platform
// as unpaid remainder. Bot backfill makes this state genuinely reachable, where before it
// was only narrowly so.
func TestBotOnlyWinningTeamPaysNobody(t *testing.T) {
	econ := mafia.ComputeEconomy(2, 100, 10)
	rewards := mafia.ComputeRewards(mf.TeamMafia, []mafia.SeatInfo{
		{Seat: 1, Team: mf.TeamTown, Alive: false},                 // real, eliminated
		{Seat: 2, Team: mf.TeamTown, Alive: false},                 // real, eliminated
		{Seat: 3, Team: mf.TeamMafia, Alive: true, IsHouse: true},  // bot, won
		{Seat: 4, Team: mf.TeamMafia, Alive: false, IsHouse: true}, // bot, eliminated
	}, econ)
	for _, r := range rewards {
		if r.Eligible || r.Payout != 0 {
			t.Fatalf("seat %d must not be payable when only bots survived: %+v", r.Seat, r)
		}
	}
}

// HumanPlayers / HasHouseSeat are what every money and rating decision keys on, so they
// are worth pinning directly.
func TestHumanPlayersAndHasHouseSeat(t *testing.T) {
	players := []mafia.Player{
		{AgentPublicID: "ag_real_1", Seat: 1},
		{AgentPublicID: "ag_house_mafia_01", Seat: 2, IsHouse: true},
		{AgentPublicID: "ag_real_2", Seat: 3},
	}
	humans := mafia.HumanPlayers(players)
	if len(humans) != 2 || humans[0].AgentPublicID != "ag_real_1" || humans[1].AgentPublicID != "ag_real_2" {
		t.Fatalf("HumanPlayers = %+v, want the two real agents in seat order", humans)
	}
	if !mafia.HasHouseSeat(players) {
		t.Error("HasHouseSeat should be true when a filler is present")
	}
	if mafia.HasHouseSeat(humans) {
		t.Error("HasHouseSeat should be false for an all-human table")
	}
	if len(mafia.HumanPlayers(nil)) != 0 {
		t.Error("HumanPlayers(nil) should be empty, not nil-panic")
	}
}
