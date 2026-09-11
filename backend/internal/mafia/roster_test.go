package mafia

import (
	"encoding/json"
	"strings"
	"testing"
)

// The roster is served to ANY spectator mid-match, so it must carry display
// identity and nothing else. A leaked role would hand the whole game away.
func TestRosterNeverLeaksRoles(t *testing.T) {
	players := []Player{
		{Seat: 2, AgentPublicID: "ag_b", Name: "nightowl", OwnerName: "kasparov", Role: "mafia", Team: "mafia", Alive: true},
		{Seat: 1, AgentPublicID: "ag_a", Name: "cascade", OwnerName: "nahom", Role: "detective", Team: "town", Alive: true},
	}
	seats := RosterOf(players, map[int]bool{1: true, 2: false})

	// Ordered by seat so a client can index it directly.
	if len(seats) != 2 || seats[0].Seat != 1 || seats[1].Seat != 2 {
		t.Fatalf("roster not ordered by seat: %+v", seats)
	}
	if seats[0].Name != "cascade" || seats[0].Owner != "nahom" {
		t.Fatalf("display identity missing: %+v", seats[0])
	}
	if seats[0].House || seats[1].House {
		t.Fatalf("ordinary seats must not be marked house: %+v", seats)
	}

	house := RosterOf([]Player{
		{Seat: 3, AgentPublicID: "ag_house_mafia_01", Name: "Vale", IsHouse: true, Alive: true},
	}, nil)
	if !house[0].House || !house[0].Bot {
		t.Fatalf("kind=house seat must set house+bot: %+v", house[0])
	}
	// Live alive-map wins over the stale per-player flag.
	if seats[1].Alive {
		t.Fatalf("seat 2 is dead in the alive map but roster says alive: %+v", seats[1])
	}

	blob, err := json.Marshal(seats)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"mafia", "detective", "role", "team"} {
		if strings.Contains(strings.ToLower(string(blob)), secret) {
			t.Fatalf("roster JSON leaks %q — hidden information must never be public: %s", secret, blob)
		}
	}
}
