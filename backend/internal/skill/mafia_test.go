package skill

import (
	"math"
	"testing"
)

// A 12-seat table: seats 1-3 mafia, 4-12 town.
func table12() MafiaTruth {
	m := map[int]bool{}
	for s := 1; s <= 12; s++ {
		m[s] = s <= 3
	}
	return MafiaTruth{IsMafia: m}
}

func eligibleExcept(self int) []int {
	out := []int{}
	for s := 1; s <= 12; s++ {
		if s != self {
			out = append(out, s)
		}
	}
	return out
}

// ── The property the whole design rests on ──────────────────────────────────

// Random play must score ZERO lift, not some positive number that makes a coin-flipping
// agent look competent. This is what makes the metric mean something: the scale is
// anchored at chance, not at zero accuracy.
func TestRandomPlayScoresZeroLift(t *testing.T) {
	truth := table12()
	// Seat 4 (town) votes with exactly the chance rate: 3 mafia among 11 eligible ≈ 27%.
	// Simulate it by hitting a mafia on 3 of every 11 votes.
	var votes []MafiaVote
	targets := []int{1, 2, 3, 5, 6, 7, 8, 9, 10, 11, 12} // 3 mafia, 8 town
	for i, tg := range targets {
		votes = append(votes, MafiaVote{Day: i + 1, Voter: 4, Target: tg, Eligible: eligibleExcept(4)})
	}
	got, ok := ScoreMafiaVotes(4, votes, truth)
	if !ok {
		t.Fatal("not scorable")
	}
	if math.Abs(got.Lift) > 1e-9 {
		t.Fatalf("chance-rate play scored lift %.6f, want 0 — the scale is not anchored at "+
			"random, so a coin-flipping agent would rank above a non-participant", got.Lift)
	}
}

// Perfect identification scores 1.
func TestPerfectTownPlayScoresFullLift(t *testing.T) {
	truth := table12()
	var votes []MafiaVote
	for i, tg := range []int{1, 2, 3} { // every vote lands on a real mafia
		votes = append(votes, MafiaVote{Day: i + 1, Voter: 7, Target: tg, Eligible: eligibleExcept(7)})
	}
	got, _ := ScoreMafiaVotes(7, votes, truth)
	if math.Abs(got.Lift-1) > 1e-9 {
		t.Fatalf("perfect town play scored lift %.6f, want 1", got.Lift)
	}
	if got.Quality != 1 {
		t.Fatalf("quality %.6f want 1", got.Quality)
	}
}

// Worse-than-random must be visible as negative lift. An agent that systematically votes
// town is destroying information and the metric has to say so rather than flooring at 0.
func TestWorseThanRandomScoresNegativeLift(t *testing.T) {
	truth := table12()
	var votes []MafiaVote
	for i, tg := range []int{5, 6, 8, 9, 10} { // never once votes a mafia
		votes = append(votes, MafiaVote{Day: i + 1, Voter: 7, Target: tg, Eligible: eligibleExcept(7)})
	}
	got, _ := ScoreMafiaVotes(7, votes, truth)
	if got.Lift >= 0 {
		t.Fatalf("play that never found a mafia scored lift %.4f, want negative", got.Lift)
	}
	// Quality floors at 0 so one terrible match cannot push a season score below what a
	// developer who never played would get.
	if got.Quality != 0 {
		t.Fatalf("quality %.4f want 0 (floored)", got.Quality)
	}
}

// ── The confound that makes raw accuracy useless ────────────────────────────

