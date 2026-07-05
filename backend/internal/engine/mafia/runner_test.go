package mafia

import "testing"

// TestHumanSeatFlow exercises the single-user experience: seat 1 is human, the
// rest are bots. The table auto-plays the bots and pauses only for the human;
// we feed legal human actions and confirm the match completes.
func TestHumanSeatFlow(t *testing.T) {
	sd := []byte("human-seat")
	agents := map[int]Agent{}
	for _, seat := range StandardSeats() {
		if seat != 1 {
			agents[seat] = NewBot(defaultName(seat), sd, seat)
		}
	}
	tbl := NewTable(StandardSeats(), sd, agents, 60)
	human := NewBot("You", sd, 1) // deterministic stand-in for the person

	for i := 0; i < 100000 && !tbl.Finished(); i++ {
		tbl.AdvanceBots()
		if tbl.Finished() {
			break
		}
		pend := tbl.PendingActors()
		if len(pend) == 0 {
			break
		}
		for _, seat := range pend {
			if seat != 1 {
				t.Fatalf("AdvanceBots stopped with non-human seat %d pending", seat)
			}
		}
		if len(tbl.LegalActions(1)) == 0 {
			t.Fatal("human seat is pending but has no legal actions")
		}
		if err := tbl.Apply(1, human.Decide(tbl.ViewFor(1))); err != nil {
			t.Fatalf("applying human action failed: %v", err)
		}
	}
	if !tbl.Finished() {
		t.Fatal("human-seat game did not finish")
	}
	if tbl.Winner() != TeamTown && tbl.Winner() != TeamMafia {
		t.Fatalf("invalid winner %q", tbl.Winner())
	}
}

// TestApplyValidation confirms out-of-turn and illegal submissions are rejected
// transactionally, which a UI relies on to re-prompt.
func TestApplyValidation(t *testing.T) {
	sd := []byte("apply-val")
	tbl := NewTable(StandardSeats(), sd, nil, 60)
	s := tbl.State()

	var villager, mafia int
	for seat, r := range s.Roles {
		if r == RoleVillager && villager == 0 {
			villager = seat
		}
		if r == RoleMafia && mafia == 0 {
			mafia = seat
		}
	}

	// A villager is not pending at night.
	if err := tbl.Apply(villager, Action{Kind: ActVote, Target: mafia}); err != ErrNotPending {
		t.Fatalf("expected ErrNotPending for a villager at night, got %v", err)
	}
	// The mafia is pending, but a vote is illegal at night.
	if err := tbl.Apply(mafia, Action{Kind: ActVote, Target: villager}); err == nil {
		t.Fatal("expected an error voting during the night")
	}
	// A proper night kill is accepted.
	if err := tbl.Apply(mafia, Action{Kind: ActNightKill, Target: villager}); err != nil {
		t.Fatalf("legal night kill rejected: %v", err)
	}
}

// TestPrivateNightInfoStaysPrivate confirms redaction: a seat only ever sees its
// own night results, never another player's.
func TestPrivateNightInfoStaysPrivate(t *testing.T) {
	sd := []byte("redact")
	tbl := NewTable(StandardSeats(), sd, nil, 60)
	tbl.PlayOut()
	s, log := tbl.State(), tbl.Log()

	var det, vil int
	for seat, r := range s.Roles {
		if r == RoleDetective {
			det = seat
		}
		if r == RoleVillager && vil == 0 {
			vil = seat
		}
	}

	dv := BuildView(s, det, log)
	for _, ev := range dv.Private {
		np, ok := ev.Payload.(NightPayload)
		if !ok || np.Seat != det {
			t.Fatalf("detective's private feed leaked another seat: %+v", ev.Payload)
		}
	}
	vv := BuildView(s, vil, log)
	if len(vv.Private) != 0 {
		t.Fatalf("villager has %d private night events, want 0", len(vv.Private))
	}
	if len(vv.Public) == 0 {
		t.Fatal("villager should see the public transcript")
	}
}

func TestNewTableAllBots(t *testing.T) {
	tbl := NewTable(StandardSeats(), []byte("fill"), nil, 30)
	for _, seat := range tbl.Seats() {
		if tbl.IsHuman(seat) {
			t.Fatalf("seat %d should have been a bot", seat)
		}
	}
}
