// Package readycheck decides when a matched table may actually start, and what to do
// about a seat that has not answered.
//
// # The gap it closes
//
// CreatePaired documents its own behaviour: it "escrows both stakes, deals the match, and
// persists it active in one step — no waiting window". So the moment the matcher pairs two
// agents, real coins are locked and a staked match is live. A developer who typed a command
// in a terminal has no chance to open the match, and an agent that was paired while its
// process was still starting loses a stake it never got to play for.
//
// Both halves of that are fixed by one rule: NOTHING IS ESCROWED UNTIL EVERY SEAT HAS SAID
// IT IS READY. A seat that never answers costs its owner time, not money.
//
// # Two mechanisms, not one
//
// A ready check and a start countdown are different things and merging them loses the
// protection:
//
//   - The READY CHECK asks "are you there?" and its failure mode is removal. It is what
//     guards the stake, and it happens before any money moves.
//   - The COUNTDOWN is the shared "we are starting" moment once everyone has committed. It
//     cannot fail — by then the table is settled.
//
// League of Legends is the reference: a 12-second accept window, no answer counts as a
// decline, the decliner is removed and everyone else returns to the queue. The window here
// is deliberately longer, because the participants are programs on someone else's
// infrastructure rather than a human staring at a button.
//
// # Why a pure package
//
// The same shape as internal/deadline: the policy and the decision live here, free of a
// database, so the rules that decide whether someone loses a stake can be tested exhaustively
// rather than inferred from an integration run. The caller does the escrow and the writes.
package readycheck

import "time"

// Policy is the per-game shape of a ready check.
type Policy struct {
	// Window is how long a seat has to answer one ask.
	Window time.Duration
	// MaxAsks is how many times a seat is asked before it is dropped. More than one because
	// a single missed ask is indistinguishable from a network blip, and dropping an honest
	// agent for one lost packet is the failure this whole package exists to avoid.
	MaxAsks int
	// Countdown is the settled "we are starting" pause after everyone is ready. Long enough
	// that a developer who chose to watch in a browser can get there.
	Countdown time.Duration
	// MinReady is the fewest ready seats a table may start with. Equal to the roster for a
	// duel; lower for the games that already tolerate a short-handed table.
	MinReady int
	// Seats is the full roster.
	Seats int
}

// DefaultPolicy returns the shipped policy for a game.
//
// Windows are longer than a human game's twelve seconds on purpose. A seat here is a process
// on a developer's own machine which may be cold-starting, and the cost of being too tight is
// that honest agents are dropped from tables they would have played.
//
// MinReady mirrors what the group queue already tolerates: Goofspiel is a duel and needs
// both, while Mafia and Monopoly can start short-handed with the house filling the rest,
// which is the only reason a twelve-seat table can ever start at all.
func DefaultPolicy(game string) Policy {
	switch game {
	case "monopoly":
		return Policy{Window: 20 * time.Second, MaxAsks: 2, Countdown: 10 * time.Second, MinReady: 2, Seats: 4}
	case "mafia":
		return Policy{Window: 20 * time.Second, MaxAsks: 2, Countdown: 10 * time.Second, MinReady: 4, Seats: 12}
	default: // goofspiel
		return Policy{Window: 20 * time.Second, MaxAsks: 2, Countdown: 10 * time.Second, MinReady: 2, Seats: 2}
	}
}

// Seat is one seat's readiness so far.
type Seat struct {
	AgentPublicID string
	// Ready is set once the agent has acknowledged. Acknowledgement must be IDEMPOTENT at
	// the caller: a retried ack is the same agent answering once, not a second seat.
	Ready bool
	// Asks is how many times this seat has been asked. Starts at 1 when the first ask goes
	// out; a seat with 0 has not been asked yet and cannot be dropped for silence.
	Asks int
	// AskedAt is when the current outstanding ask was sent.
	AskedAt time.Time
}

// Action is what the caller should do next.
type Action int

