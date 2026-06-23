package replay_test

import (
	"context"
	"encoding/json"
	"testing"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/replay"
)

// driveMatch plays a full match with a deterministic policy and returns the live
// final state plus the complete event log.
func driveMatch(t *testing.T, seed []byte) (gs.State, []gs.Event) {
	t.Helper()
	eng := gs.New(gs.DefaultConfig())
	pick := func(s gs.State, seat int) int { // play the lowest legal card
		lo := s.Hands[seat][0]
		for _, c := range s.Hands[seat] {
			if c < lo {
				lo = c
			}
		}
		return lo
	}
	s, evs := eng.Init(seed)
	for !s.Finished {
		var e1, e2, e3 []gs.Event
		var err error
		if s, e1, err = eng.Seal(s, gs.SeatA, pick(s, gs.SeatA)); err != nil {
			t.Fatal(err)
		}
		if s, e2, err = eng.Seal(s, gs.SeatB, pick(s, gs.SeatB)); err != nil {
			t.Fatal(err)
		}
		if s, e3, err = eng.Resolve(s); err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e1...)
		evs = append(evs, e2...)
		evs = append(evs, e3...)
	}
	return s, evs
}

func TestReconstructEqualsLive(t *testing.T) {
	live, evs := driveMatch(t, []byte("seed-1"))
	res, err := replay.Reconstruct(evs)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if !res.Finished || res.Winner != live.Winner || res.Scores != live.Scores {
		t.Fatalf("reconstruct %+v != live (winner=%d scores=%v)", res, live.Winner, live.Scores)
	}
	if len(res.Rounds) != len(live.History) {
		t.Fatalf("round count %d != %d", len(res.Rounds), len(live.History))
	}
}

func TestVerifyOK(t *testing.T) {
	seed := []byte("provably-fair-seed")
	_, evs := driveMatch(t, seed)
	if err := replay.Verify(seed, evs); err != nil {
		t.Fatalf("Verify on a clean match failed: %v", err)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	seed := []byte("seed-T")
	_, evs := driveMatch(t, seed)
	// Tamper: inflate the prize recorded in the first round_revealed.
	tampered := false
	for i, ev := range evs {
		if ev.Type == gs.EvRoundRevealed {
			p := ev.Payload.(gs.RoundRevealedPayload)
			p.Prize += 100
			evs[i].Payload = p
			tampered = true
			break
		}
	}
	if !tampered {
		t.Fatal("no round_revealed event to tamper")
	}
	if err := replay.Verify(seed, evs); err == nil {
		t.Fatal("Verify accepted a tampered log")
	}
}

func TestVerifyDetectsWrongSeed(t *testing.T) {
	_, evs := driveMatch(t, []byte("real-seed"))
	if err := replay.Verify([]byte("not-the-seed"), evs); err == nil {
		t.Fatal("Verify accepted a seed that does not match the commit")
	}
}

// A spliced/reordered log must be rejected by the seq + round-order guards, even
// though every individual event is genuine.
func TestVerifyDetectsReorderedLog(t *testing.T) {
	seed := []byte("seed-R")
	_, evs := driveMatch(t, seed)
	// Find two round_revealed events and swap them.
	var idx []int
	for i, ev := range evs {
		if ev.Type == gs.EvRoundRevealed {
			idx = append(idx, i)
			if len(idx) == 2 {
				break
			}
		}
	}
	if len(idx) < 2 {
		t.Fatal("need two rounds to reorder")
	}
	evs[idx[0]], evs[idx[1]] = evs[idx[1]], evs[idx[0]]
	if err := replay.Verify(seed, evs); err == nil {
		t.Fatal("Verify accepted a reordered event log")
	}
}

func TestReplayHashStableAcrossRepresentations(t *testing.T) {
	_, evs := driveMatch(t, []byte("seed-H"))
	h1, err := replay.ReplayHash(evs)
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip through JSON so payloads become map[string]any (as from a DB).
	b, _ := json.Marshal(evs)
	var roundtrip []gs.Event
	if err := json.Unmarshal(b, &roundtrip); err != nil {
		t.Fatal(err)
	}
	h2, err := replay.ReplayHash(roundtrip)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("ReplayHash not representation-stable:\n%s\n%s", h1, h2)
	}
}

func TestMemStoreContiguity(t *testing.T) {
	store := replay.NewMemStore()
	ctx := context.Background()
	ok := []gs.Event{{Seq: 0, Type: gs.EvMatchCreated}, {Seq: 1, Type: gs.EvPrizeRevealed}}
	if err := store.Append(ctx, "m_1", ok); err != nil {
		t.Fatalf("contiguous append failed: %v", err)
	}
	// A gap (seq jumps to 5) must be rejected.
	if err := store.Append(ctx, "m_1", []gs.Event{{Seq: 5, Type: gs.EvCardSealed}}); err == nil {
		t.Fatal("MemStore accepted a non-contiguous seq")
	}
	got, _ := store.Load(ctx, "m_1")
	if len(got) != 2 {
		t.Fatalf("loaded %d events, want 2", len(got))
	}
}
