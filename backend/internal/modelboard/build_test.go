package modelboard

import "testing"

func seat(match, game, agent, model, scaffold, dev, result string, coverage float64) Seat {
	return Seat{
		MatchID: match, Game: game, AgentID: agent, Model: model,
		Scaffold: scaffold, DeveloperID: dev, Result: result,
		Coverage: coverage, CoverageKnown: true,
	}
}

func TestATwoPlayerMatchYieldsOneComparison(t *testing.T) {
	seats := []Seat{
		seat("m1", "goofspiel", "a1", "claude", "sc_1", "dev1", "win", 1.0),
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "loss", 1.0),
	}
	cmp, ex := BuildComparisons(seats, DefaultBuildConfig())
	if len(cmp) != 1 {
		t.Fatalf("got %d comparisons, want 1 (excluded: %v)", len(cmp), ex)
	}
	c := cmp[0]
	if c.Outcome != Win || c.ModelA != "claude" || c.ModelB != "gpt" {
		t.Errorf("comparison wrong: %+v", c)
	}
	if c.Weight != 1 {
		t.Errorf("weight = %v, want 1 for a single-pair match", c.Weight)
	}
	if c.StratumA != "dev1/sc_1" || c.StratumB != "dev2/sc_2" {
		t.Errorf("strata wrong: %q vs %q", c.StratumA, c.StratumB)
	}
}

func TestDrawsSurviveIntoTheComparisonSet(t *testing.T) {
	// Draws are the comparisons that carry the most information about near-equal models, so losing
	// them here would defeat the tie handling in the estimator downstream.
	seats := []Seat{
		seat("m1", "goofspiel", "a1", "claude", "sc_1", "dev1", "draw", 1.0),
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "draw", 1.0),
	}
	cmp, _ := BuildComparisons(seats, DefaultBuildConfig())
	if len(cmp) != 1 || cmp[0].Outcome != Draw {
		t.Fatalf("draw did not survive: %+v", cmp)
	}
}

func TestAnNPlayerMatchContributesOneUnitOfEvidence(t *testing.T) {
	// A four-seat table produces six pairs. With unit weights it would carry six times the weight
	// of a Goofspiel match, so the board would quietly become a Monopoly board — the cross-game
	// weighting has to be a decision, not a side effect of table size.
	seats := []Seat{
		seat("m1", "goofspiel", "a1", "claude", "sc_1", "dev1", "win", 1.0),
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "loss", 1.0),
		seat("m1", "goofspiel", "a3", "llama", "sc_3", "dev3", "loss", 1.0),
		seat("m1", "goofspiel", "a4", "gemini", "sc_4", "dev4", "loss", 1.0),
	}
	cmp, ex := BuildComparisons(seats, DefaultBuildConfig())
	if len(cmp) != 6 {
		t.Fatalf("got %d comparisons from 4 seats, want 6 (excluded: %v)", len(cmp), ex)
	}
	var total float64
	for _, c := range cmp {
		total += c.Weight
	}
	if total < 0.999 || total > 1.001 {
		t.Errorf("total weight = %v, want 1 per match", total)
	}
	// Winners beat losers; losers tie each other.
	var wins, draws int
	for _, c := range cmp {
		switch c.Outcome {
		case Win, Loss:
			wins++
		case Draw:
			draws++
		}
	}
	if wins != 3 || draws != 3 {
		t.Errorf("got %d decisive and %d tied pairs, want 3 and 3", wins, draws)
	}
}

// The exclusion that makes this a MODEL board rather than a claims board.
func TestSeatsWithNoVerifiedModelAreRefused(t *testing.T) {
	seats := []Seat{
		seat("m1", "goofspiel", "a1", "", "sc_1", "dev1", "win", 1.0),
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "loss", 1.0),
	}
	cmp, ex := BuildComparisons(seats, DefaultBuildConfig())
	if len(cmp) != 0 {
		t.Fatalf("an unverified seat produced %d comparisons", len(cmp))
	}
	if ex["no_verified_model"] != 1 {
		t.Errorf("census = %v, want one no_verified_model", ex)
	}
	// And the surviving seat is reported too: a match that contributes nothing is worth seeing,
	// because it usually means one side was unverified.
	if ex["match_had_one_eligible_seat"] != 1 {
		t.Errorf("census = %v, want the orphaned match counted", ex)
	}
}