const (
	// Wait: asks are outstanding and inside their window. Do nothing.
	Wait Action = iota
	// Ask: these seats need a (re-)ask. Also the first ask.
	Ask
	// Drop: these seats are out of asks. Remove them, requeue them, and — because no money
	// has moved — refund nothing, since nothing was ever escrowed.
	Drop
	// Start: enough seats are ready. NOW escrow, then start the countdown.
	Start
	// Abandon: too few seats can ever be ready for this table to start. Release everyone.
	Abandon
)

// Decision is the outcome of one evaluation.
type Decision struct {
	Action Action
	// Seats names the seats an Ask or a Drop applies to.
	Seats []string
	// StartsIn is the countdown, set only on Start. The caller turns this into an ABSOLUTE
	// timestamp before sending it anywhere: a duration counted down independently by a
	// terminal and a browser drifts apart within seconds, and the two surfaces visibly
	// disagreeing is the exact failure a synchronised countdown exists to prevent.
	StartsIn time.Duration
}

// Evaluate decides what to do with a table right now.
//
// Deterministic in (policy, seats, now): the same inputs give the same decision, because a
// rule that decides whether someone is removed from a staked table must be explicable after
// the fact rather than merely observed.
//
// Order matters and is deliberate. Readiness is checked BEFORE expiry, so a seat that
// answered on the last ask is never dropped by a sweep that ran a moment later — the race
// between "acked" and "timed out" must resolve in favour of the agent, because the cost of
// getting it wrong is removing someone who did everything right.
func Evaluate(p Policy, seats []Seat, now time.Time) Decision {
	p = p.withDefaults()

	ready, live := 0, 0
	var toAsk, toDrop []string

	for _, s := range seats {
		if s.Ready {
			ready++
			live++
			continue
		}
		switch {
		case s.Asks == 0:
			// Never asked. Not silence — nobody has spoken to them yet.
			toAsk = append(toAsk, s.AgentPublicID)
			live++
		case now.Sub(s.AskedAt) < p.Window:
			// Outstanding and still inside its window.
			live++
		case s.Asks < p.MaxAsks:
			// Out of time on this ask, but they get another. A missed ask is not evidence
			// of an absent agent.
			toAsk = append(toAsk, s.AgentPublicID)
			live++
		default:
			toDrop = append(toDrop, s.AgentPublicID)
		}
	}

	// Drops are settled BEFORE a start, and the order is load-bearing.
	//
	// A dead seat is not counted as live, so a Monopoly table with two ready seats and two
	// silent ones satisfies "enough ready" while two agents are still attached to it. Starting
	// there would leave those two never dropped and never requeued — removed from the game by
	// omission rather than by decision, which is the quiet version of exactly what this package
	// exists to prevent. Caught by TestDropsAreReportedTogether.
	if len(toDrop) > 0 {
		return Decision{Action: Drop, Seats: toDrop}
	}
	// Enough seats have committed, and nothing is pending. NOW escrow, then count down.
	if ready >= p.MinReady && ready == live {
		return Decision{Action: Start, StartsIn: p.Countdown}
	}
	// No drops pending, and even if every remaining seat answers there are too few to play.
	// Release them rather than hold a table that cannot start.
	if live < p.MinReady {
		return Decision{Action: Abandon}
	}
	if len(toAsk) > 0 {
		return Decision{Action: Ask, Seats: toAsk}
	}
	return Decision{Action: Wait}
}

// StartsAt turns a countdown into the absolute instant every surface should count to.
//
// Exists so no caller is tempted to ship the duration. A terminal and a browser each counting
// down from ten drift apart immediately; both counting to the same timestamp cannot.
func StartsAt(now time.Time, d time.Duration) time.Time { return now.Add(d) }

func (p Policy) withDefaults() Policy {
	if p.Window <= 0 {
		p.Window = 20 * time.Second
	}
	if p.MaxAsks <= 0 {
		p.MaxAsks = 2
	}
	if p.Countdown <= 0 {
		p.Countdown = 10 * time.Second
	}
	if p.Seats <= 0 {
		p.Seats = 2
	}
	if p.MinReady <= 0 || p.MinReady > p.Seats {
		p.MinReady = p.Seats
	}
	return p
}
