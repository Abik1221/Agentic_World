package monopoly

import (
	"context"
	"testing"
	"time"
)

// House-agent think time exists so a practice table feels like agents deciding
// rather than a board that resolves in the same instant you click. It runs inside
// Act, so the two properties that matter are that it STOPS and that it is never
// applied where money or a move window is at stake.

func TestThinkTimeIsOffByDefault(t *testing.T) {
	s := &Service{}
	if got := s.pause(context.Background(), 0, 0); got != 0 {
		t.Fatalf("an unconfigured service paused for %v; pacing must be opt-in so "+
			"existing callers and staked play are untouched", got)
	}
}

// The budget is what keeps one click from becoming a stall: a Monopoly turn is
// several engine steps, and an unbounded per-step sleep would hold the player's
// request open for as long as the bots felt like taking.
func TestThinkTimeStopsAtItsBudget(t *testing.T) {
	s := (&Service{}).WithThinkTime(ThinkTime{PerMove: 20 * time.Millisecond, Budget: 50 * time.Millisecond})
	var spent time.Duration
	for i := 0; i < 20; i++ {
		spent += s.pause(context.Background(), i, spent)
	}
	if spent > 50*time.Millisecond {
		t.Fatalf("paused %v in total, over the %v budget", spent, 50*time.Millisecond)
	}
	if spent == 0 {
		t.Fatal("paced service never paused at all")
	}
}

// A cancelled request must not keep sleeping — the client has gone.
func TestThinkTimeYieldsToACancelledContext(t *testing.T) {
	s := (&Service{}).WithThinkTime(ThinkTime{PerMove: 5 * time.Second, Budget: 10 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	s.pause(ctx, 0, 0)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("kept sleeping %v after the context was cancelled", elapsed)
	}
}

// Jitter varies the pause by seat so the house agents do not answer in lockstep,
// but it must stay inside the per-move figure it is derived from.
func TestThinkTimeJitterStaysBounded(t *testing.T) {
	per := 40 * time.Millisecond
	s := (&Service{}).WithThinkTime(ThinkTime{PerMove: per, Jitter: 40 * time.Millisecond, Budget: time.Hour})
	seen := map[time.Duration]bool{}
	for seat := 0; seat < 5; seat++ {
		start := time.Now()
		s.pause(context.Background(), seat, 0)
		d := time.Since(start)
		if d < per-10*time.Millisecond || d > per+60*time.Millisecond {
			t.Fatalf("seat %d paused %v, outside the expected band around %v", seat, d, per)
		}
		seen[d.Round(10*time.Millisecond)] = true
	}
	if len(seen) < 2 {
		t.Fatal("every seat paused for the same time — jitter is not varying by seat")
	}
}
