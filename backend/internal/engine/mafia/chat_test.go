package mafia

import (
	"strings"
	"testing"
)

func viewFor(seat, day int, public []Event) AgentView {
	alive := map[int]bool{}
	for i := 1; i <= 6; i++ {
		alive[i] = true
	}
	return AgentView{Seat: seat, Day: day, Phase: PhaseDiscussion, Alive: alive, Public: public}
}

func msg(from int, text string, target *int) Event {
	return Event{Type: EvMessage, Payload: MessagePayload{From: from, Text: text, Target: target}}
}

// Same seed, same table, same words — this is what keeps replay and audit honest.
func TestChatIsReproducibleFromTheSeed(t *testing.T) {
	seed := []byte("match-seed-alpha")
	v := viewFor(3, 1, []Event{msg(4, "something", nil)})
	for turn := 1; turn <= 5; turn++ {
		a1, ok1 := Speak(seed, v, turn)
		a2, ok2 := Speak(seed, v, turn)
		if ok1 != ok2 || a1.Text != a2.Text || a1.Tone != a2.Tone {
			t.Fatalf("turn %d not reproducible: %q vs %q", turn, a1.Text, a2.Text)
		}
	}
}

// Deterministic must not mean predictable. The old bot said one sentence forever; a
// developer learned its script in one match. Different seeds must sound different.
func TestDifferentMatchesSoundDifferent(t *testing.T) {
	v := viewFor(3, 1, []Event{msg(4, "something", nil)})
	seen := map[string]bool{}
	for _, seed := range []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8"} {
		if a, ok := Speak([]byte(seed), v, 1); ok {
			seen[a.Text] = true
		}
	}
	if len(seen) < 3 {
		t.Fatalf("only %d distinct lines across 8 matches — the table is repeating itself", len(seen))
	}
}

// Twelve seats reading as one bot is the tell. Personas must actually differ.
func TestSeatsHaveDifferentTemperaments(t *testing.T) {
	seed := []byte("persona-seed")
	seen := map[persona]bool{}
	for seat := 1; seat <= 12; seat++ {
		seen[personaFor(seed, seat)] = true
	}
	if len(seen) < 2 {
		t.Fatal("every seat drew the same persona; the table has no texture")
	}
}

// The core of "feels alive": a line lands because it is CONTINGENT. A seat under
// direct accusation must react to that, not carry on probing.
func TestAnAccusedSeatRespondsToItsAccuser(t *testing.T) {
	me := 3
	accuser := 5
	target := me
	v := viewFor(me, 1, []Event{msg(accuser, "I'm on seat 3", &target)})

	// Persona decides whether it defends or hits back, but either way the reaction
	// must be aimed at the accuser — never at a bystander.
	for _, seed := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		p := personaFor([]byte(seed), me)
		in, tgt := readTable(v, p)
		if in != intentDefend && in != intentDoubt {
			t.Fatalf("seed %q: accused seat chose %v instead of reacting", seed, in)
		}
		if tgt != accuser {
			t.Fatalf("seed %q: reacted to seat %d, not the accuser (%d)", seed, tgt, accuser)
		}
	}
}

