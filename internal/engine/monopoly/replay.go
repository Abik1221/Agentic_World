package monopoly

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// replay.go turns the determinism guarantee into an auditable artifact. A match is
// fully determined by its Config, seed, and the ordered list of decisions. The
// Table records every applied decision as a Move; Replay re-runs them to reproduce
// the exact event log and final state, and ReplayHash/Verify let a third party
// prove a match was not tampered with — the answer to "was this rigged?".

// Move is one recorded decision: the seat and the action it submitted.
type Move struct {
	Seat   int    `json:"seat"`
	Action Action `json:"action"`
}

// Replay re-runs a match from its seed + recorded moves and returns the resulting
// final state and full event log. The output is identical to the live match
// because the engine is deterministic and all randomness derives from the seed.
func Replay(cfg Config, seed []byte, moves []Move) (State, []Event, error) {
	e := New(cfg)
	s, evs := e.Init(seed)
	for i, m := range moves {
		ns, ev, err := e.Step(s, m.Seat, m.Action, seed)
		if err != nil {
			return s, evs, fmt.Errorf("monopoly: replay failed at move %d (seat %d, %q): %w", i, m.Seat, m.Action.Kind, err)
		}
		s = ns
		evs = append(evs, ev...)
	}
	return s, evs, nil
}

// ReplayHash is the canonical commitment to a match: sha256 over the JSON-encoded
// event log. Identical event streams hash identically; any change flips the hash.
func ReplayHash(events []Event) string {
	b, _ := json.Marshal(events)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Verify reports whether replaying (seed, moves) reproduces the claimed event log.
func Verify(cfg Config, seed []byte, moves []Move, claimedLog []Event) (bool, error) {
	_, evs, err := Replay(cfg, seed, moves)
	if err != nil {
		return false, err
	}
	return ReplayHash(evs) == ReplayHash(claimedLog), nil
}
