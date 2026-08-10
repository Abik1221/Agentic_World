package main

import (
	"fmt"
	"testing"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	mono "github.com/agent-arena/arena/internal/engine/monopoly"
)

// Win-rate balance across many seeds.
//
// Two seeds both returned a mafia win, which is not evidence of anything on its own — but a
// lopsided lab IS a real problem: every match it produces becomes training data for the model
// board and the stress figures, and a town policy that always loses would show up there as a
// property of the models rather than of this file.
func TestLabMafiaWinRateBalance(t *testing.T) {
	seats := []int{0, 1, 2, 3, 4, 5, 6, 7}
	eng := &mf.Engine{}
	wins := map[string]int{}
	var totalActions, unfinished int

	const runs = 200
	for i := 0; i < runs; i++ {
		seed := []byte(fmt.Sprintf("balance-seed-%03d", i))
		st, _ := eng.Init(seed, seats)
		p := persona{Style: []string{"balanced", "aggressive"}[i%2]}
		var log []mf.Event
		acted := 0

		for step := 0; step < 5000 && !st.Finished; step++ {
			pending := mf.PendingActors(st)
			if len(pending) == 0 {
				break
			}
			for _, seat := range pending {
				legal := mf.LegalActions(st, seat)
				if len(legal) == 0 {
					continue
				}
				v := viewForSeat(t, st, seat, legal, log)
				act, _ := mafiaAction(p, v)
				kind, _ := act["action"].(string)
				a := mf.Action{Kind: kind}
				if tgt, ok := act["target"].(int); ok {
					a.Target = tgt
				}
				if txt, ok := act["text"].(string); ok {
					a.Text = txt
				}
				if tone, ok := act["tone"].(string); ok {
					a.Tone = tone
				}
				next, evs, err := eng.Act(st, seat, a)
				if err != nil {
					t.Fatalf("seed %d: engine rejected %q from seat %d in %q: %v", i, kind, seat, st.Phase, err)
				}
				st = next
				log = append(log, evs...)
				acted++
				if st.Finished {
					break
				}
			}
		}
		if !st.Finished {
			unfinished++
			continue
		}
		wins[st.Winner]++
		totalActions += acted
	}

	t.Logf("over %d matches: %v  (unfinished %d, mean %d actions)",
		runs, wins, unfinished, totalActions/runs)

	mafiaWins := wins[mf.RoleMafia] + wins["mafia"] + wins["Mafia"]
	rate := float64(mafiaWins) / float64(runs)
	t.Logf("mafia win rate: %.1f%%", rate*100)

	// Real Mafia is not a 50/50 game and a balanced lab is not the goal. The bar is that BOTH
	// factions win sometimes: a policy that never wins produces a constant, and a constant
	// carries no information about the models playing it.
	if mafiaWins == 0 || mafiaWins == runs {
		t.Fatalf("one faction won every single match (%v) — this lab cannot distinguish a strong "+
			"agent from a weak one, so any board built on it would be measuring this file", wins)
	}
	if unfinished > runs/20 {
		t.Fatalf("%d/%d matches never finished; a stalling policy hangs a stress run", unfinished, runs)
	}
}

// The Monopoly equivalent: no seat may be structurally destined to win.
//
// Turn order confers a real advantage in Monopoly, so an even split is not the bar. What would
// invalidate the lab is a DEGENERATE distribution — one seat taking nearly everything — because
// then the board would be scoring seat assignment and calling it model strength.
func TestLabMonopolySeatWinDistribution(t *testing.T) {
	wins := map[int]int{}
	var totalActions, unfinished int
	const runs = 60

	for i := 0; i < runs; i++ {
		eng := mono.New(mono.Config{Players: 4, StartingCash: 1500, MaxTurns: 400})
		seed := []byte(fmt.Sprintf("mono-balance-%03d", i))
		st, _ := eng.Init(seed)
		p := persona{Style: []string{"balanced", "aggressive"}[i%2]}
		acted := 0

		for step := 0; step < 20000 && !st.Finished; step++ {
			seat := eng.PendingSeat(st)
			if seat < 0 {
				break
			}
			legal := eng.LegalActions(st, seat)
			if len(legal) == 0 {
				break
			}
			act, _ := monopolyAction(p, monopolyView{
				YourSeat: seat, Phase: st.Phase, Legal: legal, State: &st,
			})
			kind, _ := act["kind"].(string)
			a := mono.Action{Kind: kind}
			if v, ok := act["property"].(int); ok {
				a.Property = v
			}
			if v, ok := act["amount"].(int); ok {
				a.Amount = v
			}
			if v, ok := act["trade"].(mono.Trade); ok {
				tr := v
				a.Trade = &tr
			}
			next, _, err := eng.Step(st, seat, a, seed)
			if err != nil {
				t.Fatalf("seed %d: engine rejected %q from seat %d in %q: %v", i, kind, seat, st.Phase, err)
			}
			st = next
			acted++
		}
		if !st.Finished {
			unfinished++
			continue
		}
		// Winner: the last solvent seat, or the richest if the turn cap ended it.
		best, bestCash := -1, -1<<30
		for _, pl := range st.Players {
			if pl.Bankrupt {
				continue
			}
			if pl.Cash > bestCash {
				best, bestCash = pl.Seat, pl.Cash
			}
		}
		wins[best]++
		totalActions += acted
	}

	t.Logf("over %d matches: %v  (unfinished %d, mean %d actions)", runs, wins, unfinished, totalActions/runs)

	if unfinished > runs/10 {
		t.Fatalf("%d/%d matches never finished", unfinished, runs)
	}
	seatsThatWon := 0
	for _, n := range wins {
		if n > 0 {
			seatsThatWon++
		}
	}
	if seatsThatWon < 2 {
		t.Fatalf("only %d seat(s) ever won (%v) — the lab is scoring seat order, not play",
			seatsThatWon, wins)
	}
}
