// Command monopoly-starter is a COPY-ME example: it shows how to write your own
// Monopoly agent, seat it against the sandbox's bots, play a full game, and verify
// the result. Copy MyAgent into your own project, make it smarter, and you have a
// platform-ready agent.
//
//	go run ./cmd/monopoly-starter
package main

import (
	"fmt"

	"github.com/agent-arena/arena/internal/engine/monopoly"
)

// MyAgent implements monopoly.Agent. Monopoly is perfect-information apart from
// future dice (which are NOT in the state), so your agent receives the full public
// State and the engine (for engine.LegalActions). This baseline only ever returns
// a legal action; replace the body with real strategy.
type MyAgent struct{}

func (MyAgent) Name() string { return "MyAgent" }

func (MyAgent) Decide(e *monopoly.Engine, s monopoly.State, seat int) monopoly.Action {
	legal := e.LegalActions(s, seat)
	can := func(k string) bool {
		for _, x := range legal {
			if x == k {
				return true
			}
		}
		return false
	}
	switch {
	case can(monopoly.ActRoll):
		return monopoly.Action{Kind: monopoly.ActRoll}
	case can(monopoly.ActPayJail): // buy our way out when we can afford it
		return monopoly.Action{Kind: monopoly.ActPayJail}
	case can(monopoly.ActRollJail):
		return monopoly.Action{Kind: monopoly.ActRollJail}
	case can(monopoly.ActBuy): // LegalActions only offers Buy when it is affordable
		return monopoly.Action{Kind: monopoly.ActBuy}
	case can(monopoly.ActPass): // an auction we choose not to chase
		return monopoly.Action{Kind: monopoly.ActPass}
	case can(monopoly.ActBankrupt): // out of cash and nothing left to liquidate
		return monopoly.Action{Kind: monopoly.ActBankrupt}
	default:
		return monopoly.Action{Kind: monopoly.ActEndTurn}
	}
}

func main() {
	cfg := monopoly.Config{Players: 4, StartingCash: 1500, MaxTurns: 1000}
	seed := []byte("starter")

	// Seat 0 is your agent; the remaining seats auto-fill with the sandbox bots.
	tbl := monopoly.NewTable(cfg, seed, []monopoly.Agent{MyAgent{}})
	tbl.PlayOut()

	winner := "a tie"
	if tbl.Winner() != monopoly.Tie {
		winner = fmt.Sprintf("seat %d (%s)", tbl.Winner(), tbl.SeatName(tbl.Winner()))
	}
	fmt.Printf("Game over after %d rolls. Winner: %s\n", tbl.State().TurnCount, winner)
	fmt.Printf("Your (seat 0) net worth: $%d\n", tbl.State().NetWorth(0))

	// Prove the match is legitimate: replay the recorded moves and check the hash.
	ok, err := monopoly.Verify(cfg, seed, tbl.Moves(), tbl.Log())
	fmt.Printf("Replay verified: %v (err=%v)\n", ok, err)
	fmt.Printf("Replay hash: %s\n", tbl.ReplayHash())
}
