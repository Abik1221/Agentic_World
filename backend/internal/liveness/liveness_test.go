package liveness_test

import (
	"context"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/liveness"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

type fakeRepo struct {
	prev      time.Time
	has       bool
	readErr   error
	outages   int
	beats     int
	lastGrace time.Time

	extendCalls int
	extendedTo  time.Time
	extendRows  int64
	extendErr   error
}

func (r *fakeRepo) LastBeat(context.Context) (time.Time, bool, error) {
	return r.prev, r.has, r.readErr
}
func (r *fakeRepo) Beat(_ context.Context, at time.Time) error {
	r.beats++
	r.prev = at
	r.has = true
	return nil
}
func (r *fakeRepo) RecordOutage(_ context.Context, _, _, graceUntil time.Time, _ int64) error {
	r.outages++
	r.lastGrace = graceUntil
	return nil
}

func (r *fakeRepo) ExtendActiveDeadlines(_ context.Context, until time.Time) (int64, error) {
	r.extendCalls++
	r.extendedTo = until
	return r.extendRows, r.extendErr
}

func newAt(sec int64) *fakeClock { return &fakeClock{t: time.Unix(sec, 0).UTC()} }

// An ordinary restart must NOT open a grace window — otherwise every deploy would
// hand a free pass to agents that had genuinely abandoned their matches.
func TestShortGapIsNotAnOutage(t *testing.T) {
	clk := newAt(10_000)
	repo := &fakeRepo{prev: clk.t.Add(-30 * time.Second), has: true}
	tr := liveness.NewTracker(clk, nil)

	tr.Detect(context.Background(), repo)

	if tr.InGrace() {
		t.Fatal("a 30s restart was treated as an outage; forfeits would be suppressed")
	}
	if repo.outages != 0 {
		t.Fatalf("audit row written for a non-outage: %d", repo.outages)
	}
}

// A real gap opens grace, and the window must outlast a move window so every seat
// gets a genuine chance to play rather than a technically-open deadline.
func TestRealOutageOpensGrace(t *testing.T) {
	clk := newAt(10_000)
	repo := &fakeRepo{prev: clk.t.Add(-5 * time.Minute), has: true}
	tr := liveness.NewTracker(clk, nil)

	tr.Detect(context.Background(), repo)

	if !tr.InGrace() {
		t.Fatal("a 5-minute outage did not open a grace window")
	}
	if repo.outages != 1 {
		t.Fatalf("expected exactly one audit row, got %d", repo.outages)
	}
	if got := tr.GraceUntil(); !got.Equal(clk.t.Add(liveness.GraceAfter)) {
		t.Fatalf("grace_until = %v, want now+%v", got, liveness.GraceAfter)
	}
	// Suppressing the sweep ALONE would only postpone the mass forfeit: the same lapsed
	// matches reappear the instant grace ends. Deadlines must actually be re-armed, and
	// past the window so they are not immediately expired again.
	if repo.extendCalls != 1 {
		t.Fatalf("ExtendActiveDeadlines called %d times, want 1 — forfeits would only be delayed", repo.extendCalls)
	}
	if !repo.extendedTo.After(tr.GraceUntil()) {
		t.Fatalf("deadlines re-armed to %v, not past grace_until %v — they would expire on the next sweep",
			repo.extendedTo, tr.GraceUntil())
	}

	// Grace must EXPIRE. A window that never closed would permanently disable
	// forfeits, which is the same bug as never forfeiting at all.
	clk.t = clk.t.Add(liveness.GraceAfter + time.Second)
	if tr.InGrace() {
		t.Fatal("grace window never expired")
	}
}

// A very long incident must not pause the sweeper for hours — an operator handles
// that deliberately.
func TestLongOutageIsCapped(t *testing.T) {
	clk := newAt(10_000)
	repo := &fakeRepo{prev: clk.t.Add(-8 * time.Hour), has: true}
	tr := liveness.NewTracker(clk, nil)

	tr.Detect(context.Background(), repo)

	if got := tr.GraceUntil(); !got.Equal(clk.t.Add(liveness.MaxGrace)) {
		t.Fatalf("grace_until = %v, want capped at now+%v", got, liveness.MaxGrace)
	}
}

// FAIL CLOSED. A read error or a fresh install must leave forfeits ENABLED: a bug in
// outage detection must never become a blanket amnesty on every staked match.
func TestFailsClosedOnErrorOrFreshInstall(t *testing.T) {
	clk := newAt(10_000)

	tr := liveness.NewTracker(clk, nil)
	tr.Detect(context.Background(), &fakeRepo{readErr: context.DeadlineExceeded})
	if tr.InGrace() {
		t.Fatal("a failed heartbeat read opened a grace window")
	}

	tr2 := liveness.NewTracker(clk, nil)
	tr2.Detect(context.Background(), &fakeRepo{has: false})
	if tr2.InGrace() {
		t.Fatal("a fresh install (no heartbeat row) opened a grace window")
	}
}

// A nil tracker (feature unwired) must behave exactly like today: forfeits on.
func TestNilTrackerReportsNoGrace(t *testing.T) {
	var tr *liveness.Tracker
	if tr.InGrace() {
		t.Fatal("nil tracker claimed grace")
	}
	if !tr.GraceUntil().IsZero() {
		t.Fatal("nil tracker returned a grace deadline")
	}
	tr.Detect(context.Background(), &fakeRepo{}) // must not panic
}

// Detect must be driven ONLY by our own heartbeat. Agents cannot write it, so they
// cannot manufacture grace for a match they are losing — this is why detection is
// not based on counting agent disconnects.
func TestGraceIsDrivenOnlyByOurOwnHeartbeat(t *testing.T) {
	clk := newAt(10_000)
	// Heartbeat healthy: whatever the agents did, there is no grace.
	repo := &fakeRepo{prev: clk.t.Add(-1 * time.Second), has: true}
	tr := liveness.NewTracker(clk, nil)
	tr.Detect(context.Background(), repo)
	if tr.InGrace() {
		t.Fatal("grace opened while the platform was demonstrably serving")
	}
}
