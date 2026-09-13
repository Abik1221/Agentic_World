package mafia

import (
	"context"
	"strconv"
	"testing"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
)

// Private invite rooms: host opens an unlisted table; invited humans join until
// the 12-seat roster is full. No house-bot fill — real-money path only.

func TestCreateRoomMarksPrivateAndIsNotListed(t *testing.T) {
	repo := &recordingCreateRepo{}
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, nil)

	id, err := svc.CreateRoom(context.Background(), "ag_host", "usr_host", 500)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if id == "" {
		t.Fatal("CreateRoom returned empty id")
	}
	if !repo.last.Private {
		t.Fatal("CreateRoom must set private so the open lobby cannot claim the seat")
	}
	if repo.last.EntryFee != 500 {
		t.Fatalf("entry fee = %d, want 500", repo.last.EntryFee)
	}
}

func TestCreateRoomDoesNotNeedHouseBots(t *testing.T) {
	repo := &recordingCreateRepo{}
	svc := NewService(repo, fakeLock{}, nil, &recordingWallet{}, fakeBcast{}, nil, nil,
		fakeClock{}, Config{})
	// No EnablePushPlay → no bots. Invite rooms must still open.
	if _, err := svc.CreateRoom(context.Background(), "ag_host", "usr_host", 500); err != nil {
		t.Fatalf("CreateRoom without house bots: %v", err)
	}
}

func TestCreateRoomRefusesZeroStake(t *testing.T) {
	svc := newSeatingSvc(&recordingCreateRepo{}, &recordingWallet{}, nil, nil, nil)
	if _, err := svc.CreateRoom(context.Background(), "ag_host", "usr_host", 0); err == nil {
		t.Fatal("a room is staked — zero fee must be refused")
	}
}

func TestPrivateRoomStaysWaitingUntilRosterFull(t *testing.T) {
	creator := Player{AgentPublicID: "ag_host", OwnerPublicID: "usr_host", Seat: 1}
	repo := newSeatingRepo(500, creator)
	repo.m.Private = true
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, nil)

	view, err := svc.Join(context.Background(), "ag_friend", "usr_friend", "mf_test")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if repo.started {
		t.Fatal("second human must not start the table — need a full human roster")
	}
	if len(repo.m.Players) != 2 {
		t.Fatalf("players = %d, want 2", len(repo.m.Players))
	}
	if view.Status != StatusWaiting {
		t.Fatalf("view status = %s, want waiting", view.Status)
	}
	if HasHouseSeat(repo.m.Players) {
		t.Fatal("private invite rooms must never seat house bots")
	}
}

func TestPrivateRoomAllowsMoreInvitees(t *testing.T) {
	creator := Player{AgentPublicID: "ag_host", OwnerPublicID: "usr_host", Seat: 1}
	friend := Player{AgentPublicID: "ag_friend", OwnerPublicID: "usr_friend", Seat: 2}
	repo := newSeatingRepo(500, creator)
	repo.m.Private = true
	repo.m.Players = []Player{creator, friend}
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, nil)

	if _, err := svc.Join(context.Background(), "ag_third", "usr_third", "mf_test"); err != nil {
		t.Fatalf("third invitee join = %v, want success", err)
	}
	if len(HumanPlayers(repo.m.Players)) != 3 {
		t.Fatalf("humans = %d, want 3", len(HumanPlayers(repo.m.Players)))
	}
	if repo.started {
		t.Fatal("table must stay waiting until all 12 seats are human")
	}
}

func TestPrivateRoomStartsWhenTwelveHumansSit(t *testing.T) {
	players := make([]Player, 0, mf.RosterSize-1)
	for i := 1; i < mf.RosterSize; i++ {
		players = append(players, Player{
			AgentPublicID: "ag_seat_" + strconv.Itoa(i),
			OwnerPublicID: "usr_seat_" + strconv.Itoa(i),
			Seat:          i,
		})
	}
	repo := newSeatingRepo(500, players[0])
	repo.m.Private = true
	repo.m.Players = append([]Player{}, players...)
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, nil)

	view, err := svc.Join(context.Background(), "ag_seat_12", "usr_seat_12", "mf_test")
	if err != nil {
		t.Fatalf("12th join: %v", err)
	}
	if !repo.started {
		t.Fatal("full human roster must start the match")
	}
	if len(repo.m.Players) != mf.RosterSize {
		t.Fatalf("roster = %d, want %d", len(repo.m.Players), mf.RosterSize)
	}
	if HasHouseSeat(repo.m.Players) {
		t.Fatal("private room must not have house bots after start")
	}
	if view.Status != StatusActive {
		t.Fatalf("view status = %s, want active", view.Status)
	}
}

// recordingCreateRepo captures CreateWaiting input for CreateRoom assertions.
type recordingCreateRepo struct {
	fakeRepo
	last CreateMatchInput
}

func (r *recordingCreateRepo) CreateWaiting(_ context.Context, in CreateMatchInput) (Match, error) {
	r.last = in
	return Match{
		PublicID: in.PublicID, Status: StatusWaiting, EntryFee: in.EntryFee,
		Private: in.Private, Players: []Player{in.Creator},
	}, nil
}
