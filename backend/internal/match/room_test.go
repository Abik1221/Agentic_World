package match_test

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-arena/arena/internal/match"
)

// Rooms: a private staked table two developers reach by sharing its id.
//
// The whole feature is one bit — the match is hidden from the open lobby — so these tests
// are mostly about what must NOT have changed. A room that quietly skipped the stake
// floor, the verification gate or the same-owner refusal would be a way to move coins
// between two accounts with none of the controls the open lobby applies, and it would
// look like a working feature the entire time.

func TestCreateRoomMarksTheMatchPrivate(t *testing.T) {
	svc, repo := newSvcWithRepo()

	if _, err := svc.CreateRoom(context.Background(), "ag_a", "usr_a", 50); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if !repo.lastCreate.Private {
		t.Fatal("a room was created public; the open lobby would list it and a stranger " +
			"could take the seat before the invited player used the code")
	}
}

// The open lobby must stay open. This is the regression that would turn every existing
// table invisible and make the arena look empty.
func TestCreateOpenStaysPublic(t *testing.T) {
	svc, repo := newSvcWithRepo()

	if _, err := svc.CreateOpen(context.Background(), "ag_a", "usr_a", 50); err != nil {
		t.Fatalf("CreateOpen: %v", err)
	}
	if repo.lastCreate.Private {
		t.Fatal("an open lobby match was marked private; nobody would be able to find it")
	}
}

// A room is joinable by its id, by somebody else. That is the entire point.
func TestRoomIsJoinableByItsID(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	id, err := svc.CreateRoom(ctx, "ag_a", "usr_a", 50)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatalf("joining a room by id failed: %v", err)
	}
}

// You cannot sit at both seats of your own room.
//
// This matters more for rooms than for the open lobby. A private staked table between two
// accounts is the shape a coin transfer takes, and the creator playing themselves is the
// cheapest version of it.
func TestRoomRefusesTheCreatorsOwnAccount(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	id, err := svc.CreateRoom(ctx, "ag_a", "usr_a", 50)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	// A different agent, same owner.
	_, err = svc.Join(ctx, "ag_a2", "usr_a", id)
	if !errors.Is(err, match.ErrSameOwner) {
		t.Fatalf("join by the creator's own account = %v, want ErrSameOwner", err)
	}
}

// A room must refuse a stake the open lobby would refuse.
//
// Pinned because the codebase has already been bitten by exactly this: the stake floor was
// bypassed once by a second caller that reached the escrow path around the check. Rooms are
// that second caller, and this is the test that fails if they ever stop delegating.
func TestRoomEnforcesTheSameStakeRules(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	for _, bid := range []int64{0, -1} {
		if _, err := svc.CreateRoom(ctx, "ag_a", "usr_a", bid); err == nil {
			t.Fatalf("CreateRoom accepted a bid of %d; the open lobby refuses it", bid)
		}
	}
}

// The anti-drift guard: both paths must accept and reject the same things.
//
// Not a style preference. Two create paths that disagree is how a control ends up applied
// on one and forgotten on the other, and the forgotten one is always the newer, less
// travelled path — which here is the one carrying private staked matches.
func TestRoomAndOpenAgreeOnEveryInput(t *testing.T) {
	ctx := context.Background()

	for _, bid := range []int64{-5, 0, 1, 50, 1_000_000} {
		openSvc := newSvc()
		roomSvc := newSvc()

		_, openErr := openSvc.CreateOpen(ctx, "ag_a", "usr_a", bid)
		_, roomErr := roomSvc.CreateRoom(ctx, "ag_a", "usr_a", bid)

		if (openErr == nil) != (roomErr == nil) {
			t.Fatalf("bid %d: CreateOpen err=%v but CreateRoom err=%v — the two paths "+
				"disagree, so a control applies to one and not the other", bid, openErr, roomErr)
		}
	}
}

// A cancelled room releases its seat like any other waiting match.
func TestRoomCanBeCancelledByItsCreator(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	id, err := svc.CreateRoom(ctx, "ag_a", "usr_a", 50)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if err := svc.Cancel(ctx, "ag_a", id); err != nil {
		t.Fatalf("cancelling a room: %v", err)
	}
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err == nil {
		t.Fatal("a cancelled room was still joinable")
	}
}

// Somebody who is not the creator cannot cancel the room out from under them.
func TestRoomCancelRefusesANonCreator(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	id, err := svc.CreateRoom(ctx, "ag_a", "usr_a", 50)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if err := svc.Cancel(ctx, "ag_b", id); err == nil {
		t.Fatal("a stranger cancelled somebody else's room")
	}
}