// The same accuracy on tables with different mafia density must NOT score the same. This
// is the whole reason for the chance normalisation: without it, agents would be ranked by
// which table shape they happened to draw.
func TestLiftCorrectsForTableShape(t *testing.T) {
	// Dense table: 3 mafia among 4 eligible → 75% chance rate.
	dense := MafiaTruth{IsMafia: map[int]bool{1: true, 2: true, 3: true, 4: false, 5: false}}
	denseVotes := []MafiaVote{
		{Day: 1, Voter: 5, Target: 1, Eligible: []int{1, 2, 3, 4}},
		{Day: 2, Voter: 5, Target: 4, Eligible: []int{1, 2, 3, 4}},
	}
	d, _ := ScoreMafiaVotes(5, denseVotes, dense)

	// Sparse table: 1 mafia among 4 eligible → 25% chance rate. Same 50% accuracy.
	sparse := MafiaTruth{IsMafia: map[int]bool{1: true, 2: false, 3: false, 4: false, 5: false}}
	sparseVotes := []MafiaVote{
		{Day: 1, Voter: 5, Target: 1, Eligible: []int{1, 2, 3, 4}},
		{Day: 2, Voter: 5, Target: 2, Eligible: []int{1, 2, 3, 4}},
	}
	s, _ := ScoreMafiaVotes(5, sparseVotes, sparse)

	if math.Abs(d.Accuracy-s.Accuracy) > 1e-9 {
		t.Fatalf("test setup: accuracies differ (%.2f vs %.2f)", d.Accuracy, s.Accuracy)
	}
	if !(s.Lift > d.Lift) {
		t.Fatalf("identical 50%% accuracy scored lift %.3f on a sparse table and %.3f on a "+
			"dense one; finding a mafia when they are rare is harder and must score higher",
			s.Lift, d.Lift)
	}
	// On the dense table, 50% accuracy is WORSE than guessing (75% chance).
	if d.Lift >= 0 {
		t.Errorf("50%% accuracy against a 75%% chance rate scored %.3f, want negative", d.Lift)
	}
}

// ── Role conditioning ───────────────────────────────────────────────────────

// A mafia voting a townsfolk is doing its job. Scoring it on the town objective would
// punish it for playing its role correctly.
func TestMafiaIsScoredOnItsOwnObjective(t *testing.T) {
	truth := table12()
	votes := []MafiaVote{
		{Day: 1, Voter: 1, Target: 7, Eligible: eligibleExcept(1)},
		{Day: 2, Voter: 1, Target: 8, Eligible: eligibleExcept(1)},
	}
	got, _ := ScoreMafiaVotes(1, votes, truth)
	if got.Role != "mafia" {
		t.Fatalf("role=%q want mafia", got.Role)
	}
	if got.Hits != 2 {
		t.Fatalf("a mafia voting town scored %d/2 hits — it is being graded on the town "+
			"objective and punished for playing its role", got.Hits)
	}
	if got.SelfBetrayals != 0 {
		t.Fatalf("self-betrayals=%d want 0", got.SelfBetrayals)
	}
}

// A mafia voting its own teammate carries no uncertainty — it knows the team. That is an
// unambiguous error and must be penalised beyond the lift scale, which is calibrated for
// decisions made under uncertainty.
func TestMafiaVotingItsOwnTeamIsPenalised(t *testing.T) {
	truth := table12()
	clean, _ := ScoreMafiaVotes(1, []MafiaVote{
		{Day: 1, Voter: 1, Target: 7, Eligible: eligibleExcept(1)},
		{Day: 2, Voter: 1, Target: 8, Eligible: eligibleExcept(1)},
	}, truth)
	betrayer, _ := ScoreMafiaVotes(1, []MafiaVote{
		{Day: 1, Voter: 1, Target: 7, Eligible: eligibleExcept(1)},
		{Day: 2, Voter: 1, Target: 2, Eligible: eligibleExcept(1)}, // seat 2 is a teammate
	}, truth)

	if betrayer.SelfBetrayals != 1 {
		t.Fatalf("self-betrayals=%d want 1", betrayer.SelfBetrayals)
	}
	if !(betrayer.Quality < clean.Quality) {
		t.Fatalf("voting a teammate scored quality %.3f, no worse than clean play's %.3f",
			betrayer.Quality, clean.Quality)
	}
}

