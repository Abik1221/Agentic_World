package mafia

import "testing"

// TestAssignRolesComposition proves the configured role pool is dealt exactly
// once each, for many independent seeds (fairness of the "paper cards").
func TestAssignRolesComposition(t *testing.T) {
	seats := StandardSeats()
	for i := 0; i < 500; i++ {
		seed := []byte{byte(i), byte(i >> 8), 0xC0}
		roles := assignRoles(seed, seats)
		if len(roles) != RosterSize {
			t.Fatalf("seed %d: assigned %d roles, want %d", i, len(roles), RosterSize)
		}
		counts := map[string]int{}
		for _, r := range roles {
			counts[r]++
		}
		if counts[RoleMafia] != 3 || counts[RoleDetective] != 1 || counts[RoleDoctor] != 1 ||
			counts[RoleSheriff] != 1 || counts[RoleVillager] != 6 {
			t.Fatalf("seed %d: bad composition %v", i, counts)
		}
	}
}

// TestAssignRolesDeterministic proves a seed reproduces the exact assignment
// (required for replay/audit) while different seeds differ (unpredictable).
func TestAssignRolesDeterministic(t *testing.T) {
	seats := StandardSeats()
	a := assignRoles([]byte("same-seed"), seats)
	b := assignRoles([]byte("same-seed"), seats)
	for _, seat := range seats {
		if a[seat] != b[seat] {
			t.Fatalf("seat %d: non-deterministic (%s vs %s)", seat, a[seat], b[seat])
		}
	}
	c := assignRoles([]byte("different-seed"), seats)
	same := true
	for _, seat := range seats {
		if a[seat] != c[seat] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("two different seeds produced identical assignments")
	}
}

// TestAssignRolesUniform checks the shuffle is unbiased: across many seeds each
// seat holds each role about as often as any other seat. A biased shuffle (the
// old single-hash/modulo version) fails this.
func TestAssignRolesUniform(t *testing.T) {
	seats := StandardSeats()
	const trials = 60000
	mafiaAt := map[int]int{}
	for i := 0; i < trials; i++ {
		seed := []byte{byte(i), byte(i >> 8), byte(i >> 16), 0x5A}
		for seat, r := range assignRoles(seed, seats) {
			if r == RoleMafia {
				mafiaAt[seat]++
			}
		}
	}
	// P(seat is Mafia) = 3/12 = 0.25 for a fair shuffle.
	exp := float64(trials) * 3.0 / float64(RosterSize)
	for _, seat := range seats {
		got := float64(mafiaAt[seat])
		if got < exp*0.93 || got > exp*1.07 {
			t.Fatalf("seat %d Mafia frequency %.0f deviates from expected ~%.0f (non-uniform)", seat, got, exp)
		}
	}
}
