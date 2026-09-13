package match_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform"
)

// Rooms: a private staked table two developers reach by sharing its id.
//
// The whole feature is one bit — the match is hidden from the open lobby — so these tests
// are mostly about what must NOT have changed. A room that quietly skipped the stake
// floor or the same-owner refusal would be a way to move coins between two accounts
// with none of the controls the open lobby applies. The sit gate is the one that
// must differ: rooms are not the ranked certify-endpoint lobby.

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
// A host who funded the sitting agent with exactly the stake must be able to
// open the room. Sit eligibility is covering stake only (no reserve stacked).
func TestCreateRoomAcceptsAnAgentThatHoldsExactlyTheStake(t *testing.T) {
	lim := &coveringLimits{}
	svc := svcWithLimits(lim)
	if _, err := svc.CreateRoom(context.Background(), "ag_a", "usr_a", 500); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if lim.covering != 1 {
		t.Fatalf("room consulted CheckJoinCoveringStake %d times, want 1", lim.covering)
	}
	if lim.ranked != 0 {
		t.Fatalf("room used CheckJoin (%d) — prefer covering-stake when available", lim.ranked)
	}
}

func TestCreateOpenUsesCoveringStake(t *testing.T) {
	lim := &coveringLimits{}
	svc := svcWithLimits(lim)
	if _, err := svc.CreateOpen(context.Background(), "ag_a", "usr_a", 500); err != nil {
		t.Fatalf("CreateOpen: %v", err)
	}
	if lim.covering != 1 {
		t.Fatalf("open lobby consulted CheckJoinCoveringStake %d times, want 1", lim.covering)
	}
	if lim.ranked != 0 {
		t.Fatalf("open lobby used CheckJoin (%d) — sit is stake-only, no reserve", lim.ranked)
	}
}

type coveringLimits struct{ covering, ranked int }

func (c *coveringLimits) CheckJoin(context.Context, string, int64) error {
	c.ranked++
	return nil
}
func (c *coveringLimits) CheckJoinCoveringStake(context.Context, string, int64) error {
	c.covering++
	return nil
}
func (c *coveringLimits) CheckConcurrency(context.Context, string) error { return nil }

// splitVerifier is the ranked-vs-room gate split: CheckEligible is the public
// lobby / ranked bar; CheckPrivateRoom is the friend-invite bar.
type splitVerifier struct {
	ranked, rooms     int
	rankedErr, roomErr error
}

func (s *splitVerifier) Record(context.Context, string, *string, int) {}
func (s *splitVerifier) CheckEligible(context.Context, string) error {
	s.ranked++
	return s.rankedErr
}
func (s *splitVerifier) CheckPrivateRoom(context.Context, string) error {
	s.rooms++
	return s.roomErr
}

func svcWithVerifier(v match.Verifier) *match.Service {
	return match.New(newFakeRepo(), fakeLocker{}, match.NoopLimits{}, match.NoopWallet{},
		match.NoopBroadcaster{}, v, match.NoopRater{}, match.NoopFinishHook{},
		platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		match.Config{MoveWindow: 20 * time.Second, RakePct: 5, Rounds: 13, LockTTL: 5 * time.Second})
}

func TestCreateRoomDoesNotUseTheRankedCertifyGate(t *testing.T) {
	v := &splitVerifier{rankedErr: errors.New("Verify your agent's endpoint before entering ranked play.")}
	if _, err := svcWithVerifier(v).CreateRoom(context.Background(), "ag_a", "usr_a", 500); err != nil {
		t.Fatalf("room refused a playable local agent: %v", err)
	}
	if v.rooms != 1 {
		t.Fatalf("room consulted CheckPrivateRoom %d times, want 1", v.rooms)
	}
	if v.ranked != 0 {
		t.Fatalf("room used ranked CheckEligible (%d) — that is the certify-endpoint banner", v.ranked)
	}
}

func TestCreateOpenStillUsesTheRankedCertifyGate(t *testing.T) {
	v := &splitVerifier{rankedErr: errors.New("Verify your agent's endpoint before entering ranked play.")}
	if _, err := svcWithVerifier(v).CreateOpen(context.Background(), "ag_a", "usr_a", 500); err == nil {
		t.Fatal("open lobby skipped the ranked certify gate")
	}
	if v.ranked != 1 {
		t.Fatalf("open lobby consulted CheckEligible %d times, want 1", v.ranked)
	}
	if v.rooms != 0 {
		t.Fatalf("open lobby used the private-room gate")
	}
}

func TestJoinPrivateRoomUsesTheRoomPlayableGate(t *testing.T) {
	v := &splitVerifier{rankedErr: errors.New("Verify your agent's endpoint before entering ranked play.")}
	svc := svcWithVerifier(v)
	id, err := svc.CreateRoom(context.Background(), "ag_a", "usr_a", 500)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if _, err := svc.Join(context.Background(), "ag_b", "usr_b", id); err != nil {
		t.Fatalf("joining a room hit the ranked certify gate: %v", err)
	}
	if v.rooms != 2 {
		t.Fatalf("create+join consulted CheckPrivateRoom %d times, want 2", v.rooms)
	}
	if v.ranked != 0 {
		t.Fatalf("room join used ranked CheckEligible (%d)", v.ranked)
	}
}

func TestCreateRoomRefusesWhenTheRoomGateDoes(t *testing.T) {
	v := &splitVerifier{roomErr: errors.New("agent_not_playable")}
	if _, err := svcWithVerifier(v).CreateRoom(context.Background(), "ag_a", "usr_a", 500); err == nil {
		t.Fatal("room accepted an unplayable agent")
	}
}

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