// ── Silence ─────────────────────────────────────────────────────────────────

// An abstain must be EXCLUDED from accuracy, not counted as a miss. Silence is already
// punished by the absence-forfeit rules; counting it here would punish it twice and would
// also make a timed-out agent look like a bad player rather than an unreachable one.
func TestAbstentionsAreExcludedNotCountedWrong(t *testing.T) {
	truth := table12()
	got, ok := ScoreMafiaVotes(7, []MafiaVote{
		{Day: 1, Voter: 7, Target: 1, Eligible: eligibleExcept(7)}, // hit
		{Day: 2, Voter: 7, Target: 0, Eligible: eligibleExcept(7)}, // silence
		{Day: 3, Voter: 7, Target: 0, Eligible: eligibleExcept(7)}, // silence
	}, truth)
	if !ok {
		t.Fatal("not scorable")
	}
	if got.Votes != 1 {
		t.Fatalf("votes=%d want 1 — abstentions were folded into the denominator", got.Votes)
	}
	if got.Abstentions != 2 {
		t.Fatalf("abstentions=%d want 2 — silence must stay visible, not vanish", got.Abstentions)
	}
	if got.Accuracy != 1 {
		t.Fatalf("accuracy=%.2f want 1.0", got.Accuracy)
	}
}

// A seat that never voted is excluded entirely rather than scored zero: "never played"
// and "played badly" are different facts, and averaging them lets an absent agent look
// merely mediocre.
func TestSeatThatNeverVotedIsExcluded(t *testing.T) {
	truth := table12()
	_, ok := ScoreMafiaVotes(7, []MafiaVote{
		{Day: 1, Voter: 7, Target: 0, Eligible: eligibleExcept(7)},
	}, truth)
	if ok {
		t.Fatal("a seat that only ever abstained was scored instead of excluded")
	}
}

// ── Survival ────────────────────────────────────────────────────────────────

// The trap in "just count rounds survived": it must be measured against the role's own
// baseline, or the metric pays agents to sit still and be forgettable.
func TestSurvivalIsRelativeToTheRoleBaseline(t *testing.T) {
	atBaseline := MafiaSurvival(4, 4)
	if math.Abs(atBaseline-0.5) > 1e-9 {
		t.Fatalf("surviving exactly the baseline scored %.4f, want 0.5", atBaseline)
	}
	if !(MafiaSurvival(8, 4) > atBaseline) {
		t.Fatal("outlasting the baseline did not score above it")
	}
	if !(MafiaSurvival(1, 4) < atBaseline) {
		t.Fatal("dying early did not score below the baseline")
	}
	// Bounded: no amount of survival can dominate the rest of the score.
	if got := MafiaSurvival(1000, 1); got > 1 {
		t.Fatalf("survival score %.4f exceeded 1", got)
	}
	// An unknown baseline is neutral, not a free full score.
	if got := MafiaSurvival(5, 0); got != 0.5 {
		t.Fatalf("unknown baseline scored %.4f, want 0.5", got)
	}
}

// Determinism, again — map iteration is the classic way a scorer silently stops being
// reproducible.
func TestMafiaScoringIsDeterministic(t *testing.T) {
	truth := table12()
	votes := []MafiaVote{
		{Day: 1, Voter: 7, Target: 1, Eligible: eligibleExcept(7)},
		{Day: 2, Voter: 7, Target: 9, Eligible: eligibleExcept(7)},
	}
	first, _ := ScoreMafiaVotes(7, votes, truth)
	for i := 0; i < 50; i++ {
		got, _ := ScoreMafiaVotes(7, votes, truth)
		if got != first {
			t.Fatalf("run %d differed from the first", i)
		}
	}
	seats := SortedSeats(truth)
	for i := 1; i < len(seats); i++ {
		if seats[i-1] >= seats[i] {
			t.Fatal("SortedSeats is not ascending")
		}
	}
}
