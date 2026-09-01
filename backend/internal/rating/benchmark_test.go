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
	byModel := map[string]ModelStat{}
	for _, m := range page.Models {
		byModel[m.Model] = m
	}
	// claude: 60W/40L/0T → 100 games, 60% win rate.
	c := byModel["claude-sonnet-5"]
	if c.Games != 100 || c.WinRate < 0.599 || c.WinRate > 0.601 {
		t.Fatalf("claude games=%d winrate=%.3f, want 100 / 0.60", c.Games, c.WinRate)
	}
	// gpt-4o: 30W/20L/10T → 60 games, win rate over DECISIVE games = 30/50 = 0.60.
	g := byModel["gpt-4o"]
	if g.Games != 60 || g.WinRate < 0.599 || g.WinRate > 0.601 {
		t.Fatalf("gpt games=%d winrate=%.3f, want 60 / 0.60", g.Games, g.WinRate)
	}
	if c.CoinsWon != 12000 {
		t.Fatalf("coins carried through wrong: %d", c.CoinsWon)
	}
}

// The board's default must be EVERY arena. It used to silently answer for Goofspiel
// alone, so two of three arenas' play was invisible under a heading that reads "which
// LLM wins on Pyyol".
func TestModelBenchmarkDefaultsToEveryArena(t *testing.T) {
	for _, requested := range []string{"", ArenaAll} {
		repo := newRollFakeRepo(-1)
		page, err := svcAtSeason(repo, 3).ModelBenchmark(context.Background(), requested, 0)
		if err != nil {
			t.Fatalf("game=%q: %v", requested, err)
		}
		// "" reaches the repo as "do not filter by arena".
		if repo.benchGame != "" {
			t.Errorf("game=%q reached repo as %q, want \"\" (all arenas)", requested, repo.benchGame)
		}
		if page.Game != ArenaAll {
			t.Errorf("game=%q reported as %q, want %q", requested, page.Game, ArenaAll)
		}
	}
	// A named arena still narrows.
	repo := newRollFakeRepo(-1)
	page, _ := svcAtSeason(repo, 3).ModelBenchmark(context.Background(), GameMafia, 0)
	if repo.benchGame != GameMafia || page.Game != GameMafia {
		t.Errorf("mafia: repo got %q, page reports %q", repo.benchGame, page.Game)
	}
}

// Facts are aggregated over the SEASON WINDOW, not over all time — the repo is handed
// the same [start, end) the season API publishes.
func TestModelBenchmarkPassesSeasonWindow(t *testing.T) {
	repo := newRollFakeRepo(-1)
	svc := svcAtSeason(repo, 4)
	if _, err := svc.ModelBenchmark(context.Background(), ArenaAll, 0); err != nil {
		t.Fatal(err)
	}
	wantStart, wantEnd := svc.SeasonBounds(4)
	if !repo.benchStart.Equal(wantStart) || !repo.benchEnd.Equal(wantEnd) {
		t.Fatalf("window = [%s, %s), want [%s, %s)", repo.benchStart, repo.benchEnd, wantStart, wantEnd)
	}
}

// minGames is the floor for APPEARING. It is applied after derivation so a filtered
// row cannot be counted in one figure and missing from another.
func TestModelBenchmarkMinGamesFiltersThinRows(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{
		{Provider: "openai", Model: "played-a-lot", Wins: 40, Losses: 10},
		{Provider: "openai", Model: "played-twice", Wins: 1, Losses: 1},
	}
	page, err := svcAtSeason(repo, 1).ModelBenchmark(context.Background(), ArenaAll, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Models) != 1 || page.Models[0].Model != "played-a-lot" {
		t.Fatalf("models = %+v, want only played-a-lot", page.Models)
	}
	if page.MinGames != 10 {
		t.Errorf("MinGames = %d, want the applied floor echoed back", page.MinGames)
	}
}

