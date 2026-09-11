package match

import (
	"encoding/json"
	"testing"
)

func TestRosterMarksHouseSeats(t *testing.T) {
	seats := RosterOf([]Player{
		{Seat: 0, AgentPublicID: "ag_dev", Name: "cascade", OwnerName: "nahom", AvatarURL: "https://x/a.png"},
		{Seat: 1, AgentPublicID: "ag_house_master", Name: "Vale", OwnerName: "system", IsHouse: true, AvatarURL: "https://x/house.png"},
	})
	if len(seats) != 2 {
		t.Fatalf("len = %d", len(seats))
	}
	if seats[0].House || seats[0].Bot {
		t.Fatalf("developer seat must not be house: %+v", seats[0])
	}
	if !seats[1].House || !seats[1].Bot {
		t.Fatalf("house seat must set house+bot: %+v", seats[1])
	}

	// In-memory sandbox players may not have IsHouse hydrated; the public id
	// convention still has to mark them so the table never paints initials.
	inferred := RosterOf([]Player{
		{Seat: 1, AgentPublicID: "ag_house_challenger", Name: "Kite"},
	})
	if !inferred[0].House {
		t.Fatalf("ag_house_* must infer house without IsHouse: %+v", inferred[0])
	}

	blob, err := json.Marshal(seats)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(blob) {
		t.Fatalf("invalid roster json: %s", blob)
	}
}
