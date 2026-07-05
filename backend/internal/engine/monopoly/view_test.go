package monopoly

import "testing"

// TestPublicStateHidesFutureCards proves the deck orders (future cards) are
// stripped from the agent/spectator view while the rest of the perfect-info
// state is preserved, and that the original state is not mutated.
func TestPublicStateHidesFutureCards(t *testing.T) {
	e := New(DefaultConfig())
	s, _ := e.Init([]byte("view-seed"))

	if len(s.ChanceOrder) == 0 || len(s.CCOrder) == 0 {
		t.Fatal("expected the private state to carry shuffled decks")
	}

	pub := s.PublicState()
	if pub.ChanceOrder != nil || pub.CCOrder != nil {
		t.Fatal("future card decks leaked into the public state")
	}
	// Everything an agent may legitimately see is retained.
	if len(pub.Players) != len(s.Players) || len(pub.Holdings) != len(s.Holdings) {
		t.Fatal("public state dropped board/player info an agent is entitled to")
	}
	if pub.Current != s.Current || pub.Phase != s.Phase {
		t.Fatal("public state altered turn/phase")
	}
	// Redaction must not mutate the source state.
	if len(s.ChanceOrder) == 0 || len(s.CCOrder) == 0 {
		t.Fatal("PublicState mutated the caller's state")
	}
}

// TestPendingSeat confirms the exported pending-seat accessor tracks the phase.
func TestPendingSeat(t *testing.T) {
	e := New(DefaultConfig())
	s, _ := e.Init([]byte("view-seed"))
	if got := e.PendingSeat(s); got != s.Current {
		t.Fatalf("pending seat = %d at roll, want current %d", got, s.Current)
	}
}
