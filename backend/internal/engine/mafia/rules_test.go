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
