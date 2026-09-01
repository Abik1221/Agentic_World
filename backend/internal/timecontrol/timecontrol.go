// Package timecontrol allocates thinking time symmetrically across the seats of a match.
//
// # What this is actually for — and what it is NOT for
//
// CORRECTION, recorded because the first version of this file asserted the opposite and the
// claim was wrong. An audit reported that internal/deadline let an agent pad its early
// decisions to raise its own p95 and thereafter receive up to FOUR TIMES its opponent's
// wall-clock in the same match. deadline.For is indeed per-agent, which is what the audit
// saw. But every caller maxes it across the seats before using it:
//
//   - Goofspiel (match/service.go:341-359) takes the LONGEST window over the unsealed seats;
//     the round deadline is shared.
//   - Monopoly (monopoly/service.go:85-97) does the same across the whole table, and says why:
//     "cutting an agent off because of who it was seated with is the one thing a deadline
//     must never depend on."
//   - Mafia does not use adaptive windows at all — phaseWindow returns a per-phase constant
//     from the engine (mafia/service.go:889-895).
//
// So there is no within-match asymmetry to close, and deadline's claim that waiting longer
// buys authenticity rather than advantage holds better than the audit credited. This package
// is NOT an urgent fix for an exploit.
//
// # The two things that are genuinely wrong, and that this does fix
//
// BETWEEN-MATCH VARIANCE. Because the window is the maximum over the seats present, an agent
// drawn against a slow opponent gets more thinking time than the same agent drawn against a
// fast one. Within a match that is fair; across a benchmark it is not, because measured
// performance then depends on who you happened to be matched with. For a leaderboard that is
// noise. For a certified measurement it is a confound in the estimator.
//
// TIME IS NOT A DECLARED PARAMETER. A reproducible benchmark has to publish its time control
// the way a chess tournament does. "However long the slowest agent in your pairing happened
// to need" is not a specification, cannot be pinned in a spec hash, and cannot be reproduced
// by a third party a year later.
//
// A secondary, weaker point: a padder does still drag the shared window from 45s toward the
// 180s ceiling. It hands its opponent exactly the same time, so there is no direct relative
// gain — but an entrant that converts extra time into quality more efficiently than its
// opponent does profit slightly. That is second-order and is not the motivation here.
//
// # A chess clock instead
//
// Every seat receives an IDENTICAL total budget for the match and spends it as it chooses.
// The properties this buys:
//
//   - SYMMETRIC BY CONSTRUCTION. The budget does not depend on history, latency, or anything
//     the agent controls. There is no quantity to inflate.
//   - PADDING IS SELF-DEFEATING. Time spent early is time unavailable later, so the exploit
//     inverts into a cost. This is the difference between a rule and a request.
//   - SLOW BUT HONEST IS STILL FINE. An agent that needs 90s for a hard decision can take it;
//     it simply pays for it out of its own budget rather than out of its opponent's.
//   - A DEAD SEAT CANNOT STALL THE TABLE. Its budget drains and the fallback plays, exactly
//     as a timeout does today.
//   - LEGIBLE. This is standard tournament practice in computer chess and Go. A researcher
//     reading our method section already knows what a time control is, and knows why an
//     adaptive per-seat deadline would not have been one.
//
// Thinking time stops being a confound and becomes a declared, equal resource — which also
// makes "how well does this model allocate its own compute?" a measurable question rather
// than a leak.
//
// # What stays
//
// The liveness probe stays, for the job it is actually good at: telling a dead endpoint from
// a thinking one, so the table stops waiting on a process that will never answer. What it no
// longer does is GRANT TIME. Being reachable is not evidence of computing.
//
// # Purity
//
// No clock, no RNG, no I/O. The caller measures elapsed time and tells us; we do arithmetic.
// A deadline that varies run to run cannot be explained to a developer who missed one, and a
// match that cannot be replayed cannot be published.
package timecontrol

import (
	"fmt"
	"time"
)

// Control is the per-match time control. Identical for every seat, by construction: there is
// no per-agent field here, and that absence is the whole design.
type Control struct {
	// Budget is the total thinking time each seat gets for the entire match.
	Budget time.Duration
	// PerMove caps any single decision, so one seat cannot spend its whole budget on one
	// turn and leave a 12-seat table waiting. It bounds latency for everyone else; it is not
	// a fairness device, since Budget already handles fairness.
	PerMove time.Duration
	// Increment is added back after each completed decision (Fischer increment). It keeps a
	// long match from degenerating into forced instant moves at the end, which would measure
	// the clock rather than the agent. Zero is a valid, stricter choice.
	Increment time.Duration
	// Grace is a small allowance beyond Budget before a seat is treated as out of time,
	// absorbing network jitter and scheduling delay that the agent did not cause. It is NOT
	// extra thinking time: it is not returned by Window and cannot be spent deliberately.
	Grace time.Duration
}

