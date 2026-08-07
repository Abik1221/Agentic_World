package modelboard

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

type stubSeats struct {
	seats []Seat
	err   error
	calls int
}

func (s *stubSeats) Seats(context.Context, string, time.Time, time.Time) ([]Seat, error) {
	s.calls++
	return s.seats, s.err
}

func twoModelSeats(n int) []Seat {
	var out []Seat
	for i := 0; i < n; i++ {
		m := "m" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		res1, res2 := "win", "loss"
		if i%3 == 0 {
			res1, res2 = "loss", "win"
		}
		out = append(out,
			seat(m, "goofspiel", "a1", "anthropic/claude", "sc_1", "dev1", res1, 1.0),
			seat(m, "goofspiel", "a2", "openai/gpt", "sc_2", "dev2", res2, 1.0))
	}
	return out
}

func fastSvc(src SeatSource) *Service {
	s := NewService(src, 90*24*time.Hour, slog.New(slog.DiscardHandler))
	s.fit = fastConfig()
	return s
}

func TestNoSnapshotIsDistinctFromAnEmptyBoard(t *testing.T) {
	// The distinction the handler depends on. "We have not fitted one yet" and "we fitted one and
	// no model qualified" are different states, and the second is a real finding about the
	// platform — it must never be manufactured by a service that has only just started.
	svc := fastSvc(&stubSeats{seats: twoModelSeats(20)})
	if svc.Snapshot() != nil {
		t.Fatal("a fresh service reported a snapshot it never computed")
	}
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	snap := svc.Snapshot()
	if snap == nil {
		t.Fatal("no snapshot after a successful refresh")
	}
	if len(snap.Board.Ratings) != 2 {
		t.Fatalf("got %d ratings, want 2: %s", len(snap.Board.Ratings), snap.Board.Summary())
	}
	if snap.ComputedAt.IsZero() || snap.WindowDays != 90 {
		t.Errorf("provenance missing: computed=%v window=%d", snap.ComputedAt, snap.WindowDays)
	}
}

func TestAFailedRefreshKeepsTheLastGoodBoard(t *testing.T) {
	// A refresh that cannot read the database must not also destroy the last good answer. Wiping
	// it would turn a transient outage into an empty leaderboard, which reads to a developer as
	// "my model was removed" — a far worse message than a slightly stale one.
	src := &stubSeats{seats: twoModelSeats(20)}
	svc := fastSvc(src)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	good := svc.Snapshot()
	if good == nil {
		t.Fatal("no snapshot after the first refresh")
	}

	src.err = errors.New("database is on fire")
	if err := svc.Refresh(context.Background()); err == nil {
		t.Fatal("a failing source did not surface an error")
	}
	still := svc.Snapshot()
	if still == nil {
		t.Fatal("a failed refresh destroyed the previous board")
	}
	if still.ComputedAt != good.ComputedAt {
		t.Error("a failed refresh replaced the snapshot")
	}
}

func TestAnEmptyPlatformProducesAnEmptyBoardNotAnError(t *testing.T) {
	// A season with no verified play is the state Pyyol was actually in when this was built. It
	// must fit cleanly and report zero models, because an error here would be indistinguishable
	// from a broken service — and the truthful answer is that nothing has qualified yet.
	svc := fastSvc(&stubSeats{})
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("empty platform errored: %v", err)
	}
	snap := svc.Snapshot()
	if snap == nil {
		t.Fatal("no snapshot for an empty platform")
	}
	if len(snap.Board.Ratings) != 0 {
		t.Errorf("an empty platform produced %d ratings", len(snap.Board.Ratings))
	}
}

func TestUnverifiedSeatsAreExcludedAndTheReasonIsPublished(t *testing.T) {
	// The board's whole claim is that it ranks models rather than assertions. A seat with no
	// verified model must be dropped AND counted, because "why is my model missing" needs an
	// answer a developer can act on.
	seats := []Seat{
		seat("m1", "goofspiel", "a1", "", "sc_1", "dev1", "win", 1.0),
		seat("m1", "goofspiel", "a2", "openai/gpt", "sc_2", "dev2", "loss", 1.0),
	}
	svc := fastSvc(&stubSeats{seats: seats})
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	snap := svc.Snapshot()
	if len(snap.Board.Ratings) != 0 {
		t.Errorf("an unverified seat reached the board")
	}
	if snap.Board.SeatsExcluded["no_verified_model"] != 1 {
		t.Errorf("census = %v, want the exclusion named", snap.Board.SeatsExcluded)
	}
}

func TestTheWorkerRefreshesImmediatelyRatherThanAfterOneInterval(t *testing.T) {
	// Without an immediate run the board is empty for a whole interval after every deploy — and an
	// empty board is indistinguishable from "no model qualified", which would be a claim we make
	// by accident.
	src := &stubSeats{seats: twoModelSeats(12)}
	svc := fastSvc(src)
	w := NewWorker(svc, time.Hour, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	deadline := time.After(5 * time.Second)
	for svc.Snapshot() == nil {
		select {
		case <-deadline:
			cancel()
			t.Fatal("the worker did not refresh within 5s despite a one-hour interval — it is " +
				"waiting for the first tick instead of running immediately")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if src.calls == 0 {
		t.Error("the worker never read the source")
	}
}

func TestTheWorkerSurvivesAFailingSource(t *testing.T) {
	// A board that keeps working through a database blip is the point of having a snapshot. The
	// worker must log and continue rather than exiting, or one transient failure permanently stops
	// every future refresh.
	src := &stubSeats{err: errors.New("down")}
	svc := fastSvc(src)
	w := NewWorker(svc, 20*time.Millisecond, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the worker did not return after its context was cancelled")
	}
	if src.calls < 2 {
		t.Errorf("the worker made %d attempts; it stopped retrying after a failure", src.calls)
	}
}
