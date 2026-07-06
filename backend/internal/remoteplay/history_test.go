package remoteplay

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/engine/goofspiel"
)

// recordingDecider plays the lowest legal card and captures the last view it saw,
// so a test can assert what context the platform hands an agent each turn.
type recordingDecider struct {
	last GoofspielView
	seen int
}

func (d *recordingDecider) Decide(_ context.Context, v GoofspielView) (int, error) {
	d.last = v
	d.seen++
	return lowest(v.LegalActions), nil
}

// TestTurnViewCarriesReplayableHistory proves the enriched Goofspiel view is
// self-contained: by the final turn the agent has every prior round (both cards,
// winner, running scores) from its own perspective — no dependence on /event.
func TestTurnViewCarriesReplayableHistory(t *testing.T) {
	rec := &recordingDecider{}
	res, err := PlayGoofspiel(context.Background(), rec, NearestPool{}, []byte("hist-seed"))
	if err != nil {
		t.Fatalf("PlayGoofspiel: %v", err)
	}
	if !res.Finished {
		t.Fatal("match did not finish")
	}
	// The last view the seat saw was on the final round; it must contain every
	// already-resolved round before it.
	last := rec.last
	wantHistory := last.Round - 1
	if len(last.History) != wantHistory {
		t.Fatalf("final turn view history len=%d, want %d (round %d)", len(last.History), wantHistory, last.Round)
	}
	// History must be self-consistent: rounds in order, opponent cards revealed,
	// and the running scores on the last history entry match the current view.
	for i, h := range last.History {
		if h.Round != i+1 {
			t.Fatalf("history[%d].round=%d, want %d", i, h.Round, i+1)
		}
		if h.YourCard == 0 || h.OppCard == 0 {
			t.Fatalf("history[%d] missing revealed cards: %+v", i, h)
		}
	}
	if n := len(last.History); n > 0 {
		if last.History[n-1].Scores != last.Scores {
			t.Fatalf("history tail scores %v != current view scores %v", last.History[n-1].Scores, last.Scores)
		}
	}
}

// TestHistoryRoundsAreSelfConsistent proves each history row is internally
// consistent: equal revealed cards must be a tie; unequal cards must name the
// higher card's seat as winner (from seat 0's perspective).
func TestHistoryRoundsAreSelfConsistent(t *testing.T) {
	rec := &recordingDecider{} // seat A (0)
	if _, err := PlayGoofspiel(context.Background(), rec, NearestPool{}, []byte("seat-seed")); err != nil {
		t.Fatal(err)
	}
	for _, h := range rec.last.History {
		switch {
		case h.YourCard == h.OppCard:
			if h.Winner != goofspiel.Tie {
				t.Fatalf("round %d equal cards but winner=%d (want Tie)", h.Round, h.Winner)
			}
		case h.YourCard > h.OppCard:
			if h.Winner != goofspiel.SeatA {
				t.Fatalf("round %d your_card>opp_card but winner=%d (want SeatA)", h.Round, h.Winner)
			}
		default:
			if h.Winner != goofspiel.SeatB {
				t.Fatalf("round %d your_card<opp_card but winner=%d (want SeatB)", h.Round, h.Winner)
			}
		}
	}
}
