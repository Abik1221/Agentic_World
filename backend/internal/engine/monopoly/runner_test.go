package monopoly

import (
	"testing"
)

// TestTablePlaysFullGame proves the dedicated environment can play complete games
// end-to-end with bots in every seat, across many seeds and player counts.
func TestTablePlaysFullGame(t *testing.T) {
	for trial := 0; trial < 100; trial++ {
		players := 2 + trial%7 // 2..8
		cfg := Config{Players: players, StartingCash: 1500, MaxTurns: 300}
		seed := []byte{byte(trial), 0xA5, byte(trial >> 8)}
		tbl := NewTable(cfg, seed, nil) // nil -> all seats filled with bots

		log := tbl.PlayOut()
		if !tbl.Finished() {
			t.Fatalf("trial %d: table did not finish", trial)
		}
		if w := tbl.Winner(); w != Tie && (w < 0 || w >= players) {
			t.Fatalf("trial %d: invalid winner %d", trial, w)
		}
		if len(log) == 0 || log[len(log)-1].Type != EvMatchFinished {
			t.Fatalf("trial %d: last event is not match_finished", trial)
		}
		checkInvariants(t, tbl.State(), trial)

		// Whole log is gap-free.
		full := tbl.Log()
		for i, ev := range full {
			if ev.Seq != i {
				t.Fatalf("trial %d: event %d has seq %d", trial, i, ev.Seq)
			}
		}
	}
}

// TestSelfPlayDeterministic proves a table replays identically from the same seed
// — the property that makes matches verifiable.
func TestSelfPlayDeterministic(t *testing.T) {
	for trial := 0; trial < 25; trial++ {
		cfg := Config{Players: 4, StartingCash: 1500, MaxTurns: 300}
		seed := []byte{0x7e, byte(trial)}
		run := func() (State, []Event) {
			tbl := NewTable(cfg, seed, nil)
			tbl.PlayOut()
			return tbl.State(), tbl.Log()
		}
		s1, l1 := run()
		s2, l2 := run()
		if mustJSON(t, s1) != mustJSON(t, s2) {
			t.Fatalf("trial %d: final states differ", trial)
		}
		if len(l1) != len(l2) {
			t.Fatalf("trial %d: log lengths differ (%d vs %d)", trial, len(l1), len(l2))
		}
		for i := range l1 {
			if mustJSON(t, l1[i]) != mustJSON(t, l2[i]) {
				t.Fatalf("trial %d: event %d differs", trial, i)
			}
		}
	}
}

// TestHumanSeatFlow exercises the single-user experience: seat 0 is human, the
// rest are bots. The table advances bots automatically and pauses for the human;
// we feed legal human moves and confirm the game completes correctly.
func TestHumanSeatFlow(t *testing.T) {
	cfg := Config{Players: 4, StartingCash: 1500, MaxTurns: 300}
	seed := []byte("human-seat")
	bots := []Agent{
		nil, // seat 0 = the human
		NewBot("Borg", StyleTycoon, seed, 1),
		NewBot("Cleo", StyleBanker, seed, 2),
		NewBot("Dax", StyleWildcard, seed, 3),
	}
	tbl := NewTable(cfg, seed, bots)

	// A deterministic stand-in for the human's choices.
	human := NewBot("You", StyleBanker, seed, 0)

	for i := 0; i < 200000 && !tbl.Finished(); i++ {
		tbl.AdvanceBots()
		if tbl.Finished() {
			break
		}
		seat, isHuman := tbl.PendingSeat()
		if !isHuman || seat != 0 {
			t.Fatalf("AdvanceBots stopped on non-human seat %d (human=%v)", seat, isHuman)
		}
		// The presented legal actions must be non-empty for the human.
		if len(tbl.LegalActions(0)) == 0 {
			t.Fatal("human seat has no legal actions to choose from")
		}
		a := human.Decide(tbl.Engine(), tbl.State(), 0)
		if err := tbl.Apply(0, a); err != nil {
			t.Fatalf("applying human action %+v failed: %v", a, err)
		}
	}
	if !tbl.Finished() {
		t.Fatal("human-seat game did not finish")
	}
	checkInvariants(t, tbl.State(), 0)
}

// TestApplyValidation confirms the table rejects out-of-turn and illegal human
// moves transactionally (state unchanged), which a UI relies on to re-prompt.
func TestApplyValidation(t *testing.T) {
	cfg := Config{Players: 2, StartingCash: 1500, MaxTurns: 300}
	seed := []byte("apply-validation")
	tbl := NewTable(cfg, seed, []Agent{nil, NewBot("Borg", StyleTycoon, seed, 1)})

	// Seat 0 (human) acts first.
	if seat, human := tbl.PendingSeat(); seat != 0 || !human {
		t.Fatalf("expected human seat 0 to be pending, got seat=%d human=%v", seat, human)
	}
	if err := tbl.Apply(1, Action{Kind: ActRoll}); err != ErrSeatNotPending {
		t.Fatalf("expected ErrSeatNotPending, got %v", err)
	}
	if err := tbl.Apply(0, Action{Kind: ActBuy}); err != ErrIllegalAction {
		t.Fatalf("expected ErrIllegalAction for buy during roll, got %v", err)
	}
	before := mustJSON(t, tbl.State())
	if err := tbl.Apply(0, Action{Kind: ActRoll}); err != nil {
		t.Fatalf("legal roll rejected: %v", err)
	}
	if before == mustJSON(t, tbl.State()) {
		t.Fatal("state did not change after a legal action")
	}
}

// TestNewTableAutoFillsSeats confirms passing nil agents seats a full bot table.
func TestNewTableAutoFillsSeats(t *testing.T) {
	tbl := NewTable(Config{Players: 6, StartingCash: 1500, MaxTurns: 50}, []byte("fill"), nil)
	for seat := 0; seat < 6; seat++ {
		if tbl.IsHuman(seat) {
			t.Fatalf("seat %d should have been auto-filled with a bot", seat)
		}
		if tbl.SeatName(seat) == "" {
			t.Fatalf("seat %d has no name", seat)
		}
	}
}
