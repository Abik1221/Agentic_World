package mafia

import (
	"encoding/json"
	"testing"
)

func playFull(seed []byte, maxDays int) (State, []Event) {
	tbl := NewTable(StandardSeats(), seed, nil, maxDays) // nil -> all bots
	tbl.PlayOut()
	return tbl.State(), tbl.Log()
}

// TestTablePlaysFullGame proves the dedicated environment plays complete Mafia
// matches to a valid team victory across many seeds.
func TestTablePlaysFullGame(t *testing.T) {
	for trial := 0; trial < 120; trial++ {
		sd := []byte{byte(trial), 0x33, byte(trial >> 8)}
		s, log := playFull(sd, 60)

		if !s.Finished {
			t.Fatalf("trial %d: game did not finish", trial)
		}
		if s.Winner != TeamTown && s.Winner != TeamMafia {
			t.Fatalf("trial %d: invalid winner %q", trial, s.Winner)
		}
		if !hasVictory(log, s.Winner) {
			t.Fatalf("trial %d: no matching victory event", trial)
		}
		checkInvariants(t, s, log, trial)
	}
}

func checkInvariants(t *testing.T, s State, log []Event, trial int) {
	t.Helper()

	// Roles never change: composition is preserved for the whole match.
	counts := map[string]int{}
	for _, r := range s.Roles {
		counts[r]++
	}
	if counts[RoleMafia] != 3 || counts[RoleDetective] != 1 || counts[RoleDoctor] != 1 ||
		counts[RoleSheriff] != 1 || counts[RoleVillager] != 6 {
		t.Fatalf("trial %d: role composition changed: %v", trial, counts)
	}

	// Winner is consistent with the surviving balance of power.
	mafiaAlive, townAlive := s.countTeam(TeamMafia), s.countTeam(TeamTown)
	switch s.Winner {
	case TeamTown:
		if mafiaAlive >= townAlive {
			t.Fatalf("trial %d: town won but mafia %d >= town %d", trial, mafiaAlive, townAlive)
		}
	case TeamMafia:
		if mafiaAlive < townAlive {
			t.Fatalf("trial %d: mafia won but mafia %d < town %d", trial, mafiaAlive, townAlive)
		}
	}

	// Event log is gap-free.
	for i, ev := range log {
		if ev.Seq != i {
			t.Fatalf("trial %d: event %d has seq %d (log not gap-free)", trial, i, ev.Seq)
		}
	}
}

// TestSelfPlayDeterministic proves a table replays byte-for-byte from its seed.
func TestSelfPlayDeterministic(t *testing.T) {
	for trial := 0; trial < 30; trial++ {
		sd := []byte{0x09, byte(trial)}
		s1, l1 := playFull(sd, 60)
		s2, l2 := playFull(sd, 60)
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

// TestForceTimeoutDeterministic drives whole games purely through the engine's
// timeout filler and confirms reproducibility.
func TestForceTimeoutDeterministic(t *testing.T) {
	run := func() (State, []Event) {
		e := NewWithMaxDays(60)
		s, evs := e.Init([]byte("timeout-seed"), StandardSeats())
		for i := 0; i < 100000 && !s.Finished; i++ {
			ns, ev, err := e.ForceTimeout(s, []byte("timeout-seed"))
			if err != nil {
				t.Fatalf("force timeout: %v", err)
			}
			// ForceTimeout fills the current phase; if it made no progress, advance is impossible.
			if len(ev) == 0 && !ns.Finished {
				t.Fatalf("force timeout made no progress at day %d phase %s", s.Day, s.Phase)
			}
			s, evs = ns, append(evs, ev...)
		}
		if !s.Finished {
			t.Fatal("timeout-driven game did not finish")
		}
		return s, evs
	}
	s1, e1 := run()
	s2, e2 := run()
	if mustJSON(t, s1) != mustJSON(t, s2) || len(e1) != len(e2) {
		t.Fatal("ForceTimeout is not deterministic")
	}
}

// TestReplayReproducesMatch proves a recorded match replays to the identical state
// and event log, that the replay hash matches, and that tampering is detected.
func TestReplayReproducesMatch(t *testing.T) {
	const maxDays = 60
	for trial := 0; trial < 25; trial++ {
		sd := []byte{byte(trial), 0x4d, byte(trial >> 8)}
		tbl := NewTable(StandardSeats(), sd, nil, maxDays)
		tbl.PlayOut()

		s2, log2, err := Replay(StandardSeats(), sd, maxDays, tbl.Moves())
		if err != nil {
			t.Fatalf("trial %d: replay error: %v", trial, err)
		}
		if mustJSON(t, tbl.State()) != mustJSON(t, s2) {
			t.Fatalf("trial %d: replayed final state differs", trial)
		}
		if len(tbl.Log()) != len(log2) || tbl.ReplayHash() != ReplayHash(log2) {
			t.Fatalf("trial %d: replayed log/hash differs", trial)
		}
		if ok, err := Verify(StandardSeats(), sd, maxDays, tbl.Moves(), tbl.Log()); err != nil || !ok {
			t.Fatalf("trial %d: Verify failed (ok=%v err=%v)", trial, ok, err)
		}
		bad := append([]Event(nil), tbl.Log()...)
		if len(bad) > 5 {
			bad[5].Type = "tampered"
			if ok, _ := Verify(StandardSeats(), sd, maxDays, tbl.Moves(), bad); ok {
				t.Fatalf("trial %d: Verify accepted a tampered log", trial)
			}
		}
	}
}

func hasVictory(log []Event, team string) bool {
	for _, ev := range log {
		if ev.Type == EvVictory {
			if vp, ok := ev.Payload.(VictoryPayload); ok && vp.Team == team {
				return true
			}
		}
	}
	return false
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
