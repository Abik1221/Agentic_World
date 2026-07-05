// Package replay turns the append-only event log into the source of truth for a
// match: it persists events (gap-free), reconstructs the final result, and
// verifies a match end-to-end against its revealed seed (provable fairness). All
// logic here is pure over the event stream; persistence is injected via EventStore
// (an in-memory store ships now; the pgx-backed store + match_events migration
// land with the match worker in Stage 3, where the matches table exists).
package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// EventStore persists and loads a match's ordered event log.
type EventStore interface {
	// Append adds events for a match. Implementations MUST enforce gap-free,
	// monotonic seq (no gaps, no duplicates).
	Append(ctx context.Context, matchID string, events []gs.Event) error
	// Load returns all events for a match in seq order.
	Load(ctx context.Context, matchID string) ([]gs.Event, error)
}

// Result is the reconstructed outcome of a match, folded purely from its events.
type Result struct {
	Finished bool
	Winner   int
	Scores   [2]int
	Rounds   []gs.RoundResult
}

// Reconstruct folds the event log into the final result without needing the seed.
// It proves the log is internally consistent and yields the canonical outcome.
func Reconstruct(events []gs.Event) (Result, error) {
	var res Result
	for _, ev := range events {
		switch ev.Type {
		case gs.EvRoundRevealed:
			var p gs.RoundRevealedPayload
			if err := remarshal(ev.Payload, &p); err != nil {
				return Result{}, fmt.Errorf("seq %d: %w", ev.Seq, err)
			}
			res.Rounds = append(res.Rounds, gs.RoundResult(p))
			res.Scores = p.Scores
		case gs.EvMatchFinished:
			var p gs.MatchFinishedPayload
			if err := remarshal(ev.Payload, &p); err != nil {
				return Result{}, fmt.Errorf("seq %d: %w", ev.Seq, err)
			}
			res.Finished = true
			res.Winner = p.Winner
			res.Scores = p.Scores
		}
	}
	return res, nil
}

// Verify is the strong, provable-fairness check. Given the revealed seed and the
// event log it: (1) confirms sha256(seed) == the committed value, (2) re-derives
// the prize order from the seed, (3) re-applies the recorded card choices through
// a fresh engine, and (4) asserts the recomputed per-round and final results match
// the log exactly. Any discrepancy means the match is invalid.
func Verify(seed []byte, events []gs.Event) error {
	if len(events) == 0 {
		return fmt.Errorf("empty event log")
	}
	// 1. match_created → config + commit.
	var created gs.MatchCreatedPayload
	if events[0].Type != gs.EvMatchCreated {
		return fmt.Errorf("first event must be match_created, got %s", events[0].Type)
	}
	if err := remarshal(events[0].Payload, &created); err != nil {
		return err
	}
	if !gs.VerifyCommit(seed, created.Commit) {
		return fmt.Errorf("commit mismatch: seed does not match the published commit")
	}

	// 2. Rebuild the engine and re-derive the prize order from the seed.
	eng := gs.New(gs.Config{Cards: created.Cards, Rounds: created.Rounds, FairnessMode: created.FairnessMode})
	state, _ := eng.Init(seed)

	// 3 & 4. Re-apply each recorded round and compare. The seq must be strictly
	// monotonic and rounds must appear in order 1,2,3,… — a reordered or spliced
	// log is rejected before the game math is even recomputed.
	var lastFinishSeen bool
	prevSeq := -1
	expectedRound := 1
	for _, ev := range events {
		if ev.Seq <= prevSeq {
			return fmt.Errorf("non-monotonic event seq: %d after %d", ev.Seq, prevSeq)
		}
		prevSeq = ev.Seq
		switch ev.Type {
		case gs.EvRoundRevealed:
			var rec gs.RoundRevealedPayload
			if err := remarshal(ev.Payload, &rec); err != nil {
				return err
			}
			if rec.Round != expectedRound {
				return fmt.Errorf("out-of-order round: recorded %d, expected %d", rec.Round, expectedRound)
			}
			expectedRound++
			var err error
			if state, _, err = eng.Seal(state, gs.SeatA, rec.Cards[gs.SeatA]); err != nil {
				return fmt.Errorf("round %d seal A: %w", rec.Round, err)
			}
			if state, _, err = eng.Seal(state, gs.SeatB, rec.Cards[gs.SeatB]); err != nil {
				return fmt.Errorf("round %d seal B: %w", rec.Round, err)
			}
			state, _, err = eng.Resolve(state)
			if err != nil {
				return fmt.Errorf("round %d resolve: %w", rec.Round, err)
			}
			got := state.History[len(state.History)-1]
			if got.Prize != rec.Prize || got.PrizePool != rec.PrizePool ||
				got.Winner != rec.Winner || got.Scores != rec.Scores {
				return fmt.Errorf("round %d mismatch: recomputed %+v != recorded %+v", rec.Round, got, rec)
			}
		case gs.EvMatchFinished:
			var rec gs.MatchFinishedPayload
			if err := remarshal(ev.Payload, &rec); err != nil {
				return err
			}
			if !state.Finished || state.Winner != rec.Winner || state.Scores != rec.Scores {
				return fmt.Errorf("final mismatch: recomputed winner=%d scores=%v != recorded winner=%d scores=%v",
					state.Winner, state.Scores, rec.Winner, rec.Scores)
			}
			lastFinishSeen = true
		}
	}
	if !lastFinishSeen {
		return fmt.Errorf("event log has no match_finished")
	}
	return nil
}

// Hash is a stable digest of the event log, independent of whether payloads
// are Go structs (in-memory) or maps (DB round-trip) — both canonicalize to the
// same JSON. Stored on the match and re-checkable by anyone.
func Hash(events []gs.Event) (string, error) {
	h := sha256.New()
	for _, ev := range events {
		b, err := canonicalJSON(map[string]any{"seq": ev.Seq, "type": string(ev.Type), "payload": ev.Payload})
		if err != nil {
			return "", err
		}
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// remarshal converts a payload (struct OR map[string]any from a DB round-trip)
// into a typed payload struct, so reconstruction/verification are representation-agnostic.
func remarshal(payload, dst any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// canonicalJSON marshals v with sorted map keys by round-tripping through a
// generic value (Go's json sorts map keys), making the output order-stable.
func canonicalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}
