package clips_test

import (
	"testing"

	"github.com/agent-arena/arena/internal/clips"
	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

func created(rounds int) gs.Event {
	return gs.Event{Seq: 0, Type: gs.EvMatchCreated, Payload: gs.MatchCreatedPayload{Rounds: rounds}}
}
func round(seq, rnd, pool, ca, cb, winner, sa, sb int) gs.Event {
	return gs.Event{Seq: seq, Type: gs.EvRoundRevealed, Payload: gs.RoundRevealedPayload{
		Round: rnd, Prize: pool, PrizePool: pool, Cards: [2]int{ca, cb}, Winner: winner, Scores: [2]int{sa, sb},
	}}
}
func finished(seq, sa, sb, winner int) gs.Event {
	return gs.Event{Seq: seq, Type: gs.EvMatchFinished, Payload: gs.MatchFinishedPayload{Scores: [2]int{sa, sb}, Winner: winner}}
}

func has(triggers []clips.Trigger, kind string) bool {
	for _, t := range triggers {
		if t.Kind == kind {
			return true
		}
	}
	return false
}

func TestDetectTieCarry(t *testing.T) {
	ev := []gs.Event{created(13), round(2, 1, 24, 8, 6, gs.SeatA, 24, 0)}
	if !has(clips.Detect(ev), clips.TieCarry) {
		t.Fatal("expected tie_carry for a 24-coin pot")
	}
}

func TestDetectPerfectRead(t *testing.T) {
	// Winner (seat A) played exactly one higher than the loser.
	ev := []gs.Event{created(13), round(2, 1, 5, 8, 7, gs.SeatA, 5, 0)}
	if !has(clips.Detect(ev), clips.PerfectRead) {
		t.Fatal("expected perfect_read when winning card == losing card + 1")
	}
}

func TestDetectAllInLastRound(t *testing.T) {
	ev := []gs.Event{created(13), round(40, 13, 4, 3, 2, gs.SeatA, 50, 30), finished(41, 50, 30, gs.SeatA)}
	if !has(clips.Detect(ev), clips.AllIn) {
		t.Fatal("expected all_in on the final round")
	}
}

func TestDetectBlowout(t *testing.T) {
	ev := []gs.Event{created(13), round(2, 1, 5, 9, 1, gs.SeatA, 65, 26), finished(3, 65, 26, gs.SeatA)}
	if !has(clips.Detect(ev), clips.Blowout) {
		t.Fatal("expected blowout when winner >= 60 points")
	}
}

func TestDetectComeback(t *testing.T) {
	// Seat A trails by 18, then wins the match.
	ev := []gs.Event{
		created(13),
		round(2, 1, 6, 2, 9, gs.SeatB, 6, 24), // A trails 6-24 (deficit 18)
		round(3, 2, 8, 11, 1, gs.SeatA, 40, 30),
		finished(4, 40, 30, gs.SeatA),
	}
	if !has(clips.Detect(ev), clips.Comeback) {
		t.Fatal("expected comeback when the winner overcame a >=15 deficit")
	}
}

func TestDetectNothingDramatic(t *testing.T) {
	ev := []gs.Event{
		created(13),
		round(2, 1, 3, 5, 2, gs.SeatA, 3, 0),
		round(3, 2, 2, 4, 7, gs.SeatB, 3, 2),
		finished(4, 3, 2, gs.SeatA),
	}
	if got := clips.Detect(ev); len(got) != 0 {
		t.Fatalf("expected no triggers, got %+v", got)
	}
}
