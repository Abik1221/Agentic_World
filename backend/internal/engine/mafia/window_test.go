package mafia

import "testing"

// The transcript window, and the line it must not cross.
//
// WHY THIS FILE EXISTS. The first version of the windowing was applied inside BuildView, and
// the whole Mafia suite passed. It was still wrong in two ways nothing checked:
//
//   - dispatchPublicEvents walks the view's Public to decide what to push. Windowing there
//     would have SILENTLY DROPPED every event past the window from the real-time stream, so
//     an agent would simply never learn what was said — the exact guarantee the push path
//     exists to provide.
//   - the game-end payload ships Public as the match archive, documented as the complete
//     transcript. It would have shipped a fraction of it.
//
// So the rule these tests pin is not "the window is 60". It is WHERE the trim may happen:
// BuildView stays whole, and only the per-turn payload is trimmed.

func publicLog(n int) []Event {
	out := make([]Event, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Event{Seq: i + 1, Type: EvMessage})
	}
	return out
}

// BuildView must return the WHOLE transcript. This is the regression guard: a future change
// that "optimises" by trimming here breaks the push stream and the match archive at once.
func TestBuildViewKeepsTheWholeTranscript(t *testing.T) {
	const n = MaxPublicWindow * 3
	s := State{Roles: map[int]string{0: RoleVillager}, Alive: map[int]bool{0: true}}

	v := BuildView(s, 0, publicLog(n))

	if len(v.Public) != n {
		t.Fatalf("BuildView returned %d of %d public events.\n"+
			"It must stay COMPLETE: dispatchPublicEvents pushes from this slice (so trimming "+
			"here silently drops events an agent never receives) and the game-end record ships "+
			"it as the full match transcript. Trim at the payload boundary with WindowPublic.",
			len(v.Public), n)
	}
	if v.Digest != nil {
		t.Error("BuildView produced a Digest; summarising belongs at the payload boundary")
	}
}

// WindowPublic is where the trimming is allowed, and it must keep the RECENT end — an agent
// answers the conversation it is in, not the one it has already replied to.
func TestWindowPublicKeepsTheMostRecentEvents(t *testing.T) {
	log := publicLog(MaxPublicWindow + 25)

	windowed, digest := WindowPublic(log)

	if len(windowed) != MaxPublicWindow {
		t.Fatalf("windowed to %d events, want %d", len(windowed), MaxPublicWindow)
	}
	// The last event of the log must be the last event of the window. Keeping the OLDEST
	// instead would hand an agent the opening of the match and hide the line it is replying
	// to — worse than sending nothing, because it looks complete.
	if got, want := windowed[len(windowed)-1].Seq, log[len(log)-1].Seq; got != want {
		t.Errorf("window ends at seq %d, want the newest %d — the window kept the wrong end", got, want)
	}
	if digest == nil {
		t.Fatal("no digest for a trimmed transcript: an agent cannot tell history exists")
	}
	if digest.Events != 25 {
		t.Errorf("digest reports %d dropped events, want 25", digest.Events)
	}
}

// A transcript that fits is returned untouched, with no digest — a digest saying "0 events
// were dropped" is noise on every early turn of every match.
func TestWindowPublicLeavesAShortTranscriptAlone(t *testing.T) {
	log := publicLog(MaxPublicWindow)

	windowed, digest := WindowPublic(log)

	if len(windowed) != MaxPublicWindow {
		t.Errorf("trimmed a transcript that already fits: %d events", len(windowed))
	}
	if digest != nil {
		t.Errorf("produced a digest (%+v) for a transcript that fit", digest)
	}
}

// The digest carries FACTS, never prose. Mafia is played through speech, and a reworded
// version of what a player said is not what they said — an agent reasoning over a paraphrase
// would be deceived by the platform rather than by another player.
func TestDigestCountsWithoutRewordingAnybody(t *testing.T) {
	dropped := []Event{
		{Seq: 1, Type: EvMessage, Payload: MessagePayload{From: 3, Text: "I am the doctor, trust me"}},
		{Seq: 2, Type: EvVote, Payload: VotePayload{From: 1, Target: 3}},
		{Seq: 3, Type: EvEliminate, Payload: EliminatePayload{Target: 3, Cause: "vote"}},
		{Seq: 4, Type: EvMessage, Payload: MessagePayload{From: 2, Text: "told you"}},
	}

	d := digestOf(dropped)

	if d == nil {
		t.Fatal("no digest for dropped events")
	}
	if d.Messages != 2 || d.Votes != 1 || d.Eliminations != 1 {
		t.Errorf("counts = messages %d, votes %d, eliminations %d; want 2/1/1",
			d.Messages, d.Votes, d.Eliminations)
	}
	// Who died, in order, is the load-bearing fact from omitted history: Alive says who
	// remains, and only this says the sequence that got them there.
	if len(d.Eliminated) != 1 || d.Eliminated[0] != 3 {
		t.Errorf("eliminated = %v, want [3]", d.Eliminated)
	}
}
