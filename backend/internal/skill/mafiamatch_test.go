package skill

import "testing"

// Seats 1,2 are mafia; 3,4,5,6 are town.
func fourTownTwoMafia() []MafiaSeatRow {
	return []MafiaSeatRow{
		{Seat: 1, AgentID: 101, IsMafia: true},
		{Seat: 2, AgentID: 102, IsMafia: true},
		{Seat: 3, AgentID: 103},
		{Seat: 4, AgentID: 104},
		{Seat: 5, AgentID: 105},
		{Seat: 6, AgentID: 106},
	}
}

func scoreOf(t *testing.T, got []MafiaSeatScore, seat int) MafiaSeatScore {
	t.Helper()
	for _, s := range got {
		if s.Seat == seat {
			return s
		}
	}
	t.Fatalf("seat %d was not scored; got %+v", seat, got)
	return MafiaSeatScore{}
}

// A town seat that finds mafia every time must beat one that never does, and the winner must
// score above chance rather than merely above the loser.
func TestTownAccuracySeparatesGoodFromBadVoters(t *testing.T) {
	in := MafiaMatchInput{
		MatchID: "m_test",
		Seats:   fourTownTwoMafia(),
		Events: []MafiaEventRow{
			{Seq: 1, Type: "vote", Day: 1, From: 3, Target: 1}, // town → mafia, correct
			{Seq: 2, Type: "vote", Day: 1, From: 4, Target: 5}, // town → town, wrong
			{Seq: 3, Type: "vote", Day: 2, From: 3, Target: 2}, // correct again
			{Seq: 4, Type: "vote", Day: 2, From: 4, Target: 6}, // wrong again
		},
	}
	got := ScoreMafiaMatch(in)
	good, bad := scoreOf(t, got, 3), scoreOf(t, got, 4)

	if good.Skill.Lift <= 0 {
		t.Fatalf("a seat that voted mafia every time scored lift %.3f — at or below random, so "+
			"the metric is not detecting correct play", good.Skill.Lift)
	}
	if bad.Skill.Lift >= good.Skill.Lift {
		t.Fatalf("the seat that never found a mafia (lift %.3f) scored at least as well as the "+
			"seat that always did (lift %.3f)", bad.Skill.Lift, good.Skill.Lift)
	}
	if good.Regret >= bad.Regret {
		t.Fatalf("regret is inverted: good seat %.3f, bad seat %.3f", good.Regret, bad.Regret)
	}
}

// A mafia voting its own teammate is the one error no uncertainty excuses, and it must be
// punished below a mafia that votes town.
func TestSelfBetrayalIsPunished(t *testing.T) {
	in := MafiaMatchInput{
		MatchID: "m_test",
		Seats:   fourTownTwoMafia(),
		Events: []MafiaEventRow{
			{Seq: 1, Type: "vote", Day: 1, From: 1, Target: 2}, // mafia → own teammate
			{Seq: 2, Type: "vote", Day: 1, From: 2, Target: 4}, // mafia → town, correct play
		},
	}
	got := ScoreMafiaMatch(in)
	betrayer, loyal := scoreOf(t, got, 1), scoreOf(t, got, 2)
	if betrayer.Skill.SelfBetrayals != 1 {
		t.Fatalf("self-betrayal not counted: %+v", betrayer.Skill)
	}
	if betrayer.Skill.Quality >= loyal.Skill.Quality {
		t.Fatalf("a mafia that voted its own teammate scored %.3f, at least as well as one that "+
			"voted town (%.3f)", betrayer.Skill.Quality, loyal.Skill.Quality)
	}
}

// TestEligibilityIsReplayedNotTakenFromTheEnd is the one that pins the replay.
//
// The chance rate depends on how many mafia are alive among the seats a voter could pick. If
// eliminations are applied before the votes that preceded them — the shape you get by reading
// the final alive set instead of replaying — an early vote is judged against a late-game
// chance rate. Here mafia seat 1 is eliminated after day 1: the day-1 vote faced 2 mafia among
// 5 eligible (40% chance), the day-2 vote faced 1 among 4 (25%). Collapsing both onto the
// end state would score the day-1 vote as if it had been the harder one.
func TestEligibilityIsReplayedNotTakenFromTheEnd(t *testing.T) {
	in := MafiaMatchInput{
		MatchID: "m_test",
		Seats:   fourTownTwoMafia(),
		Events: []MafiaEventRow{
			{Seq: 1, Type: "vote", Day: 1, From: 3, Target: 1},
			{Seq: 2, Type: "eliminate", Seat: 1},
			{Seq: 3, Type: "vote", Day: 2, From: 3, Target: 2},
		},
	}
	got := ScoreMafiaMatch(in)
	s := scoreOf(t, got, 3)
	if s.Skill.Votes != 2 {
		t.Fatalf("expected 2 scored votes, got %d", s.Skill.Votes)
	}
	// Day 1: eligible {1,2,4,5,6}, mafia alive 2 → 0.40. Day 2: eligible {2,4,5,6}, 1 → 0.25.
	const want = (0.40 + 0.25) / 2
	if diff := s.Skill.Chance - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("chance rate %.4f, want %.4f — the eligible set is not being replayed in "+
			"sequence, so votes are scored against the wrong baseline", s.Skill.Chance, want)
	}
}

// A seat that never voted must be absent, not present with a zero.
func TestSilentSeatsAreOmittedRatherThanScoredZero(t *testing.T) {
	in := MafiaMatchInput{
		MatchID: "m_test",
		Seats:   fourTownTwoMafia(),
		Events:  []MafiaEventRow{{Seq: 1, Type: "vote", Day: 1, From: 3, Target: 1}},
	}
	got := ScoreMafiaMatch(in)
	if len(got) != 1 || got[0].Seat != 3 {
		t.Fatalf("only the seat that voted should be scored, got %+v", got)
	}
}
