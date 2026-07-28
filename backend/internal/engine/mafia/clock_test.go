package mafia

import "testing"

// Silence at night is a RULE of the game, not a UI nicety: the town is asleep, so
// no seat may speak. The floor is also closed once the ballot opens.
func TestSayOnlyDuringDiscussion(t *testing.T) {
	e := New()
	s, _ := e.Init(seed, StandardSeats())

	if s.Phase != PhaseNight {
		t.Fatalf("expected the match to open at night, got %q", s.Phase)
	}
	if _, _, err := e.Say(s, 1, "who is with me?", "alliance", 0); err == nil {
		t.Fatal("an agent spoke during the night — the town is supposed to be asleep")
	}

	s.Phase = PhaseVoting
	if _, _, err := e.Say(s, 1, "wait, hear me out", "defend", 0); err == nil {
		t.Fatal("an agent spoke during voting — the floor should be closed")
	}

	s.Phase = PhaseDiscussion
	if _, _, err := e.Say(s, 1, "seat 4 is lying", "accuse", 4); err != nil {
		t.Fatalf("speech rejected while the floor is open: %v", err)
	}
}

// The whole point of the change: an agent accused on the floor must be able to
// answer immediately and repeatedly. One statement per seat is a roll-call, not a
// debate. Talking must also never close the phase early.
func TestSayIsFreeFormAndDoesNotAdvancePhase(t *testing.T) {
	e := New()
	s, _ := e.Init(seed, StandardSeats())
	s.Phase = PhaseDiscussion
	beforeMessages := s.Messages

	var evs []Event
	for i := 0; i < 5; i++ {
		var e2 []Event
		var err error
		if s, e2, err = e.Say(s, 1, "i keep talking", "info", 0); err != nil {
			t.Fatalf("line %d rejected — speech should be unlimited on an open floor: %v", i, err)
		}
		evs = append(evs, e2...)
	}
	if len(evs) != 5 {
		t.Fatalf("emitted %d message events, want 5 — spectators must see every line", len(evs))
	}
	if s.Phase != PhaseDiscussion {
		t.Fatalf("chatter closed the floor: phase is now %q", s.Phase)
	}
	if s.Messages != beforeMessages {
		t.Fatalf("table talk consumed formal statements (%d -> %d) — it must not rush the phase",
			beforeMessages, s.Messages)
	}
}

// A dead seat has no voice.
func TestSayRejectedWhenDead(t *testing.T) {
	e := New()
	s, _ := e.Init(seed, StandardSeats())
	s.Phase = PhaseDiscussion
	s.Alive[2] = false
	if _, _, err := e.Say(s, 2, "from beyond the grave", "info", 0); err != ErrNotAlive {
		t.Fatalf("dead seat spoke: err = %v, want ErrNotAlive", err)
	}
}
