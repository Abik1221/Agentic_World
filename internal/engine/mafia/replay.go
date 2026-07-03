package mafia

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// replay.go turns the determinism guarantee into an auditable artifact. A match is
// fully determined by its seats, seed, day cap, and the ordered list of decisions.
// The Table records every applied decision as a Move; Replay re-runs them to
// reproduce the exact event log and final state, and ReplayHash/Verify let a third
// party prove a match was not tampered with.

// Move is one recorded decision: the seat and the action it submitted.
type Move struct {
	Seat   int    `json:"seat"`
	Action Action `json:"action"`
}

// Replay re-runs a match from its inputs + recorded moves and returns the final
// state and full event log, identical to the live match.
func Replay(seats []int, seed []byte, maxDays int, moves []Move) (State, []Event, error) {
	e := NewWithMaxDays(maxDays)
	s, evs := e.Init(seed, append([]int(nil), seats...))
	for i, m := range moves {
		ns, ev, err := e.Act(s, m.Seat, m.Action)
		if err != nil {
			return s, evs, fmt.Errorf("mafia: replay failed at move %d (seat %d, %q): %w", i, m.Seat, m.Action.Kind, err)
		}
		s = ns
		evs = append(evs, ev...)
	}
	return s, evs, nil
}

// ReplayHash is the canonical commitment to a match: sha256 over the JSON-encoded
// event log.
func ReplayHash(events []Event) string {
	b, _ := json.Marshal(events)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Verify reports whether replaying the inputs + moves reproduces the claimed log.
func Verify(seats []int, seed []byte, maxDays int, moves []Move, claimedLog []Event) (bool, error) {
	_, evs, err := Replay(seats, seed, maxDays, moves)
	if err != nil {
		return false, err
	}
	return ReplayHash(evs) == ReplayHash(claimedLog), nil
}
