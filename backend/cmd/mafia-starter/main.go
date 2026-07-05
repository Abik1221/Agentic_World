// Command mafia-starter is a COPY-ME example: it shows how to write your own Mafia
// agent, seat it against the sandbox's bots, play a full game, and verify the
// result. Copy MyAgent into your own project and make it smarter.
//
//	go run ./cmd/mafia-starter
package main

import (
	"fmt"
	"sort"

	"github.com/agent-arena/arena/internal/engine/mafia"
)

// MyAgent implements mafia.Agent. Mafia is a hidden-role game, so your agent only
// ever receives its REDACTED view: its own role/team, its allies if it is Mafia,
// the public transcript (view.Public), and its OWN night results (view.Private).
// It can never see another player's role. Replace the body with real strategy —
// e.g. a Detective should read view.Private and vote the player it found guilty.
type MyAgent struct{}

func (MyAgent) Name() string { return "MyAgent" }

func (MyAgent) Decide(v mafia.AgentView) mafia.Action {
	if len(v.Legal) == 0 {
		return mafia.Action{}
	}
	switch v.Legal[0] {
	case mafia.ActMessage:
		return mafia.Action{Kind: mafia.ActMessage, Tone: "info", Text: "I'm reading the table."}
	case mafia.ActNightKill, mafia.ActInvestigate, mafia.ActProtect, mafia.ActProfile, mafia.ActVote:
		return mafia.Action{Kind: v.Legal[0], Target: firstAliveOther(v)}
	}
	return mafia.Action{}
}

// firstAliveOther returns the lowest living seat that isn't us (deterministic).
func firstAliveOther(v mafia.AgentView) int {
	var alive []int
	for seat, ok := range v.Alive {
		if ok && seat != v.Seat {
			alive = append(alive, seat)
		}
	}
	sort.Ints(alive)
	if len(alive) > 0 {
		return alive[0]
	}
	return v.Seat
}

func main() {
	seed := []byte("starter")
	const maxDays = 60
	seats := mafia.StandardSeats()

	// Seat 1 is your agent; the rest are the sandbox's bots.
	agents := map[int]mafia.Agent{1: MyAgent{}}
	for _, seat := range seats {
		if seat != 1 {
			agents[seat] = mafia.NewBot(fmt.Sprintf("Bot%d", seat), seed, seat)
		}
	}
	tbl := mafia.NewTable(seats, seed, agents, maxDays)
	tbl.PlayOut()

	fmt.Printf("Game over. Winner: %s\n", tbl.Winner())
	fmt.Printf("Your (seat 1) role was: %s — %s\n", tbl.RoleOf(1), aliveWord(tbl.State().Alive[1]))

	ok, err := mafia.Verify(seats, seed, maxDays, tbl.Moves(), tbl.Log())
	fmt.Printf("Replay verified: %v (err=%v)\n", ok, err)
	fmt.Printf("Replay hash: %s\n", tbl.ReplayHash())
}

func aliveWord(alive bool) string {
	if alive {
		return "survived"
	}
	return "eliminated"
}
