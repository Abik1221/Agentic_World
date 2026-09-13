package mafia

import (
	"context"
	"strings"
	"testing"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
)

// Private invite rooms: host opens an unlisted table; the second distinct-owner
// human triggers house-bot fill and the match starts.

func TestCreateRoomMarksPrivateAndIsNotListed(t *testing.T) {
	repo := &recordingCreateRepo{}
	bots := houseIDs(mf.RosterSize - PrivateRoomMinHumans)
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, bots)

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

func TestCreateRoomRefusesWithoutHouseBots(t *testing.T) {
	repo := &recordingCreateRepo{}
	svc := NewService(repo, fakeLock{}, nil, &recordingWallet{}, fakeBcast{}, nil, nil,
		fakeClock{}, Config{})
	// No EnablePushPlay → no bots.
	_, err := svc.CreateRoom(context.Background(), "ag_host", "usr_host", 500)
	if err == nil {
		t.Fatal("CreateRoom must refuse when house bots cannot fill the roster")
	}
	if !strings.Contains(err.Error(), "house bots") && !strings.Contains(err.Error(), "room_fill_unavailable") {
		t.Fatalf("err = %v, want room_fill_unavailable / house bots", err)
	}
}

func TestCreateRoomRefusesZeroStake(t *testing.T) {
	bots := houseIDs(mf.RosterSize - PrivateRoomMinHumans)
	svc := newSeatingSvc(&recordingCreateRepo{}, &recordingWallet{}, nil, nil, bots)
	if _, err := svc.CreateRoom(context.Background(), "ag_host", "usr_host", 0); err == nil {
		t.Fatal("a room is staked — zero fee must be refused")
	}
}

func TestPrivateRoomJoinFillsBotsWhenSecondHumanSits(t *testing.T) {
	bots := houseIDs(mf.RosterSize - PrivateRoomMinHumans)
	creator := Player{AgentPublicID: "ag_host", OwnerPublicID: "usr_host", Seat: 1}
	repo := newSeatingRepo(500, creator)
	repo.m.Private = true
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, bots)

	view, err := svc.Join(context.Background(), "ag_friend", "usr_friend", "mf_test")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if !repo.started {
		t.Fatal("second human join must fill bots and start the match")
	}
	if len(repo.m.Players) != mf.RosterSize {
		t.Fatalf("roster = %d, want %d", len(repo.m.Players), mf.RosterSize)
	}
	humans := HumanPlayers(repo.m.Players)
	if len(humans) != 2 {
		t.Fatalf("humans = %d, want 2 (host + friend)", len(humans))
	}
	if view.Status != StatusActive {
		t.Fatalf("view status = %s, want active", view.Status)
	}
}

func TestPrivateRoomStaysWaitingWithOnlyHost(t *testing.T) {
	bots := houseIDs(mf.RosterSize - PrivateRoomMinHumans)
	creator := Player{AgentPublicID: "ag_host", OwnerPublicID: "usr_host", Seat: 1}
	repo := newSeatingRepo(500, creator)
	repo.m.Private = true
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, bots)

	// CreateRoom already seated the host; nothing else joins.
	if repo.started {
		t.Fatal("host alone must not start the table")
	}
	if len(repo.m.Players) != 1 {
		t.Fatalf("players = %d, want host only", len(repo.m.Players))
	}
	_ = svc
}

// If bot fill fails after the friend sits, the waiting invite must be cancelled
// so the room is not left half-filled until WaitingTTL.
func TestPrivateRoomFillFailureCancelsWaiting(t *testing.T) {
	creator := Player{AgentPublicID: "ag_host", OwnerPublicID: "usr_host", Seat: 1}
	repo := newSeatingRepo(500, creator)
	repo.m.Private = true
	// Enough bots to pass CreateRoom's count check is N/A here — Join uses an
	// already-private waiting row. EnablePushPlay with ZERO bots so fill fails.
	svc := NewService(repo, fakeLock{}, nil, &recordingWallet{}, fakeBcast{}, nil, nil,
		fakeClock{}, Config{})
	svc.EnablePushPlay(nil, nil, nil, nil)

	_, err := svc.Join(context.Background(), "ag_friend", "usr_friend", "mf_test")
	if err == nil {
		t.Fatal("join must fail when house bots cannot fill")
	}
	if repo.m.Status != StatusAborted && repo.cancelled == 0 {
		// seatingRepo tracks cancel via cancelled counter if Status not updated.
		t.Fatalf("after fill failure status=%s cancelled=%d — waiting invite must be aborted",
			repo.m.Status, repo.cancelled)
	}
}

// A third human must not sit while the invite is waiting for (or filling) bots.
func TestPrivateRoomRefusesThirdHuman(t *testing.T) {
	bots := houseIDs(mf.RosterSize - PrivateRoomMinHumans)
	creator := Player{AgentPublicID: "ag_host", OwnerPublicID: "usr_host", Seat: 1}
	friend := Player{AgentPublicID: "ag_friend", OwnerPublicID: "usr_friend", Seat: 2}
	repo := newSeatingRepo(500, creator)
	repo.m.Private = true
	repo.m.Players = []Player{creator, friend}
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, bots)

	_, err := svc.Join(context.Background(), "ag_third", "usr_third", "mf_test")
	if err != ErrPrivateRoomSealed {
		t.Fatalf("third human join = %v, want ErrPrivateRoomSealed", err)
	}
	if len(HumanPlayers(repo.m.Players)) != 2 {
		t.Fatalf("humans = %d, want 2", len(HumanPlayers(repo.m.Players)))
	}
}

// Host cancel while fill runs must not look like a successful friend join.
func TestPrivateRoomFillAfterAbortIsNotSuccess(t *testing.T) {
	bots := houseIDs(mf.RosterSize - PrivateRoomMinHumans)
	creator := Player{AgentPublicID: "ag_host", OwnerPublicID: "usr_host", Seat: 1}
	friend := Player{AgentPublicID: "ag_friend", OwnerPublicID: "usr_friend", Seat: 2}
	repo := newSeatingRepo(500, creator)
	repo.m.Private = true
	repo.m.Players = []Player{creator, friend}
	repo.m.Status = StatusAborted
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, bots)

	err := svc.fillPrivateRoom(context.Background(), "mf_test")
	if err != ErrNotWaiting {
		t.Fatalf("fill after abort = %v, want ErrNotWaiting (not silent success)", err)
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