// Validate rejects a control that could not produce a fair match.
func (c Control) Validate() error {
	switch {
	case c.Budget <= 0:
		return fmt.Errorf("timecontrol: Budget must be > 0, got %v", c.Budget)
	case c.PerMove <= 0:
		return fmt.Errorf("timecontrol: PerMove must be > 0, got %v", c.PerMove)
	case c.Increment < 0:
		return fmt.Errorf("timecontrol: Increment must be >= 0, got %v", c.Increment)
	case c.Grace < 0:
		return fmt.Errorf("timecontrol: Grace must be >= 0, got %v", c.Grace)
	case c.PerMove > c.Budget:
		// Not fatal arithmetically, but it means the cap never binds and the first decision
		// could consume everything — almost certainly a mis-specified control.
		return fmt.Errorf("timecontrol: PerMove (%v) exceeds Budget (%v)", c.PerMove, c.Budget)
	}
	return nil
}

// DefaultControl returns the shipped control for a game.
//
// Budgets are derived as (typical decisions per seat) x (the per-decision BASE that
// internal/deadline already used), not the ceiling. The base is what an ordinary agent was
// expected to need; the ceiling was the escape hatch that became the exploit. Multiplying the
// base by the decision count reproduces the same total generosity for an honest agent while
// removing the ability to take it from someone else.
//
// PerMove keeps deadline's ceiling, so a single genuinely hard decision still gets room.
//
// MONOPOLY IS DELIBERATELY NOT CALIBRATED HERE. A moderate Monopoly match is ~400 decisions
// per seat (harness/bench/plan.py:42), so 400 x 60s is 6.7 hours and plainly wrong, while the
// drive loop already abandons a match at 5 minutes (match/drive.go:135). The honest budget is
// a measurement — median decisions per seat and median latency from agent_match_decisions —
// not a number invented here. Until that measurement exists this returns a conservative
// control and says so, because picking a threshold off a synthetic harness is how this repo
// has been wrong before.
func DefaultControl(game string) Control {
	switch game {
	case "mafia":
		// ~22 decisions per seat (plan.py:38) x 60s base.
		return Control{
			Budget: 22 * time.Minute, PerMove: 2 * time.Minute,
			Increment: 5 * time.Second, Grace: 2 * time.Second,
		}
	case "monopoly":
		// PLACEHOLDER — see the note above. Chosen to be survivable rather than correct.
		return Control{
			Budget: 20 * time.Minute, PerMove: 3 * time.Minute,
			Increment: 2 * time.Second, Grace: 2 * time.Second,
		}
	default: // goofspiel: 13 decisions per seat x 45s base.
		return Control{
			Budget: 10 * time.Minute, PerMove: 3 * time.Minute,
			Increment: 5 * time.Second, Grace: 2 * time.Second,
		}
	}
}

// NeedsCalibration reports whether a game's default control is a placeholder rather than a
// measured value. Exposed so a caller can refuse to publish a certified result from a game
// whose time control was guessed.
func NeedsCalibration(game string) bool { return game == "monopoly" }

// Clock is one seat's remaining budget. Zero value is not usable; call NewClock.
type Clock struct {
	remaining time.Duration
	spent     time.Duration
	decisions int
}

// NewClock starts a seat at the full budget.
func NewClock(c Control) (*Clock, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Clock{remaining: c.Budget}, nil
}

// Remaining is the seat's unspent budget.
func (k *Clock) Remaining() time.Duration { return k.remaining }

// Spent is the total time this seat has consumed.
func (k *Clock) Spent() time.Duration { return k.spent }

// Decisions is how many decisions the seat has completed.
func (k *Clock) Decisions() int { return k.decisions }

// Window is how long to wait for this seat's next decision.
//
// It is the smaller of what the seat has left and the per-move cap. When the budget is gone
// this returns 0, which the caller must read as "play the fallback now" rather than "wait
// forever".
func (k *Clock) Window(c Control) time.Duration {
	if k.remaining <= 0 {
		return 0
	}
	w := k.remaining
	if w > c.PerMove {
		w = c.PerMove
	}
	return w
}

// Deadline is the wall-clock instant this seat's decision expires, given when it was asked.
//
// Grace is added here and only here: it absorbs jitter the agent did not cause, and because
// it is not part of Window the agent is never told about it and cannot plan to spend it.
func (k *Clock) Deadline(c Control, askedAt time.Time) time.Time {
	return askedAt.Add(k.Window(c) + c.Grace)
}

// Spend records a completed decision and returns whether the seat is still in time.
//
// used is the observed wall-clock the agent took. It is charged in full even when it exceeds
// the window — an overrun is real time the table waited, and forgiving it would restore a
// smaller version of the same exploit.
//
// The increment is credited only for a decision made IN TIME. Crediting an overrun would pay
// an agent for being late.
func (k *Clock) Spend(c Control, used time.Duration) (inTime bool) {
	if used < 0 {
		used = 0
	}
	limit := k.Window(c) + c.Grace
	inTime = k.remaining > 0 && used <= limit

	k.spent += used
	k.remaining -= used
	k.decisions++

	if inTime {
		k.remaining += c.Increment
		if k.remaining > c.Budget {
			// Increments may not accumulate into an advantage over a seat that thought
			// harder. The budget is a ceiling as well as a starting point.
			k.remaining = c.Budget
		}
	}
	if k.remaining < 0 {
		k.remaining = 0
	}
	return inTime
}

// Expired reports whether the seat is out of time and every further decision must fall back.
func (k *Clock) Expired() bool { return k.remaining <= 0 }
