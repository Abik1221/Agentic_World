package mafia

import "testing"

// A quiet round must not be an automatic unanimous lynch of the lowest seat.
//
// Observed on a live sandbox table: an agent seated at 1 was voted out on day one,
// ten votes to nothing, having done nothing but say "I'm watching everyone closely
// tonight." The next round the table lynched seat 2, then 3, then 5, then 6 — every
// time the lowest living seat. The whole game was decided by seating order and no
// discussion could change it, which makes a Mafia practice table worthless: an agent
// that draws a low seat dies before it can play, and one that draws a high seat wins
// by waiting.
//
// Two separate defects produced that, and both are pinned here because either alone
// brings the behaviour back:
//
//  1. The fallback bot in internal/mafia attached a target to "Observing the table." —
//     a line that names nobody. The engine bots read the transcript for who to vote,
//     so a seat saying nothing registered as accusing the lowest living seat, and
//     everyone followed an accusation that was never made.
//
//  2. With no accusation to follow, town returned aliveOthers()[0]. Every silent bot
//     picks the SAME seat, so the tie is not just predictable, it is decisive.

func votingView(seat int, alive map[int]bool, public []Event) AgentView {
	return AgentView{
		Seat: seat, Day: 1, Phase: PhaseVoting,
		Role: RoleVillager, Alive: alive, Public: public,
	}
}

func aliveSet(seats ...int) map[int]bool {
	m := map[int]bool{}
	for _, s := range seats {
		m[s] = true
	}
	return m
}

// votesOn returns what each living seat votes, given a shared transcript.
func votesOn(alive map[int]bool, public []Event) map[int]int {
	out := map[int]int{}
	for seat := range alive {
		b := NewBot("house", []byte("seed-lynch-order"), seat)
		out[seat] = b.voteTarget(votingView(seat, alive, public))
	}
	return out
}

func TestAQuietRoundDoesNotLynchTheLowestSeat(t *testing.T) {
	// Day one after a night kill: the table only mourns the victim. Nobody accuses a
	// living seat, which is exactly the round the observed table got wrong.
	alive := aliveSet(1, 2, 3, 5, 6, 7)
	dead := 4
	public := []Event{
		msg(2, "They went for seat 4. That tells us who felt threatened.", &dead),
		msg(3, "Seat 4 is gone.", &dead),
		msg(5, "Observing the table.", nil),
		msg(6, "Observing the table.", nil),
		msg(7, "Losing seat 4 hurts.", &dead),
	}

	votes := votesOn(alive, public)

	tally := map[int]int{}
	for _, target := range votes {
		tally[target]++
	}
	// Assert on CONCENTRATION, not unanimity.
	//
	// aliveOthers() excludes self, so even the broken fallback was never unanimous:
	// the lowest seat voted the second-lowest while everyone else voted the lowest.
	// That is 5 of 6 — decisive, and a test looking for 6 of 6 passes happily while
	// the table lynches by seat order anyway. It measured a property the bug did not
	// have.
	//
	// The property that matters is that a table with nothing to go on does not
	// converge. Measured: 5 of 6 before the fix, 2 of 6 after.
	worst, at := 0, 0
	for target, n := range tally {
		if n > worst {
			worst, at = n, target
		}
	}
	if worst > len(alive)/2 {
		t.Fatalf("%d of %d seats converged on seat %d with no accusation on the table — "+
			"that is seat order, not a read (votes: %v)", worst, len(alive), at, votes)
	}
}

func TestAnUntargetedLineIsNotAnAccusation(t *testing.T) {
	// The poisoning half. If a nameless line carries a target again, every bot reads
	// it as a read and follows it — which is how one silent seat swung a whole table.
	alive := aliveSet(1, 2, 3, 5, 6, 7)
	one := 1
	poisoned := []Event{
		msg(5, "Observing the table.", &one),
		msg(6, "Observing the table.", &one),
		msg(7, "Observing the table.", &one),
	}

	b := NewBot("house", []byte("seed-lynch-order"), 2)
	got := b.accusationTarget(votingView(2, alive, poisoned))
	if got != 1 {
		t.Fatalf("guard is not measuring what it claims: targeted messages should tally, got %d", got)
	}

	// The same transcript with the targets removed — what the fixed bots now emit —
	// must leave nothing to follow.
	clean := []Event{
		msg(5, "Observing the table.", nil),
		msg(6, "Observing the table.", nil),
		msg(7, "Observing the table.", nil),
	}
	if got := b.accusationTarget(votingView(2, alive, clean)); got != -1 {
		t.Fatalf("a table that named nobody produced accusation target %d, want -1", got)
	}
}

func TestBotsStillFollowARealAccusation(t *testing.T) {
	// The other half: spreading the silent vote must not cost the bots their reads.
	// Talk you can influence changing the vote is the whole point of practising here.
	alive := aliveSet(1, 2, 3, 5, 6, 7)
	accused := 6
	public := []Event{
		msg(2, "That's a claim, not a case. Try again, seat 6.", &accused),
		msg(3, "You went quiet the moment pressure moved, seat 6.", &accused),
		msg(5, "Observing the table.", nil),
	}

	votes := votesOn(alive, public)
	for seat, target := range votes {
		if seat == accused {
			continue // the accused votes elsewhere, and must not vote itself
		}
		if target != accused {
			t.Errorf("seat %d voted %d, want %d — the table's accusation was ignored",
				seat, target, accused)
		}
	}
	if votes[accused] == accused {
		t.Errorf("seat %d voted for itself", accused)
	}
}

func TestTheSilentVoteIsStillDeterministic(t *testing.T) {
	// Replay and audit depend on it. Spreading the vote must not mean randomising it:
	// the same seed and the same table have to produce the same ballot every time.
	alive := aliveSet(1, 2, 3, 5, 6, 7)
	public := []Event{msg(5, "Observing the table.", nil)}

	first := votesOn(alive, public)
	for i := 0; i < 5; i++ {
		again := votesOn(alive, public)
		for seat, target := range first {
			if again[seat] != target {
				t.Fatalf("seat %d voted %d then %d — not replay-stable", seat, target, again[seat])
			}
		}
	}
}
