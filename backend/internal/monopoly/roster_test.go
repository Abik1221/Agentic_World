package monopoly

import "testing"

// Bot seats are not rows in match_players, so a naive projection of the agent list
// returns one seat for a four-player push-play table and the board renders three
// unnamed ghosts. Every seat must be accounted for and labelled honestly.
func TestRosterFillsBotSeats(t *testing.T) {
	agents := []Player{{Seat: 0, AgentPublicID: "ag_dev", Name: "cascade", OwnerName: "nahom"}}
	seats := RosterOf(agents, 4)

	if len(seats) != 4 {
		t.Fatalf("roster has %d seats, want 4 — bot seats were dropped", len(seats))
	}
	if seats[0].Bot || seats[0].Name != "cascade" || seats[0].AgentID != "ag_dev" {
		t.Fatalf("real seat mislabelled: %+v", seats[0])
	}
	for _, s := range seats[1:] {
		if !s.Bot {
			t.Fatalf("seat %d should be a bot: %+v", s.Seat, s)
		}
		if s.Name == "" {
			t.Fatalf("seat %d has no label — the board would show a ghost", s.Seat)
		}
		if s.AgentID != "" {
			t.Fatalf("bot seat %d must not claim an agent id: %+v", s.Seat, s)
		}
	}
	for i, s := range seats {
		if s.Seat != i {
			t.Fatalf("seats out of order at %d: %+v", i, seats)
		}
	}
}

// An agent with no display name must not be rendered as a blank seat.
func TestRosterLabelsUnnamedAgent(t *testing.T) {
	seats := RosterOf([]Player{{Seat: 0, AgentPublicID: "ag_x"}}, 1)
	if seats[0].Name != "Unnamed agent" {
		t.Fatalf("unnamed agent rendered as %q", seats[0].Name)
	}
}

// A disagreeing seat count must never silently drop a real player.
func TestRosterNeverDropsRealSeats(t *testing.T) {
	agents := []Player{{Seat: 0, Name: "a"}, {Seat: 1, Name: "b"}, {Seat: 2, Name: "c"}}
	if got := len(RosterOf(agents, 1)); got != 3 {
		t.Fatalf("roster dropped real seats: got %d, want 3", got)
	}
}
