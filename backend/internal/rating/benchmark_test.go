package rating

import (
	"context"
	"testing"
)

func TestModelBenchmarkComputesGamesAndWinRate(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{
		{Provider: "anthropic", Model: "claude-sonnet-5", Agents: 3, Wins: 60, Losses: 40, Ties: 0, AvgElo: 1540, CoinsWon: 12000},
		{Provider: "openai", Model: "gpt-4o", Agents: 5, Wins: 30, Losses: 20, Ties: 10, AvgElo: 1500, CoinsWon: 8000},
	}
	page, err := svcAtSeason(repo, 2).ModelBenchmark(context.Background(), GameGoofspiel, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Season != 2 || len(page.Models) != 2 {
		t.Fatalf("page = %+v", page)
	}
	// claude: 60W/40L/0T → 100 games, 60% win rate.
	c := page.Models[0]
	if c.Games != 100 || c.WinRate < 0.599 || c.WinRate > 0.601 {
		t.Fatalf("claude games=%d winrate=%.3f, want 100 / 0.60", c.Games, c.WinRate)
	}
	// gpt-4o: 30W/20L/10T → 60 games, win rate over DECISIVE games = 30/50 = 0.60.
	g := page.Models[1]
	if g.Games != 60 || g.WinRate < 0.599 || g.WinRate > 0.601 {
		t.Fatalf("gpt games=%d winrate=%.3f, want 60 / 0.60", g.Games, g.WinRate)
	}
	if c.CoinsWon != 12000 {
		t.Fatalf("coins carried through wrong: %d", c.CoinsWon)
	}
}

func TestStandingPassthrough(t *testing.T) {
	repo := newRollFakeRepo(-1)
	// Not found when the agent hasn't played.
	if _, found, err := svcAtSeason(repo, 1).Standing(context.Background(), "ag_x", GameGoofspiel); err != nil || found {
		t.Fatalf("want not-found, got found=%v err=%v", found, err)
	}
	repo.standing = &Standing{Rank: 7, Total: 120, Elo: 1610, Wins: 22, Model: "claude-sonnet-5"}
	st, found, err := svcAtSeason(repo, 1).Standing(context.Background(), "ag_x", GameGoofspiel)
	if err != nil || !found {
		t.Fatalf("want found, got found=%v err=%v", found, err)
	}
	if st.Rank != 7 || st.Total != 120 || st.Elo != 1610 {
		t.Fatalf("standing = %+v", st)
	}
}
