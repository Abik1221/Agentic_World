package devplatform

import (
	"testing"
)

// TestRegistryHasThreeGames guards that the platform ships exactly the three
// supported games, each wired to a real engine runner.
func TestRegistryHasThreeGames(t *testing.T) {
	reg := DefaultRegistry()
	got := reg.IDs()
	want := map[GameID]bool{GameGoofspiel: true, GameMafia: true, GameMonopoly: true}
	if len(got) != len(want) {
		t.Fatalf("registry has %d games, want %d (%v)", len(got), len(want), got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected game %q", id)
		}
		g, ok := reg.Get(id)
		if !ok || g.run == nil {
			t.Errorf("game %q missing runner", id)
		}
	}
}

// TestCertifyAllGames runs the full 3-match pipeline against every game using
// the real engines, and asserts each one certifies with all checks green.
func TestCertifyAllGames(t *testing.T) {
	reg := DefaultRegistry()
	cert := NewCertifier(reg)

	for _, spec := range reg.All() {
		spec := spec
		t.Run(string(spec.ID), func(t *testing.T) {
			rep, err := cert.Certify(spec.ID, "test-agent-v1")
			if err != nil {
				t.Fatalf("certify %s: %v", spec.ID, err)
			}
			if len(rep.Matches) != 3 {
				t.Fatalf("%s: expected 3 matches, got %d", spec.ID, len(rep.Matches))
			}
			if !rep.Certified {
				t.Errorf("%s: expected certified, got failure:\n%s", spec.ID, rep.String())
			}
			for _, m := range rep.Matches {
				if !m.Outcome.Completed {
					t.Errorf("%s match %d: engine did not complete a match", spec.ID, m.Index)
				}
				for _, c := range m.Checks {
					if !c.Passed {
						t.Errorf("%s match %d: check %q failed: %s", spec.ID, m.Index, c.Name, c.Detail)
					}
				}
			}
		})
	}
}

// TestSandboxDeterminism proves every game is reproducible: the same seed yields
// an identical replay hash, winner, and move count. This underpins replay
// verification and tournament version-lock.
func TestSandboxDeterminism(t *testing.T) {
	reg := DefaultRegistry()
	for _, spec := range reg.All() {
		spec := spec
		t.Run(string(spec.ID), func(t *testing.T) {
			seed := []byte("determinism-seed")
			a, err := spec.Run(spec.CertSeats(), seed)
			if err != nil {
				t.Fatalf("run A: %v", err)
			}
			b, err := spec.Run(spec.CertSeats(), seed)
			if err != nil {
				t.Fatalf("run B: %v", err)
			}
			if a.ReplayHash == "" {
				t.Fatalf("%s: empty replay hash", spec.ID)
			}
			if a.ReplayHash != b.ReplayHash {
				t.Errorf("%s: replay hash mismatch: %s != %s", spec.ID, a.ReplayHash, b.ReplayHash)
			}
			if a.Winner != b.Winner || a.Moves != b.Moves {
				t.Errorf("%s: non-deterministic outcome: (%s,%d) vs (%s,%d)", spec.ID, a.Winner, a.Moves, b.Winner, b.Moves)
			}
		})
	}
}

// TestDifferentSeedsDiverge sanity-checks that seeds actually matter (the sandbox
// is not returning a constant), so certification isn't trivially satisfiable.
func TestDifferentSeedsDiverge(t *testing.T) {
	reg := DefaultRegistry()
	spec, _ := reg.Get(GameMonopoly)
	h := map[string]bool{}
	for _, s := range []string{"a", "b", "c", "d"} {
		out, err := spec.Run(4, []byte(s))
		if err != nil {
			t.Fatal(err)
		}
		h[out.ReplayHash] = true
	}
	if len(h) < 2 {
		t.Errorf("expected distinct replays across seeds, got %d unique", len(h))
	}
}
