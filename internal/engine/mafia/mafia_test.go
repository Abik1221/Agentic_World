package mafia

import "testing"

var seed = []byte("mafia-test-seed")

func TestInitComposition(t *testing.T) {
	e := New()
	s, evs := e.Init(seed, StandardSeats())
	if len(s.Roles) != RosterSize {
		t.Fatalf("roles = %d, want %d", len(s.Roles), RosterSize)
	}
	counts := map[string]int{}
	for _, r := range s.Roles {
		counts[r]++
	}
	if counts[RoleMafia] != 3 || counts[RoleDetective] != 1 || counts[RoleDoctor] != 1 ||
		counts[RoleSheriff] != 1 || counts[RoleVillager] != 6 {
		t.Fatalf("bad role composition: %v", counts)
	}
	for seat, alive := range s.Alive {
		if !alive {
			t.Fatalf("seat %d should start alive", seat)
		}
	}
	if s.Phase != PhaseNight || s.Day != 1 {
		t.Fatalf("should open on night 1, got %s day %d", s.Phase, s.Day)
	}
	if len(evs) == 0 {
		t.Fatal("init produced no events")
	}
}

func TestCommitVerify(t *testing.T) {
	if !VerifyCommit(seed, Commit(seed)) {
		t.Fatal("commit/verify mismatch")
	}
	if VerifyCommit([]byte("other"), Commit(seed)) {
		t.Fatal("commit verified against the wrong seed")
	}
}

func TestPendingAndLegalNight(t *testing.T) {
	e := New()
	s, _ := e.Init(seed, StandardSeats())

	pend := PendingActors(s)
	if len(pend) != 6 { // 3 mafia + detective + doctor + sheriff
		t.Fatalf("night pending = %d, want 6", len(pend))
	}
	for _, seat := range pend {
		if !RoleActsAtNight(s.Roles[seat]) {
			t.Fatalf("seat %d (%s) should not be pending at night", seat, s.Roles[seat])
		}
		if len(LegalActions(s, seat)) == 0 {
			t.Fatalf("pending seat %d has no legal action", seat)
		}
	}
	// A villager has no night action.
	for seat, role := range s.Roles {
		if role == RoleVillager {
			if LegalActions(s, seat) != nil {
				t.Fatalf("villager seat %d should have no night action", seat)
			}
			break
		}
	}
}

func TestViewRedaction(t *testing.T) {
	e := New()
	s, log := e.Init(seed, StandardSeats())

	var villager, mafia int
	for seat, r := range s.Roles {
		if r == RoleVillager && villager == 0 {
			villager = seat
		}
		if r == RoleMafia && mafia == 0 {
			mafia = seat
		}
	}

	vv := BuildView(s, villager, log)
	if vv.Role != RoleVillager || vv.Team != TeamTown {
		t.Fatalf("villager view wrong: role=%s team=%s", vv.Role, vv.Team)
	}
	if len(vv.Allies) != 0 {
		t.Fatal("a villager must not be shown any allies")
	}

	mv := BuildView(s, mafia, log)
	if mv.Team != TeamMafia {
		t.Fatal("mafia view should be on the mafia team")
	}
	if len(mv.Allies) != 2 { // 3 mafia total, minus self
		t.Fatalf("mafia should see 2 allies, got %d", len(mv.Allies))
	}
	for _, ally := range mv.Allies {
		if s.Roles[ally] != RoleMafia {
			t.Fatalf("ally seat %d is not actually mafia", ally)
		}
	}
}

func TestActIsPure(t *testing.T) {
	e := New()
	s, _ := e.Init(seed, StandardSeats())
	before := mustJSON(t, s)
	seat := PendingActors(s)[0]
	if _, _, err := e.Act(s, seat, defaultActionFor(s, seat, seed)); err != nil {
		t.Fatalf("act: %v", err)
	}
	if mustJSON(t, s) != before {
		t.Fatal("Act mutated the caller's state; it must operate on a clone")
	}
}

func TestDeadSeatCannotAct(t *testing.T) {
	e := New()
	s, _ := e.Init(seed, StandardSeats())
	// Kill a seat directly and confirm the engine rejects its actions.
	var victim int
	for seat := range s.Roles {
		victim = seat
		break
	}
	s.Alive[victim] = false
	if _, _, err := e.Act(s, victim, Action{Kind: ActMessage, Text: "hi"}); err != ErrNotAlive {
		t.Fatalf("expected ErrNotAlive for a dead seat, got %v", err)
	}
}
