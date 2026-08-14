package mafia

import "testing"

// nightState builds a minimal night-1 state with the given roles (all alive) and a
// single Mafia kill vote (killer → victim). No detective/doctor/sheriff seats, so
// resolveNight only exercises the kill path.
func nightState(roles map[int]string, killer, victim, day int) State {
	s := State{
		Day: day, Phase: PhaseNight,
		Alive: map[int]bool{}, Roles: roles,
		MafiaKill: map[int]int{killer: victim},
		NightActs: map[int]Action{},
	}
	for seat := range roles {
		s.Alive[seat] = true
	}
	return s
}

func findElim(evs []Event) (EliminatePayload, bool) {
	for _, e := range evs {
		if e.Type == EvEliminate {
			if p, ok := e.Payload.(EliminatePayload); ok {
				return p, true
			}
		}
	}
	return EliminatePayload{}, false
}

func TestRevealRoleOnDeath(t *testing.T) {
	roles := map[int]string{1: RoleMafia, 2: RoleVillager}

	// Default: role is hidden in the public eliminate event.
	_, evs, err := New().resolveNight(nightState(roles, 1, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := findElim(evs)
	if !ok || p.Target != 2 || p.Cause != "mafia" {
		t.Fatalf("expected seat 2 killed by mafia, got %+v (ok=%v)", p, ok)
	}
	if p.Role != "" {
		t.Fatalf("role must stay hidden by default, leaked %q", p.Role)
	}

	// Reveal-on-death: the eliminate event carries the dead seat's role.
	e := New()
	e.RevealRoleOnDeath = true
	_, evs2, _ := e.resolveNight(nightState(roles, 1, 2, 2))
	p2, _ := findElim(evs2)
	if p2.Role != RoleVillager {
		t.Fatalf("reveal-on-death should expose the role, got %q", p2.Role)
	}
}

func TestNoFirstNightKill(t *testing.T) {
	roles := map[int]string{1: RoleMafia, 2: RoleVillager}

	e := New()
	e.NoFirstNightKill = true

	// Night 1: the kill is suppressed — nobody is eliminated.
	s1, evs1, _ := e.resolveNight(nightState(roles, 1, 2, 1))
	if _, ok := findElim(evs1); ok {
		t.Fatal("no-first-night-kill: nobody should be eliminated on night 1")
	}
	if !s1.Alive[2] {
		t.Fatal("victim should survive night 1")
	}

	// Night 2+: the rule no longer applies — the kill lands.
	_, evs2, _ := e.resolveNight(nightState(roles, 1, 2, 2))
	if p, ok := findElim(evs2); !ok || p.Target != 2 {
		t.Fatalf("kill should land on night 2, got %+v (ok=%v)", p, ok)
	}
}

// ── the doctor may not shield the same seat twice running ────────────────────
//
// Standard Mafia: "a doctor cannot heal the same person (including himself) two nights in a
// row; after skipping one night he can heal them again." The engine enforced only "the target
// is alive", so the optimal doctor shielded ITSELF every night and could never be killed at
// night — or pinned one player permanently. Either way the role stopped being a decision,
// which is the whole tension of it: who goes unguarded tonight.

// doctorNight builds a night state with a doctor at seat 1 and living seats 1..n.
func doctorNight(n int, lastProtect map[int]int) State {
	s := State{
		Day: 2, Phase: PhaseNight,
		Alive: map[int]bool{}, Roles: map[int]string{},
		NightActs: map[int]Action{}, MafiaKill: map[int]int{},
		LastProtect: lastProtect,
	}
	for seat := 1; seat <= n; seat++ {
		s.Alive[seat] = true
		s.Roles[seat] = RoleVillager
	}
	s.Roles[1] = RoleDoctor
	s.Roles[n] = RoleMafia
	return s
}

func TestADoctorCannotShieldTheSameSeatTwoNightsRunning(t *testing.T) {
	e := New()
	// Shielded seat 3 last night: tonight seat 3 is barred, everyone else is fine.
	s := doctorNight(5, map[int]int{1: 3})

	if _, _, err := e.actNight(s, 1, Action{Kind: "protect", Target: 3}); err != ErrIllegalAction {
		t.Fatalf("re-shielding last night's target = %v, want ErrIllegalAction", err)
	}
	if _, _, err := e.actNight(s, 1, Action{Kind: "protect", Target: 4}); err != nil {
		t.Fatalf("shielding a different seat was refused: %v", err)
	}
}

func TestADoctorCannotCampOnItself(t *testing.T) {
	// The degenerate line the rule exists to stop: shield yourself every night and the mafia
	// can never reach you.
	e := New()
	s := doctorNight(5, map[int]int{1: 1})
	if _, _, err := e.actNight(s, 1, Action{Kind: "protect", Target: 1}); err != ErrIllegalAction {
		t.Fatalf("a doctor shielded itself twice running (%v); that is the exact degenerate "+
			"strategy the rule removes", err)
	}
}

func TestTheBarIsOnlyForONENight(t *testing.T) {
	// "After skipping one night he can heal the same player again." A permanent ban would be a
	// different, harsher game than the one the rules describe.
	e := New()
	s := doctorNight(5, map[int]int{1: 3})

	// Night A: shield someone else, which resolution records as the new last-protect.
	s2, _, err := e.actNight(s, 1, Action{Kind: "protect", Target: 4})
	if err != nil {
		t.Fatal(err)
	}
	s3, _, err := e.resolveNight(s2)
	if err != nil {
		t.Fatal(err)
	}
	if s3.LastProtect[1] != 4 {
		t.Fatalf("last protect = %d, want the seat just shielded", s3.LastProtect[1])
	}
	// Night B: seat 3 is available again.
	s3.Phase, s3.NightActs = PhaseNight, map[int]Action{}
	if _, _, err := e.actNight(s3, 1, Action{Kind: "protect", Target: 3}); err != nil {
		t.Fatalf("seat 3 still barred after a night off: %v", err)
	}
}

func TestTheDoctorIsToldWhichSeatIsBarred(t *testing.T) {
	// Published rather than left to be discovered by rejection: an agent that learns the rule
	// by having a move refused wastes a decision and a model call on something the engine
	// already knows.
	s := doctorNight(5, map[int]int{1: 3})
	v := BuildView(s, 1, nil)
	if v.CannotProtect != 3 {
		t.Fatalf("doctor view says cannot_protect=%d, want 3", v.CannotProtect)
	}
	// A non-doctor's view must not carry it — nobody else's constraint is their business, and
	// a villager seeing it would learn a doctor exists and what it did.
	if BuildView(s, 2, nil).CannotProtect != 0 {
		t.Error("a non-doctor seat was told the doctor's constraint")
	}
	// First night: nothing barred.
	if BuildView(doctorNight(5, nil), 1, nil).CannotProtect != -1 {
		t.Error("the first night should bar nothing")
	}
}

// ── the mafia must be able to converge on one target ─────────────────────────
//
// In the real game the mafia wake TOGETHER and point at their choice, seeing each other; that
// mutual sight is how they agree. Here each seat saw only its OWN night event, so they chose
// blind — and because a tied kill vote means NOBODY DIES, three mafia splitting 1-1-1 silently
// vetoed their own night. The team could not reliably perform the one action it exists for.

func mafiaNight(kills map[int]int) State {
	s := State{
		Day: 2, Phase: PhaseNight,
		Alive: map[int]bool{}, Roles: map[int]string{},
		NightActs: map[int]Action{}, MafiaKill: kills,
	}
	for seat := 1; seat <= 6; seat++ {
		s.Alive[seat] = true
		s.Roles[seat] = RoleVillager
	}
	s.Roles[1], s.Roles[2], s.Roles[3] = RoleMafia, RoleMafia, RoleMafia
	return s
}

func TestAMafiaSeesWhatItsAlliesHavePicked(t *testing.T) {
	// Seat 1 has ALREADY picked too. That matters: without its own entry in the fixture the
	// "own pick is not echoed" assertion below cannot fail, and a mutation replacing the ally
	// filter with every seat survived it. A test that cannot fail is not a test.
	v := BuildView(mafiaNight(map[int]int{1: 4, 2: 5, 3: 6}), 1, nil)

	if len(v.AllyKills) != 2 || v.AllyKills[2] != 5 || v.AllyKills[3] != 6 {
		t.Fatalf("ally kills = %v, want seat 2→5 and seat 3→6 so the team can converge", v.AllyKills)
	}
	// Its OWN pick is not duplicated here — that is already in Private.
	if _, self := v.AllyKills[1]; self {
		t.Error("a mafia's own pick was echoed back as an ally's")
	}
}

func TestAllyKillsNeverLeakToTheTown(t *testing.T) {
	// This is the field's whole risk: it names mafia seats AND their intent. A villager who
	// could read it would win the game on the spot.
	s := mafiaNight(map[int]int{2: 5, 3: 6})
	for _, seat := range []int{4, 5, 6} {
		if v := BuildView(s, seat, nil); v.AllyKills != nil {
			t.Fatalf("seat %d (town) can see the mafia's kill selections: %v", seat, v.AllyKills)
		}
	}
}

func TestAllyKillsAreNightOnly(t *testing.T) {
	// Outside the night there is no pending kill to coordinate, and carrying yesterday's
	// selections into the day would hand a mafia stale information it never had at the table.
	s := mafiaNight(map[int]int{2: 5})
	s.Phase = PhaseDiscussion
	if v := BuildView(s, 1, nil); v.AllyKills != nil {
		t.Fatalf("ally kills present during %s: %v", s.Phase, v.AllyKills)
	}
}

func TestAnAbstainingAllyIsNotShownAsATarget(t *testing.T) {
	// An abstaining mafia records target 0. Reporting that as a choice would tell the others
	// their ally picked seat 0, which is not a seat.
	v := BuildView(mafiaNight(map[int]int{2: 0, 3: 6}), 1, nil)
	if _, ok := v.AllyKills[2]; ok {
		t.Errorf("an abstaining ally was reported as having picked a target: %v", v.AllyKills)
	}
	if v.AllyKills[3] != 6 {
		t.Errorf("the ally that DID pick was lost: %v", v.AllyKills)
	}
}

// TestATiedMafiaVoteKillsNobody documents the consequence that makes ally visibility matter:
// a split is a wasted night, so the team has a real reason to converge.
func TestATiedMafiaVoteKillsNobody(t *testing.T) {
	s := mafiaNight(map[int]int{1: 4, 2: 5, 3: 6}) // 1-1-1
	ns, evs, err := New().resolveNight(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, eliminated := findElim(evs); eliminated {
		t.Fatal("a 1-1-1 mafia split killed someone; a tie must kill nobody")
	}
	for seat := 4; seat <= 6; seat++ {
		if !ns.Alive[seat] {
			t.Fatalf("seat %d died on a tied kill vote", seat)
		}
	}
}
