package mafia

import (
	"testing"

	"github.com/agent-arena/arena/internal/benchmark"
	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/turnproof"
)

// A seat's result is its TEAM's result. Getting this wrong would invert win rates for half the
// table at once, and it is the kind of error that looks plausible on a board.
func TestMafiaSeatResultFollowsTheTeam(t *testing.T) {
	st := mf.State{
		Finished: true,
		Winner:   mf.TeamTown,
		Roles:    map[int]string{1: "Villager", 2: "Mafia", 3: "Doctor", 4: "Mafia"},
	}
	for seat, want := range map[int]benchmark.Result{
		1: benchmark.ResultWin,  // town
		3: benchmark.ResultWin,  // town (Doctor)
		2: benchmark.ResultLoss, // mafia
		4: benchmark.ResultLoss,
	} {
		if got := mafiaSeatResult(st, seat); got != want {
			t.Errorf("seat %d (%s) = %q, want %q", seat, st.Roles[seat], got, want)
		}
	}

	// Two seats on the same team must always agree, and two on opposite teams must never.
	if mafiaSeatResult(st, 1) != mafiaSeatResult(st, 3) {
		t.Error("two town seats disagreed on the outcome of a team game")
	}
	if mafiaSeatResult(st, 1) == mafiaSeatResult(st, 2) {
		t.Error("a town seat and a mafia seat shared an outcome")
	}
}

func TestMafiaSeatResultWithNoWinnerIsADrawNotALossForEveryone(t *testing.T) {
	// A finished state with no winner recorded is a draw. The zero value would have made it a
	// loss for every seat, which is a claim the data does not support.
	st := mf.State{Finished: true, Roles: map[int]string{1: "Villager", 2: "Mafia"}}
	for _, seat := range []int{1, 2} {
		if got := mafiaSeatResult(st, seat); got != benchmark.ResultDraw {
			t.Errorf("seat %d = %q, want draw", seat, got)
		}
	}
}

// The decision key must separate the two phases of one day.
//
// Mafia's move-signature sequence is State.Day, which is NOT unique per decision: a player acts
// in both the night and the voting phase of the same day. Using it as the decision seq would make
// the second action overwrite the first, so a two-phase day would record one decision instead of
// two — halving every Mafia agent's decision count and every rate computed from it.
func TestTheDecisionKeySeparatesPhasesWithinADay(t *testing.T) {
	night := turnproof.MafiaTurn(3, "night")
	voting := turnproof.MafiaTurn(3, "voting")
	if night == voting {
		t.Fatalf("night and voting on day 3 share seq %d — one decision would overwrite the other",
			night)
	}
	// And days must not collide with each other either.
	if turnproof.MafiaTurn(3, "voting") == turnproof.MafiaTurn(4, "night") {
		t.Error("day 3 voting collides with day 4 night")
	}
	// Monotonic across days, so the log's ordering is the game's ordering.
	if turnproof.MafiaTurn(4, "night") <= turnproof.MafiaTurn(3, "voting") {
		t.Error("the sequence is not monotonic across days")
	}
}