// A silent seat is the classic pressure target, and it is behaviour-derived — which
// is what makes it read as a player noticing rather than a bot naming a number.
func TestSilentSeatsGetCalledOut(t *testing.T) {
	me := 1
	v := viewFor(me, 1, []Event{msg(2, "talking", nil), msg(3, "also talking", nil)})
	// Seats 4,5,6 have said nothing.
	found := false
	for _, seed := range []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8"} {
		if in, tgt := readTable(v, personaFor([]byte(seed), me)); in == intentPressure {
			if tgt < 4 {
				t.Fatalf("pressured seat %d, which had already spoken", tgt)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no persona ever pressures a silent seat")
	}
}

// Nobody talks every round on a real table, and silence is itself information.
func TestSomeSeatsStayQuiet(t *testing.T) {
	v := viewFor(2, 1, []Event{msg(4, "hello", nil)})
	spoke, quiet := 0, 0
	for turn := 1; turn <= 60; turn++ {
		if _, ok := Speak([]byte("quiet-seed"), v, turn); ok {
			spoke++
		} else {
			quiet++
		}
	}
	if spoke == 0 || quiet == 0 {
		t.Fatalf("spoke %d, quiet %d — the seat is stuck at one behaviour", spoke, quiet)
	}
}

// A malformed line ends the illusion instantly. No raw verbs, no "seat -1".
func TestNoLineEverRendersAPlaceholderOrNegativeSeat(t *testing.T) {
	seeds := []string{"x1", "x2", "x3", "x4", "x5"}
	views := []AgentView{
		viewFor(1, 0, nil),                        // empty table, no target
		viewFor(1, 1, []Event{msg(2, "hi", nil)}), // someone spoke
		{Seat: 1, Day: 2, Phase: PhaseDiscussion, Alive: map[int]bool{1: true}}, // alone
	}
	for _, seed := range seeds {
		for _, v := range views {
			for turn := 1; turn <= 20; turn++ {
				a, ok := Speak([]byte(seed), v, turn)
				if !ok {
					continue
				}
				if strings.Contains(a.Text, "%") {
					t.Fatalf("unrendered placeholder: %q", a.Text)
				}
				if strings.Contains(a.Text, "-1") {
					t.Fatalf("negative seat leaked into a line: %q", a.Text)
				}
				if strings.TrimSpace(a.Text) == "" {
					t.Fatal("empty line emitted as a message")
				}
			}
		}
	}
}

// Chat must not disturb the streams that decide the game, or replay breaks and the
// fairness guarantee with it.
func TestChatCannotPerturbMoveSelection(t *testing.T) {
	seed := []byte("fairness-seed")
	v := AgentView{
		Seat: 2, Day: 1, Phase: PhaseVoting,
		Alive: map[int]bool{1: true, 2: true, 3: true, 4: true},
		Legal: []string{ActVote},
	}

	quiet := NewBot("a", seed, 2)
	votesWithoutChat := []int{}
	for i := 0; i < 5; i++ {
		votesWithoutChat = append(votesWithoutChat, quiet.Decide(v).Target)
	}

	// The same bot, but made to talk a lot first.
	chatty := NewBot("b", seed, 2)
	talk := viewFor(2, 1, []Event{msg(3, "noise", nil)})
	for i := 0; i < 25; i++ {
		chatty.Decide(talk)
	}
	votesAfterChat := []int{}
	for i := 0; i < 5; i++ {
		votesAfterChat = append(votesAfterChat, chatty.Decide(v).Target)
	}

	for i := range votesWithoutChat {
		if votesWithoutChat[i] != votesAfterChat[i] {
			t.Fatalf("talking changed vote %d (%d -> %d): chat is drawing from the move stream",
				i, votesWithoutChat[i], votesAfterChat[i])
		}
	}
}

// Real players pile onto the same target constantly — but never in the same words.
// Several seats drawing the identical sentence in one round is a worse tell than the
// single hardcoded line this replaced, and it happens easily: independent streams,
// few templates per intent, six speakers.
func TestSeatsDoNotRepeatEachOtherInARound(t *testing.T) {
	seed := []byte("dedupe-seed")
	alive := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true}
	var public []Event
	said := map[string]int{}

	for seat := 1; seat <= 6; seat++ {
		v := AgentView{Seat: seat, Day: 1, Phase: PhaseDiscussion, Alive: alive, Public: public}
		a, ok := Speak(seed, v, 1)
		if !ok {
			continue
		}
		said[a.Text]++
		public = append(public, Event{Type: EvMessage,
			Payload: MessagePayload{From: seat, Text: a.Text}})
	}
	for text, n := range said {
		if n > 1 {
			t.Fatalf("%d seats said %q verbatim in one round", n, text)
		}
	}
}