func TestThinCoverageSeatsAreRefused(t *testing.T) {
	// A seat we only half observed is a seat whose model we only half know. Admitting it would
	// reintroduce, at the data layer, exactly the thin-verification problem the coverage gate on
	// the boards exists to stop.
	seats := []Seat{
		seat("m1", "goofspiel", "a1", "claude", "sc_1", "dev1", "win", 0.20),
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "loss", 1.0),
	}
	_, ex := BuildComparisons(seats, DefaultBuildConfig())
	if ex["coverage_below_threshold"] != 1 {
		t.Errorf("census = %v, want one coverage_below_threshold", ex)
	}
}

func TestUnknownCoverageIsRefusedSeparatelyFromZero(t *testing.T) {
	// "We cannot say" and "nothing was proven" are different states and are counted differently,
	// because only one of them is a statement about the developer.
	seats := []Seat{
		{MatchID: "m1", Game: "goofspiel", AgentID: "a1", Model: "claude", Scaffold: "sc_1",
			DeveloperID: "dev1", Result: "win", CoverageKnown: false},
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "loss", 1.0),
	}
	_, ex := BuildComparisons(seats, DefaultBuildConfig())
	if ex["coverage_unknown"] != 1 {
		t.Errorf("census = %v, want one coverage_unknown", ex)
	}
	if ex["coverage_below_threshold"] != 0 {
		t.Errorf("unknown coverage was counted as below-threshold: %v", ex)
	}
}

func TestSeatsWithNoScaffoldAreRefused(t *testing.T) {
	// Without a harness fingerprint the seat cannot be assigned to a stratum, so its evidence
	// could never be separated from the developer behind it. Admitting it under an empty stratum
	// would pool every unfingerprinted agent into one fictional "harness" and then condition on it.
	seats := []Seat{
		seat("m1", "goofspiel", "a1", "claude", "", "dev1", "win", 1.0),
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "loss", 1.0),
	}
	_, ex := BuildComparisons(seats, DefaultBuildConfig())
	if ex["no_scaffold"] != 1 {
		t.Errorf("census = %v, want one no_scaffold", ex)
	}
}

// Mafia's exclusion is structural, and the census says so by name.
func TestMafiaIsExcludedByNameNotSilently(t *testing.T) {
	// A team game where the whole team wins together cannot support a within-match pairwise
	// comparison: same-role seats always share an outcome (no information) and cross-role seats
	// differ only by which team won, which role assignment decides. A reader asking "where is
	// Mafia" must find an answer, not an absence.
	seats := []Seat{
		seat("m1", "mafia", "a1", "claude", "sc_1", "dev1", "win", 1.0),
		seat("m1", "mafia", "a2", "gpt", "sc_2", "dev2", "loss", 1.0),
	}
	cmp, ex := BuildComparisons(seats, DefaultBuildConfig())
	if len(cmp) != 0 {
		t.Fatalf("mafia produced %d comparisons", len(cmp))
	}
	if ex["game_not_pairwise_mafia"] != 2 {
		t.Errorf("census = %v, want both mafia seats counted under a named reason", ex)
	}
}

func TestUnfinishedMatchesDoNotBecomeDraws(t *testing.T) {
	// An empty result means "we do not know how this ended". Treating it as a draw would feed the
	// board outcomes that never happened, and draws are informative here — so the error would not
	// even be conservative.
	seats := []Seat{
		seat("m1", "goofspiel", "a1", "claude", "sc_1", "dev1", "", 1.0),
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "", 1.0),
	}
	cmp, ex := BuildComparisons(seats, DefaultBuildConfig())
	if len(cmp) != 0 {
		t.Fatalf("an unfinished match produced %d comparisons: %+v", len(cmp), cmp)
	}
	if ex["no_result"] != 2 {
		t.Errorf("census = %v, want both seats counted as no_result", ex)
	}
}

