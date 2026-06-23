// Package rating computes and persists ELO skill ratings per agent per season,
// applied at match finalize (idempotently, keyed by match id), and serves the
// public leaderboard. The ELO math here is pure and deterministic; the persistence
// (read-modify-write under row locks) lives in the store, which calls the Compute
// closure this package supplies — so the formula never leaks into SQL.
package rating

import "math"

// Tie is the winner sentinel for a drawn match (matches gs.Tie).
const Tie = -1

// Expected returns seat A's expected score (0..1) against seat B given their ELOs.
func Expected(ra, rb int) float64 {
	return 1.0 / (1.0 + math.Pow(10, float64(rb-ra)/400.0))
}

// Update returns the new (eloA, eloB) after a result, where scoreA is 1 (A won),
// 0.5 (tie) or 0 (A lost) and k is the volatility factor.
func Update(ra, rb int, scoreA float64, k int) (int, int) {
	ea := Expected(ra, rb)
	scoreB, eb := 1-scoreA, 1-ea
	na := ra + int(math.Round(float64(k)*(scoreA-ea)))
	nb := rb + int(math.Round(float64(k)*(scoreB-eb)))
	return na, nb
}

// ScoreForSeat0 maps a winning seat (0, 1, or Tie) to seat 0's score.
func ScoreForSeat0(winnerSeat int) float64 {
	switch {
	case winnerSeat == Tie:
		return 0.5
	case winnerSeat == 0:
		return 1
	default:
		return 0
	}
}
