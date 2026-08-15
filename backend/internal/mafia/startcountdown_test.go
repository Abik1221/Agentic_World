package mafia

import (
	"context"
	"testing"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/readycheck"
)

// The start countdown, and the one thing about it that is easy to get wrong.
//
// A Mafia table used to go live in the same instant its twelfth seat was taken: status flipped
// to active and the first phase deadline was set to now + the phase window. Two people lost by
// that. A developer watching a terminal saw a lobby become a running match with no moment in
// between — there was nothing to count down because no future instant existed. And an agent
// still finishing its startup was handed a night phase whose clock had already been running.
//
// starts_at fixes both, but ONLY if the first phase window is measured from it. Setting
// starts_at while leaving `deadline = now + window` would publish a countdown and then spend
// it: the table would show "starting in 10s" while the night phase quietly burned ten of its
// own seconds. That is the assertion below, and it is the reason this file exists rather than
// a single "starts_at is not nil" check, which the broken version would also pass.

// fillRoster seats house bots until the table starts, returning the repo that captured it.
func fillRoster(t *testing.T) *seatingRepo {
	t.Helper()
	bots := houseIDs(mf.RosterSize - 1)
	repo := newSeatingRepo(0, Player{AgentPublicID: "ag_dev", OwnerPublicID: "usr_dev", Seat: 1})
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, bots)
	for _, b := range bots {
		if _, err := svc.JoinHouseSeat(context.Background(), b.PublicID, b.OwnerPublicID, "mf_test"); err != nil {
			t.Fatalf("seating %s: %v", b.PublicID, err)
		}
	}
	if !repo.started {
		t.Fatal("a full roster did not start the match")
	}
	return repo
}

func TestStartPublishesAnAbsoluteStartInstant(t *testing.T) {
	repo := fillRoster(t)

	// fakeClock is pinned, so the expected instant is exact rather than a tolerance. A
	// countdown a client renders from must be a specific moment, not approximately one.
	now := time.Unix(1_700_000_000, 0)
	want := now.Add(readycheck.DefaultPolicy("mafia").Countdown)
	if !repo.startsAt.Equal(want) {
		t.Fatalf("starts_at = %v, want %v (now + the mafia countdown)", repo.startsAt, want)
	}
	if !repo.startsAt.After(now) {
		t.Fatal("starts_at is not in the future, so there is nothing to count down to")
	}
}

// The assertion the broken version fails. Everything else about a countdown can be right while
// this is wrong, and nothing on screen would look off — the phase would simply be short.
func TestTheFirstPhaseWindowOpensWhenPlayDoes(t *testing.T) {
	repo := fillRoster(t)

	window := repo.deadline.Sub(repo.startsAt)
	full := mf.PhaseDuration(repo.m.State.Phase)
	if window != full {
		t.Fatalf("the first %s phase gets %v of its %v window; the countdown ate %v of it",
			repo.m.State.Phase, window, full, full-window)
	}
}

// The countdown is a real, non-zero pause. A policy change to zero would silently restore the
// old "live the instant the table fills" behaviour with every other assertion still green.
func TestTheCountdownIsNotZero(t *testing.T) {
	for _, game := range []string{"mafia", "monopoly", "goofspiel"} {
		if d := readycheck.DefaultPolicy(game).Countdown; d <= 0 {
			t.Fatalf("%s has no start countdown (%v)", game, d)
		}
	}
}
