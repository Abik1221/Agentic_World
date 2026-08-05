package rating

import "testing"

// The point of the edge metric: a developer on a weak model who beats what that model
// normally achieves must outrank one on a strong model who merely matches it. Ranking
// by raw win rate measures budget; this measures engineering.
func TestEdgeRewardsExtractingMoreFromTheSameModel(t *testing.T) {
	models := []ModelStat{
		{Provider: "anthropic", Model: "frontier", Wins: 700, Losses: 300}, // 70% baseline
		{Provider: "ollama", Model: "small", Wins: 300, Losses: 700},       // 30% baseline
	}
	for i := range models {
		deriveModelStat(&models[i])
	}
	rows := []DevModelRow{
		// Rides a strong model to a strong-looking record — but BELOW what the model does.
		{UserPublicID: "u_rich", Username: "rich", Provider: "anthropic", Model: "frontier",
			Matches: 100, Wins: 65, Losses: 35},
		// Gets far more out of a weak one than the weak one normally gives.
		{UserPublicID: "u_sharp", Username: "sharp", Provider: "ollama", Model: "small",
			Matches: 100, Wins: 50, Losses: 50},
	}

	got := BuildDeveloperEdges(rows, models, 1)
	if len(got) != 2 {
		t.Fatalf("got %d developers", len(got))
	}
	// Raw win rate would put `rich` (65%) above `sharp` (50%). Edge must invert that.
	if got[0].Username != "sharp" {
		t.Fatalf("ranked %q first; the developer beating their model's baseline must lead, "+
			"otherwise the board measures budget instead of engineering", got[0].Username)
	}
	if got[0].Edge <= 0 {
		t.Errorf("sharp edge = %.3f, want positive (50%% actual vs 30%% expected)", got[0].Edge)
	}
	if got[1].Edge >= 0 {
		t.Errorf("rich edge = %.3f, want negative (65%% actual vs 70%% expected)", got[1].Edge)
	}
	// And the expectation is the model's published baseline, checkable by hand.
	if e := got[1].ExpectedWinRate; e < 0.699 || e > 0.701 {
		t.Errorf("expected win rate = %.3f, want the frontier model's published 0.70", e)
	}
}

// Expectation is weighted by GAMES on each model, not averaged across models: a
// developer who played 90 games on a weak model and 10 on a strong one is mostly being
// measured against the weak one.
func TestExpectationIsWeightedByGamesPerModel(t *testing.T) {
	models := []ModelStat{
		{Provider: "a", Model: "strong", Wins: 90, Losses: 10}, // 0.9
		{Provider: "a", Model: "weak", Wins: 10, Losses: 90},   // 0.1
	}
	for i := range models {
		deriveModelStat(&models[i])
	}
	rows := []DevModelRow{
		{UserPublicID: "u", Username: "u", Provider: "a", Model: "weak", Wins: 45, Losses: 45},
		{UserPublicID: "u", Username: "u", Provider: "a", Model: "strong", Wins: 5, Losses: 5},
	}
	got := BuildDeveloperEdges(rows, models, 1)
	// 90 decisive on weak (0.1) + 10 on strong (0.9) ⇒ (90*0.1 + 10*0.9)/100 = 0.18.
	if e := got[0].ExpectedWinRate; e < 0.179 || e > 0.181 {
		t.Fatalf("expected = %.4f, want 0.18 — a flat average across models would give 0.50", e)
	}
}

// A model with no population baseline must not be invented into one. It counts toward
// the developer's games but contributes nothing to the expectation.
func TestModelWithNoBaselineIsExcludedFromExpectation(t *testing.T) {
	models := []ModelStat{
		{Provider: "a", Model: "known", Wins: 60, Losses: 40},
		{Provider: "a", Model: "undecided"}, // no decisive games ⇒ no baseline
	}
	for i := range models {
		deriveModelStat(&models[i])
	}
	rows := []DevModelRow{
		{UserPublicID: "u", Username: "u", Provider: "a", Model: "known", Wins: 10, Losses: 10},
		{UserPublicID: "u", Username: "u", Provider: "a", Model: "undecided", Wins: 5, Ties: 5},
	}
	got := BuildDeveloperEdges(rows, models, 1)
	if e := got[0].ExpectedWinRate; e < 0.599 || e > 0.601 {
		t.Errorf("expected = %.3f, want only the known model's 0.60", e)
	}
	// ...but the games still count toward the developer's own record.
	if got[0].Games != 30 {
		t.Errorf("games = %d, want all 30 — a model without a baseline is still play", got[0].Games)
	}
}