func TestStratumNeedsBothDeveloperAndScaffold(t *testing.T) {
	// Developer alone would pool a person's every harness version, so improving a prompt would
	// look like the model getting better. Scaffold alone would pool two developers who wrote the
	// same system prompt, which says nothing about either.
	a := seat("m", "goofspiel", "a1", "m1", "sc_same", "dev_a", "win", 1)
	b := seat("m", "goofspiel", "a2", "m2", "sc_same", "dev_b", "loss", 1)
	if a.Stratum() == b.Stratum() {
		t.Fatal("two developers sharing a scaffold collapsed into one stratum")
	}
	c := seat("m", "goofspiel", "a3", "m1", "sc_other", "dev_a", "win", 1)
	if a.Stratum() == c.Stratum() {
		t.Fatal("one developer's two scaffolds collapsed into one stratum")
	}
}

func TestBuildIsDeterministicRegardlessOfSeatOrder(t *testing.T) {
	// Seats arrive in whatever order the database returned them. Comparison order sets the
	// floating-point summation order in the likelihood, so an unstable order would make the board
	// depend on the query plan.
	base := []Seat{
		seat("m2", "goofspiel", "a3", "llama", "sc_3", "dev3", "win", 1.0),
		seat("m1", "goofspiel", "a1", "claude", "sc_1", "dev1", "win", 1.0),
		seat("m2", "goofspiel", "a4", "claude", "sc_1", "dev1", "loss", 1.0),
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "loss", 1.0),
	}
	shuffled := []Seat{base[3], base[0], base[2], base[1]}
	c1, _ := BuildComparisons(base, DefaultBuildConfig())
	c2, _ := BuildComparisons(shuffled, DefaultBuildConfig())
	if len(c1) != len(c2) {
		t.Fatalf("different counts: %d vs %d", len(c1), len(c2))
	}
	for i := range c1 {
		if c1[i] != c2[i] {
			t.Fatalf("order-dependent build at %d: %+v vs %+v", i, c1[i], c2[i])
		}
	}
}

// The Games filter drops seats the caller did not ask for, and SAYS how many.
//
// It paired goofspiel against monopoly, which were the two pairwise games. With Monopoly
// withdrawn, goofspiel is the only one — and a second game cannot stand in, because mafia is
// excluded by the PAIRWISE check first and would leave this asserting someone else's filter.
//
// So the filter is exercised directly instead: ask for a game none of the seats played. The
// property is unchanged — seats outside the requested set are excluded, and counted under
// game_filtered rather than vanishing silently, which is the part that matters when a board
// comes out thinner than expected.
func TestGameFilterIsAppliedAndCounted(t *testing.T) {
	seats := []Seat{
		seat("m1", "goofspiel", "a1", "claude", "sc_1", "dev1", "win", 1.0),
		seat("m1", "goofspiel", "a2", "gpt", "sc_2", "dev2", "loss", 1.0),
	}
	bc := DefaultBuildConfig()
	bc.Games = []string{"some-other-arena"}
	cmp, ex := BuildComparisons(seats, bc)
	if len(cmp) != 0 {
		t.Fatalf("got %d comparisons, want 0 — no seat played the requested game", len(cmp))
	}
	if ex["game_filtered"] != 2 {
		t.Errorf("census = %v, want both seats counted as game_filtered", ex)
	}
}

func TestBuildEndToEndProducesAFittedBoard(t *testing.T) {
	// The whole path: seats in, ranked board out, with the census attached.
	var seats []Seat
	for i := 0; i < 40; i++ {
		// dev1 runs claude and dev2 runs gpt; claude wins most.
		res1, res2 := "win", "loss"
		if i%4 == 0 {
			res1, res2 = "loss", "win"
		}
		m := "m" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		seats = append(seats,
			seat(m, "goofspiel", "a1", "claude", "sc_1", "dev1", res1, 1.0),
			seat(m, "goofspiel", "a2", "gpt", "sc_2", "dev2", res2, 1.0),
		)
	}
	fc := fastConfig()
	b := Build(seats, DefaultBuildConfig(), fc)
	if len(b.Ratings) != 2 {
		t.Fatalf("got %d ratings, want 2: %s", len(b.Ratings), b.Summary())
	}
	if b.Ratings[0].Model != "claude" {
		t.Errorf("top model = %q, want claude (it won 75%%)", b.Ratings[0].Model)
	}
	if b.MinCoverage != 0.90 {
		t.Errorf("board did not record its coverage gate: %v", b.MinCoverage)
	}
	if b.Summary() == "" {
		t.Error("empty summary")
	}
}