// A thin row that clears minGames is SHOWN and TAGGED rather than hidden: a board that
// silently drops sparse models looks complete when it is not.
func TestModelBenchmarkTagsPreliminarySamples(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{
		{Provider: "openai", Model: "thin", Wins: 3, Losses: 2},                            // 5 games
		{Provider: "openai", Model: "thick", Wins: prelimMinGames, Losses: prelimMinGames}, // plenty
	}
	page, err := svcAtSeason(repo, 1).ModelBenchmark(context.Background(), ArenaAll, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range page.Models {
		got[m.Model] = m.Preliminary
	}
	if len(got) != 2 {
		t.Fatalf("both rows should be present, got %+v", got)
	}
	if !got["thin"] {
		t.Error("a 5-game row must be tagged preliminary")
	}
	if got["thick"] {
		t.Error("a row at/above the threshold must not be tagged preliminary")
	}
}

// The ± on a win rate. A 4-from-4 record is not evidence of a 100% win rate, and the
// normal approximation would claim it is by reporting a zero-width interval.
func TestWinRateConfidenceIntervalWidensOnSmallSamples(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{
		{Provider: "a", Model: "small", Wins: 6, Losses: 4},     // 10 decisive
		{Provider: "a", Model: "large", Wins: 600, Losses: 400}, // 1000 decisive, same rate
	}
	page, err := svcAtSeason(repo, 1).ModelBenchmark(context.Background(), ArenaAll, 1)
	if err != nil {
		t.Fatal(err)
	}
	ci := map[string]float64{}
	for _, m := range page.Models {
		ci[m.Model] = m.WinRateCI
	}
	if !(ci["small"] > ci["large"]) {
		t.Fatalf("small-sample CI (%.4f) must exceed large-sample CI (%.4f)", ci["small"], ci["large"])
	}
	if ci["large"] <= 0 {
		t.Error("a finite sample always has a non-zero interval")
	}
	// A perfect record must NOT report certainty.
	if w := wilsonHalfWidth95(4, 4); w <= 0 {
		t.Errorf("wilson(4,4) = %.4f, want > 0 — 4/4 is not proof of a 100%% win rate", w)
	}
	if w := wilsonHalfWidth95(0, 0); w != 0 {
		t.Errorf("wilson(0,0) = %.4f, want 0 — no trials, no rate to bound", w)
	}
}

// Economics divide by MATCHES and outcomes by GAMES. Rows written before per-seat
// results were recorded count toward matches but not games, and a win rate must never
// be computed over matches it cannot see.
func TestModelBenchmarkSeparatesMatchAndGameDenominators(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{{
		Provider: "openai", Model: "m",
		Matches: 10, Wins: 4, Losses: 4, Ties: 0, // 8 games, 10 benchmarked matches
		Tokens: 100_000, Decisions: 200, EstCostUSD: 2.0,
	}}
	page, err := svcAtSeason(repo, 1).ModelBenchmark(context.Background(), ArenaAll, 1)
	if err != nil {
		t.Fatal(err)
	}
	m := page.Models[0]
	if m.Games != 8 {
		t.Errorf("Games = %d, want 8 (outcomes only)", m.Games)
	}
	if m.TokensPerMatch != 10_000 {
		t.Errorf("TokensPerMatch = %.1f, want 100000/10 matches", m.TokensPerMatch)
	}
	if m.TokensPerDecision != 500 {
		t.Errorf("TokensPerDecision = %.1f, want 100000/200", m.TokensPerDecision)
	}
	if m.TokensPerWin != 25_000 {
		t.Errorf("TokensPerWin = %.1f, want 100000/4 wins", m.TokensPerWin)
	}
	if m.CostPerMatch < 0.199 || m.CostPerMatch > 0.201 {
		t.Errorf("CostPerMatch = %.4f, want 2.00/10", m.CostPerMatch)
	}
	if m.CostPerWin < 0.499 || m.CostPerWin > 0.501 {
		t.Errorf("CostPerWin = %.4f, want 2.00/4", m.CostPerWin)
	}
}

// Cost columns divide the GATEWAY-VERIFIED total only when COVERAGE says it represents the
// row; otherwise they divide the complete self-reported total and say so.
//
// The rule used to be "verified whenever any verified cost exists", justified by self-reported
// spend being gameable — true, but only half the picture. An INCOMPLETE verified figure is
// gameable in the opposite direction and more effectively, because it wears the label a reader
// trusts most: route 5% of your calls and the gateway honestly reports 5% of your spend against
// 100% of your wins. For a ratio, completeness beats provenance.
func TestCostBasisRequiresCoverageBeforeTrustingVerifiedSpend(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{
		// Fully covered: verified spend represents the row, so it is the divisor.
		{Provider: "a", Model: "covered", Matches: 4, Wins: 4,
			EstCostUSD: 1.0, VerifiedCostUSD: 8.0, Verified: NewCoverage(100, 100)},
		// THE ATTACK: 5% routed. The gateway's $0.40 is honest and useless as a ratio — it
		// would report cost-per-win of 0.10 against a real 2.00, and label it "verified".
		{Provider: "a", Model: "thin", Matches: 4, Wins: 4,
			EstCostUSD: 8.0, VerifiedCostUSD: 0.40, Verified: NewCoverage(100, 5)},
		// Verified spend but no measurable denominator: also refused. An unmeasurable
		// denominator is exactly the state an agent would engineer to keep the label.
		{Provider: "a", Model: "unknown-coverage", Matches: 4, Wins: 4,
			EstCostUSD: 8.0, VerifiedCostUSD: 0.40},
		{Provider: "a", Model: "sdk-only", Matches: 4, Wins: 4, EstCostUSD: 4.0},
		{Provider: "a", Model: "no-cost", Matches: 4, Wins: 4},
		// Only a thin verified figure and nothing complete to fall back on. The answer is NO
		// figure: a known-incomplete cost ratio is worse than a blank, and blank already means
		// "not measured" here rather than "zero spend".
		{Provider: "a", Model: "thin-only", Matches: 4, Wins: 4,
			VerifiedCostUSD: 0.40, Verified: NewCoverage(100, 5)},
	}
	page, err := svcAtSeason(repo, 1).ModelBenchmark(context.Background(), ArenaAll, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ModelStat{}
	for _, m := range page.Models {
		got[m.Model] = m
	}
	if b := got["covered"]; b.CostBasis != CostVerified || b.CostPerWin != 2.0 {
		t.Errorf("covered: basis=%q costPerWin=%.2f, want %q / 8.00÷4", b.CostBasis, b.CostPerWin, CostVerified)
	}
	if t2 := got["thin"]; t2.CostBasis != CostSelfReported || t2.CostPerWin != 2.0 {
		t.Errorf("thin: basis=%q costPerWin=%.2f, want %q / 8.00÷4 — a 5%%-routed row must not "+
			"report 5%% of its spend as if it were the whole bill", t2.CostBasis, t2.CostPerWin, CostSelfReported)
	}
	if u := got["unknown-coverage"]; u.CostBasis != CostSelfReported || u.CostPerWin != 2.0 {
		t.Errorf("unknown-coverage: basis=%q costPerWin=%.2f, want %q / 8.00÷4",
			u.CostBasis, u.CostPerWin, CostSelfReported)
	}
	if s := got["sdk-only"]; s.CostBasis != CostSelfReported || s.CostPerWin != 1.0 {
		t.Errorf("sdk-only: basis=%q costPerWin=%.2f, want %q / 4.00÷4", s.CostBasis, s.CostPerWin, CostSelfReported)
	}
	// No cost measured at all is a BLANK basis, not a claim of zero spend.
	if n := got["no-cost"]; n.CostBasis != "" || n.CostPerWin != 0 {
		t.Errorf("no-cost: basis=%q costPerWin=%.2f, want empty / 0", n.CostBasis, n.CostPerWin)
	}
	if to := got["thin-only"]; to.CostBasis != "" || to.CostPerWin != 0 {
		t.Errorf("thin-only: basis=%q costPerWin=%.2f, want empty / 0 — a known-incomplete "+
			"figure must not be published as a cost ratio", to.CostBasis, to.CostPerWin)
	}
}

// A group's cost basis must follow the same rule as the model rows inside it: a basis that
// means one thing on a model row and another on the group containing it is worse than none.
// Coverage pools the raw COUNTS, so one tiny fully-covered model cannot carry a large
// uncovered one over the threshold.
func TestGroupCoveragePoolsCountsRatherThanAveragingFractions(t *testing.T) {
	groups := BuildGroups([]ModelStat{
		{Provider: "p", Model: "tiny-covered", Matches: 1, Wins: 1,
			EstCostUSD: 1, VerifiedCostUSD: 1, Verified: NewCoverage(10, 10)},
		{Provider: "p", Model: "huge-uncovered", Matches: 1, Wins: 1,
			EstCostUSD: 100, VerifiedCostUSD: 1, Verified: NewCoverage(9990, 0)},
	})
	var g *GroupStat
	for i := range groups {
		if groups[i].Kind == GroupProvider && groups[i].Key == "p" {
			g = &groups[i]
			break
		}
	}
	if g == nil {
		t.Fatal("no provider group built")
	}
	// 10 bound of 10000 logged = 0.1%. Averaging the two fractions would have given 50%.
	if g.Verified.Coverage > 0.01 {
		t.Fatalf("pooled coverage = %.4f, want ~0.001 — fractions were averaged, not pooled",
			g.Verified.Coverage)
	}
	if g.CostBasis != CostSelfReported {
		t.Errorf("group basis = %q, want %q at 0.1%% coverage", g.CostBasis, CostSelfReported)
	}
}

// Wall-clock averages divide by matches that CONTRIBUTED a clock, not by every match:
// a stuck match with an unusable duration must not drag the average toward zero.
func TestAvgMatchSecondsUsesTimedMatchesOnly(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{{
		Provider: "a", Model: "m", Wins: 1,
		Matches: 10, PlaySeconds: 600, TimedMatches: 4, // only 4 matches had a usable clock
	}}
	page, err := svcAtSeason(repo, 1).ModelBenchmark(context.Background(), ArenaAll, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Models[0].AvgMatchSeconds; got != 150 {
		t.Fatalf("AvgMatchSeconds = %.1f, want 600/4 = 150 (not 600/10)", got)
	}
}

// Per-arena rows carry their own derived figures, on the same definitions as the
// aggregate — a "tokens per match" that means one thing on the total row and another
// on an arena row is worse than not showing it.
func TestArenaBreakdownIsDerivedLikeTheAggregate(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{{
		Provider: "a", Model: "m", Matches: 6, Wins: 4, Losses: 2, Tokens: 60_000,
		Arenas: []ArenaStat{
			{Game: GameMafia, Matches: 4, Wins: 3, Losses: 1, Tokens: 40_000, Decisions: 100,
				LegalInternal: 95, PlaySecondsInternal: 400, TimedMatchesInternal: 2},
			// A SECOND arena is the point: the breakdown must carry a row per arena, each
			// deriving its own rates rather than inheriting the aggregate's. This was
			// Monopoly; which arena it is does not matter, only that there are two.
			{Game: GameGoofspiel, Matches: 2, Wins: 1, Losses: 1, Tokens: 20_000},
		},
	}}
	page, err := svcAtSeason(repo, 1).ModelBenchmark(context.Background(), ArenaAll, 1)
	if err != nil {
		t.Fatal(err)
	}
	arenas := page.Models[0].Arenas
	if len(arenas) != 2 {
		t.Fatalf("arenas = %+v", arenas)
	}
	mafia := arenas[0]
	if mafia.Games != 4 || mafia.WinRate != 0.75 {
		t.Errorf("mafia games=%d winRate=%.2f, want 4 / 0.75", mafia.Games, mafia.WinRate)
	}
	if mafia.TokensPerMatch != 10_000 {
		t.Errorf("mafia tokensPerMatch = %.1f, want 40000/4", mafia.TokensPerMatch)
	}
	if mafia.LegalRate != 0.95 {
		t.Errorf("mafia legalRate = %.3f, want 95/100", mafia.LegalRate)
	}
	if mafia.AvgMatchSeconds != 200 {
		t.Errorf("mafia avgMatchSeconds = %.1f, want 400/2 timed", mafia.AvgMatchSeconds)
	}
	if mafia.WinRateCI <= 0 {
		t.Error("arena rows need a confidence interval too")
	}
}

// Intelligence needs a real sample. A model that made a handful of decisions must not
// be able to top the board on a lucky run.
func TestIntelligenceRequiresEnoughDecisions(t *testing.T) {
	if got := intelligenceScore(intelMinDecisions-1, 1.0, 0, 100); got != 0 {
		t.Errorf("below the decision floor the score is 0 (not scored), got %d", got)
	}
	perfect := intelligenceScore(intelMinDecisions, 1.0, 0, 100)
	if perfect != intelScale {
		t.Errorf("all-legal, no-fallback, fast = %d, want %d", perfect, intelScale)
	}
	// Slower is worse, all else equal.
	slow := intelligenceScore(intelMinDecisions, 1.0, 0, int(intelLatencySlow)+1)
	if !(slow < perfect) {
		t.Errorf("a slow model (%d) must score below a fast one (%d)", slow, perfect)
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

// With no arena named, the service asks the repo to resolve the agent's PRIMARY arena
// rather than assuming Goofspiel — which reported "unranked" to every agent that had
// only ever played Mafia or Monopoly.
func TestStandingWithoutArenaDoesNotAssumeGoofspiel(t *testing.T) {
	for _, requested := range []string{"", ArenaAll} {
		repo := newRollFakeRepo(-1)
		repo.standing = &Standing{Game: GameMafia, Rank: 2, Total: 9}
		if _, _, err := svcAtSeason(repo, 1).Standing(context.Background(), "ag_x", requested); err != nil {
			t.Fatalf("game=%q: %v", requested, err)
		}
		if repo.standingGame != "" {
			t.Errorf("game=%q reached repo as %q, want \"\" (resolve primary arena)", requested, repo.standingGame)
		}
	}
}

func TestIsArena(t *testing.T) {
	for _, ok := range []string{"", ArenaAll, GameGoofspiel, GameMafia} {
		if !IsArena(ok) {
			t.Errorf("IsArena(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"goofspeil", "chess", "ALL"} {
		if IsArena(bad) {
			t.Errorf("IsArena(%q) = true, want false — a typo must 400, not render as an empty board", bad)
		}
	}
}

// The detail page must be built from the SAME aggregate the board is, or a model's page
// and the row someone clicked to reach it would quote different numbers.
func TestModelDetailReusesTheBoardsNumbers(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{
		{Provider: "anthropic", Model: "claude-sonnet-4", Matches: 40, Wins: 24, Losses: 16,
			Decisions: 400, Legal: 396, Tokens: 80_000, VerifiedCostUSD: 4.0, Agents: 3, Developers: 2},
		{Provider: "openai", Model: "gpt-4o", Matches: 10, Wins: 3, Losses: 7},
	}
	repo.runners = []ModelRunner{
		{AgentPublicID: "ag_1", AgentName: "Alpha", Username: "dev1", Matches: 30, Wins: 20, Losses: 10},
		{AgentPublicID: "ag_2", AgentName: "Beta", Username: "dev2", Matches: 10, Wins: 4, Losses: 6},
	}

	d, found, err := svcAtSeason(repo, 5).ModelDetail(context.Background(), ArenaAll, "anthropic", "claude-sonnet-4")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	// Same derivation as the board: 24/(24+16) = 0.60.
	if d.Model.WinRate < 0.599 || d.Model.WinRate > 0.601 {
		t.Errorf("win rate = %.3f, want the board's 0.60", d.Model.WinRate)
	}
	if d.Model.Developers != 2 || d.Model.Agents != 3 {
		t.Errorf("adoption = %d devs / %d agents, want 2/3", d.Model.Developers, d.Model.Agents)
	}
	if d.Model.Class.Vendor != "anthropic" {
		t.Errorf("class did not travel onto the detail: %+v", d.Model.Class)
	}
	// Rank tells the reader where this sits without making them go back and count.
	if d.Rank != 1 || d.Total != 2 {
		t.Errorf("rank = %d of %d, want 1 of 2", d.Rank, d.Total)
	}
	if len(d.Runners) != 2 {
		t.Fatalf("runners = %+v", d.Runners)
	}
	// Runner win rates are derived on the same definitions, ± included.
	if d.Runners[0].WinRate < 0.666 || d.Runners[0].WinRate > 0.667 {
		t.Errorf("runner win rate = %.3f, want 20/30", d.Runners[0].WinRate)
	}
	if d.Runners[0].WinRateCI <= 0 {
		t.Error("a runner row needs its confidence interval too")
	}
	// The arena must reach the repo as "" for the all-arena view.
	if repo.runnersFor != [3]string{"", "anthropic", "claude-sonnet-4"} {
		t.Errorf("ModelRunners asked for %v", repo.runnersFor)
	}
}

// A model nobody has played is a 404, not an empty page pretending to be a record.
func TestModelDetailNotFound(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{{Provider: "openai", Model: "gpt-4o", Wins: 1}}
	for _, c := range []struct{ provider, model string }{
		{"openai", "never-played"},
		{"wrong-provider", "gpt-4o"}, // same model name, different provider ⇒ different row
		{"openai", ""},
	} {
		if _, found, err := svcAtSeason(repo, 1).ModelDetail(context.Background(), ArenaAll, c.provider, c.model); found || err != nil {
			t.Errorf("%s/%s: found=%v err=%v, want not-found", c.provider, c.model, found, err)
		}
	}
}

// A model too thin for the board must still render its own page — otherwise a link
// that was valid yesterday 404s today because the model dropped below the floor.
func TestModelDetailIgnoresTheBoardsMinimumSample(t *testing.T) {
	repo := newRollFakeRepo(-1)
	repo.models = []ModelStat{{Provider: "openai", Model: "barely-played", Matches: 1, Wins: 1}}
	d, found, err := svcAtSeason(repo, 1).ModelDetail(context.Background(), ArenaAll, "openai", "barely-played")
	if err != nil || !found {
		t.Fatalf("a one-game model must still have a page: found=%v err=%v", found, err)
	}
	if !d.Model.Preliminary {
		t.Error("...and must be marked preliminary on it")
	}
}
