package profiles

import (
	"math"
	"testing"
)

func TestCostPerWin(t *testing.T) {
	if got := CostPerWin(10.0, 0); got != 0 {
		t.Errorf("zero wins should yield 0, got %v", got)
	}
	if got := CostPerWin(12.0, 4); math.Abs(got-3.0) > 1e-9 {
		t.Errorf("CostPerWin(12,4) = %v, want 3", got)
	}
}

func TestFoldEconomics_Empty(t *testing.T) {
	if foldEconomics(nil) != nil {
		t.Error("no benchmarked games → nil economics")
	}
}

func TestFoldEconomics_TotalsAndRatios(t *testing.T) {
	e := foldEconomics([]GameCost{
		{Game: "goofspiel", Games: 10, Wins: 4, TotalCostUSD: 8.0},
		{Game: "mafia", Games: 5, Wins: 0, TotalCostUSD: 2.0}, // no wins yet
	})
	if e == nil {
		t.Fatal("expected economics")
	}
	if e.Games != 15 || e.Wins != 4 {
		t.Errorf("totals wrong: games=%d wins=%d", e.Games, e.Wins)
	}
	if math.Abs(e.TotalCostUSD-10.0) > 1e-9 {
		t.Errorf("total cost = %v, want 10", e.TotalCostUSD)
	}
	if math.Abs(e.CostPerWinUSD-2.5) > 1e-9 { // 10 / 4
		t.Errorf("overall cost-per-win = %v, want 2.5", e.CostPerWinUSD)
	}
	// Per-game ratios: goofspiel 8/4=2; mafia has no wins → 0 (clean "—").
	var goof, mafia GameCost
	for _, g := range e.PerGame {
		switch g.Game {
		case "goofspiel":
			goof = g
		case "mafia":
			mafia = g
		}
	}
	if math.Abs(goof.CostPerWinUSD-2.0) > 1e-9 {
		t.Errorf("goofspiel cost-per-win = %v, want 2", goof.CostPerWinUSD)
	}
	if mafia.CostPerWinUSD != 0 {
		t.Errorf("mafia (0 wins) cost-per-win = %v, want 0", mafia.CostPerWinUSD)
	}
}
