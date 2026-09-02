package rating

import "testing"

func ms(provider, model string, wins, losses int, latency int, arenas []ArenaStat) ModelStat {
	m := ModelStat{Provider: provider, Model: model, Wins: wins, Losses: losses,
		AvgLatencyMs: latency, Decisions: 5000, Arenas: arenas}
	m.Games = wins + losses
	m.LegalRate, m.FallbackRate = 1, 0
	m.Intelligence = intelligenceScore(m.Decisions, m.LegalRate, m.FallbackRate, m.AvgLatencyMs)
	return m
}

// TestBoardIsNotOrderedByLatency is the regression guard for the bug this replaced.
//
// Both models play perfectly legally with no fallbacks, so under the OLD rule their
// Intelligence differed only by speed and the board was a latency ranking. A 700ms model
// losing most of its games outranked a 12s model winning most of them, on the page titled
// "which model wins on Pyyol".
func TestBoardIsNotOrderedByLatency(t *testing.T) {
	fastButLosing := ms("v", "fast-8b", 350, 650, 700, nil)
	slowButWinning := ms("v", "slow-frontier", 680, 320, 12000, nil)

	// Precondition: the old key really would have put the fast one first.
	if !(fastButLosing.Intelligence > slowButWinning.Intelligence) {
		t.Fatal("premise: under the old rule the fast model should score higher on Intelligence")
	}

	out := []ModelStat{fastButLosing, slowButWinning}
	orderModels(out, GameGoofspiel)

	if out[0].Model != "slow-frontier" {
		t.Fatalf("board ordered %q first; latency is still deciding the ranking", out[0].Model)
	}
	if !out[0].Normalized.Comparable || out[0].Normalized.Decisive != 1000 {
		t.Fatalf("winner has no normalised evidence: %+v", out[0].Normalized)
	}
}

// TestCrossArenaOrderingUsesPerArenaBaselines. A model that only ever queued 1v1 must not
// outrank one that won above base rate in a harder arena.
func TestCrossArenaOrderingUsesPerArenaBaselines(t *testing.T) {
	// Population: goofspiel sits at 50%, mafia at 25%.
	filler := ms("v", "filler", 0, 0, 1000, []ArenaStat{
		{Game: GameGoofspiel, Wins: 5000, Losses: 5000},
		{Game: GameMafia, Wins: 2500, Losses: 7500},
	})
	cherry := ms("v", "queue-filter", 550, 450, 700, []ArenaStat{
		{Game: GameGoofspiel, Wins: 550, Losses: 450},
	})
	rounder := ms("v", "all-rounder", 400, 600, 12000, []ArenaStat{
		{Game: GameMafia, Wins: 400, Losses: 600},
	})

	out := []ModelStat{cherry, rounder, filler}
	orderModels(out, "")

	var ci, ri int
	for i, m := range out {
		switch m.Model {
		case "queue-filter":
			ci = i
		case "all-rounder":
			ri = i
		}
	}
	if !(ri < ci) {
		t.Fatalf("queue-filter (55%% in a 50%% arena) ranked above all-rounder "+
			"(40%% in a 25%% arena); the pooled-win-rate exploit survives. order=%v",
			[]string{out[0].Model, out[1].Model, out[2].Model})
	}
}

// TestUnmeasuredRowsSinkButAreStillShown. Hiding a row with no evidence would make the board
// look complete when it is not — the same reasoning as Preliminary.
func TestUnmeasuredRowsSinkButAreStillShown(t *testing.T) {
	measured := ms("v", "measured", 600, 400, 1000, nil)
	unmeasured := ms("v", "unmeasured", 0, 0, 100, nil)
	out := []ModelStat{unmeasured, measured}
	orderModels(out, GameGoofspiel)

	if len(out) != 2 {
		t.Fatalf("a row was dropped: %d remain", len(out))
	}
	if out[0].Model != "measured" {
		t.Fatalf("unmeasured row ranked first: %q", out[0].Model)
	}
	if out[1].Normalized.Comparable {
		t.Fatal("a row with no decisive games was reported as comparable")
	}
}

// TestOrderingIsDeterministic. Identical evidence must not shuffle between requests.
func TestOrderingIsDeterministic(t *testing.T) {
	build := func() []ModelStat {
		return []ModelStat{
			ms("v", "b", 500, 500, 1000, nil),
			ms("v", "a", 500, 500, 1000, nil),
			ms("v", "c", 500, 500, 1000, nil),
		}
	}
	first := build()
	orderModels(first, GameGoofspiel)
	for i := 0; i < 25; i++ {
		got := build()
		orderModels(got, GameGoofspiel)
		for j := range got {
			if got[j].Model != first[j].Model {
				t.Fatalf("run %d differs at %d: %q vs %q", i, j, got[j].Model, first[j].Model)
			}
		}
	}
}
