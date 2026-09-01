package match_test

import (
	"context"
	"testing"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/match"
)

// The countdown must leave the server as an INSTANT, never as a duration.
//
// This is the property, not the field. A countdown shipped as "10 seconds" and counted down
// independently by a terminal and a browser drifts apart within seconds — different tick
// alignment, different render loops, a device clock minutes out — and two surfaces disagreeing
// about when a staked match begins is worse than showing no countdown at all. Both counting TO
// the same instant cannot disagree.
//
// server_now is asserted alongside it because the instant alone is not enough: a client whose
// own clock is wrong renders a wrong countdown from a correct timestamp. With both, it can
// measure its offset and be right anyway.

// newSvcAt builds a service pinned to a fixed clock, so server_now is assertable.
func newSvcAt(t *testing.T, now time.Time) (*match.Service, *fakeRepo) {
	t.Helper()
	svc, repo, _, _, _ := readySvc(t, now)
	return svc, repo
}

func TestViewShipsTheCountdownAsAnInstantAndTheServerClock(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	svc, repo := newSvcAt(t, now)

	startsAt := now.Add(10 * time.Second)
	m := readyMatch("m_cd")
	m.Status = match.StatusActive
	m.StartsAt = &startsAt
	m.TotalRounds = 13
	m.State = gs.State{Round: 1, PrizePool: 3, Hands: [2][]int{{1, 2}, {3, 4}}}
	repo.putReadyMatch(m)

	v, err := svc.State(context.Background(), "m_cd", "a", false, 0)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	if v.StartsAt == nil {
		t.Fatal("no starts_at on the view — a client has nothing to count to")
	}
	if !v.StartsAt.Equal(startsAt) {
		t.Errorf("starts_at = %v, want the stored instant %v", v.StartsAt, startsAt)
	}
	// The server's own clock, so a client with a skewed device clock can still be right.
	if v.ServerNow.IsZero() {
		t.Error("no server_now — a client cannot correct its own clock offset without it")
	}
	if !v.ServerNow.Equal(now) {
		t.Errorf("server_now = %v, want the service clock %v", v.ServerNow, now)
	}
}

func TestAMatchWithNoReadyCheckHasNoCountdown(t *testing.T) {
	// starts_at is omitempty and must stay absent rather than becoming a zero time, which a
	// client would render as a countdown to January year 1 — a very large negative number on
	// screen, which is the kind of thing that gets screenshotted.
	now := time.Unix(1_700_000_000, 0).UTC()
	svc, repo := newSvcAt(t, now)

	m := readyMatch("m_plain")
	m.Status = match.StatusActive
	m.TotalRounds = 13
	m.State = gs.State{Round: 1, Hands: [2][]int{{1}, {2}}}
	repo.putReadyMatch(m)

	v, err := svc.State(context.Background(), "m_plain", "a", false, 0)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if v.StartsAt != nil {
		t.Errorf("starts_at = %v on a match that never had a ready check; it must be absent", v.StartsAt)
	}
	if v.ServerNow.IsZero() {
		t.Error("server_now must be on EVERY view, not only countdown ones — it corrects the deadline too")
	}
}