// Contribution warns the reader when a developer is largely their own baseline, which
// is the metric's main weakness on a small platform.
func TestContributionFlagsSelfComparison(t *testing.T) {
	models := []ModelStat{{Provider: "a", Model: "m", Wins: 50, Losses: 50}}
	deriveModelStat(&models[0])
	// This developer IS almost the entire baseline.
	rows := []DevModelRow{{UserPublicID: "u", Username: "u", Provider: "a", Model: "m", Wins: 45, Losses: 45}}
	got := BuildDeveloperEdges(rows, models, 1)
	if got[0].Contribution < 0.89 {
		t.Errorf("contribution = %.2f, want ~0.9 — the reader must be able to see self-comparison",
			got[0].Contribution)
	}
}

// A developer is one competitor however many agents they run.
func TestDevelopersAreGroupedByPersonNotAgent(t *testing.T) {
	models := []ModelStat{{Provider: "a", Model: "m", Wins: 50, Losses: 50}}
	deriveModelStat(&models[0])
	rows := []DevModelRow{
		{UserPublicID: "u", Username: "u", Provider: "a", Model: "m", Agents: 1, Matches: 10, Wins: 6, Losses: 4},
		{UserPublicID: "u", Username: "u", Provider: "a", Model: "m2", Agents: 2, Matches: 20, Wins: 9, Losses: 11},
	}
	got := BuildDeveloperEdges(rows, models, 1)
	if len(got) != 1 {
		t.Fatalf("one person must be one row, got %d", len(got))
	}
	if got[0].Agents != 3 || got[0].Games != 30 {
		t.Errorf("agents=%d games=%d, want 3/30", got[0].Agents, got[0].Games)
	}
}

// Open-weights-only is a positive claim and must not be awarded by default to someone
// whose models could not be classified.
func TestOpenWeightsOnlyRequiresEvidence(t *testing.T) {
	models := []ModelStat{
		{Provider: "ollama", Model: "llama-3.1-8b", Wins: 5, Losses: 5},
		{Provider: "openai", Model: "gpt-4o", Wins: 5, Losses: 5},
		{Provider: "", Model: "mystery-thing", Wins: 5, Losses: 5},
	}
	for i := range models {
		deriveModelStat(&models[i])
	}
	rows := []DevModelRow{
		{UserPublicID: "u_open", Username: "open", Provider: "ollama", Model: "llama-3.1-8b", Wins: 5, Losses: 5},
		{UserPublicID: "u_mixed", Username: "mixed", Provider: "ollama", Model: "llama-3.1-8b", Wins: 3, Losses: 2},
		{UserPublicID: "u_mixed", Username: "mixed", Provider: "openai", Model: "gpt-4o", Wins: 3, Losses: 2},
		{UserPublicID: "u_unknown", Username: "unknown", Provider: "", Model: "mystery-thing", Wins: 5, Losses: 5},
	}
	by := map[string]DeveloperEdge{}
	for _, e := range BuildDeveloperEdges(rows, models, 1) {
		by[e.Username] = e
	}
	if !by["open"].OpenWeightsOnly {
		t.Error("a developer running only open weights should be marked")
	}
	if by["mixed"].OpenWeightsOnly {
		t.Error("a developer who used a proprietary model must not be marked")
	}
	if by["unknown"].OpenWeightsOnly {
		t.Error("an unclassifiable model must not earn the open-weights claim by default")
	}
}

// Thin samples are tagged, exactly as on the model board.
func TestEdgePreliminaryAndMinGames(t *testing.T) {
	models := []ModelStat{{Provider: "a", Model: "m", Wins: 50, Losses: 50}}
	deriveModelStat(&models[0])
	rows := []DevModelRow{
		{UserPublicID: "u_thin", Username: "thin", Provider: "a", Model: "m", Wins: 3, Losses: 1},
		{UserPublicID: "u_thick", Username: "thick", Provider: "a", Model: "m",
			Wins: prelimMinGames, Losses: prelimMinGames},
	}
	by := map[string]DeveloperEdge{}
	for _, e := range BuildDeveloperEdges(rows, models, 1) {
		by[e.Username] = e
	}
	if !by["thin"].Preliminary || by["thick"].Preliminary {
		t.Errorf("preliminary tagging wrong: thin=%v thick=%v",
			by["thin"].Preliminary, by["thick"].Preliminary)
	}
	if by["thin"].WinRateCI <= by["thick"].WinRateCI {
		t.Error("a thin sample must carry a wider interval")
	}
	// minGames filters the board without hiding the tag on rows that clear it.
	if got := BuildDeveloperEdges(rows, models, 10); len(got) != 1 || got[0].Username != "thick" {
		t.Errorf("minGames did not filter: %+v", got)
	}
}
