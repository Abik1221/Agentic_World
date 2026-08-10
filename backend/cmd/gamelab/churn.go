package main

import (
	"fmt"
	"log"
	"time"
)

// Queue churn: one mixed population, played end to end.
//
// # The question
//
// A ranked queue is not a load test, it is a CONSENT problem. Every agent in it has said "I
// want a match", and the platform has to get four different things right at once:
//
//   - an agent on autoplay re-enters after its match, without being asked again
//   - an agent NOT on autoplay leaves and STAYS left; it must never be silently replayed,
//     because that stakes its owner's coins on a match they did not ask for
//   - an agent that can no longer afford the stake is removed with a reason it can act on
//   - a newcomer arriving mid-flight is paired normally, not starved behind the incumbents
//
// Each is easy alone. They interact badly: the mechanism that re-queues an autoplay agent is
// the same one that would silently replay a non-autoplay agent, and the check that removes a
// broke agent runs on the same tick that admits a newcomer.
//
// # Why this is observed, not asserted from the code
//
// Reading the pairing query shows it selects `status = 'waiting'`, which is why a stale
// 'matched' entry cannot be re-paired. That is a good argument and it is not evidence. The
// only thing that settles it is watching a real non-autoplay agent after a real match and
// seeing that no entry comes back.
type churnRole string

const (
	roleAuto      churnRole = "autoplay"  // re-enters after every match
	roleOneShot   churnRole = "one-shot"  // plays once, must then be gone
	roleUnderfund churnRole = "underfund" // will run out of coins mid-run
	roleLatecomer churnRole = "latecomer" // joins after the first matches are already running
)

type churnAgent struct {
	ag   *labAgent
	role churnRole
	// seen records the queue states this agent passed through, so the report can show the
	// actual trajectory rather than a final snapshot that hides a re-queue that happened and
	// was undone.
	seen []string
	// matchIDs are DISTINCT matches this agent reached. Counting ticks-while-matched instead
	// reported "matches=6" for an agent that played ONE match slowly, which would have made a
	// stalled queue look like a busy one.
	matchIDs map[string]bool
	lastBal  int64
}

// runChurn drives the mixed population and reports what each role actually did.
func runChurn(a *api, lg *log.Logger, agents []*labAgent, tier string, rounds int) error {
	if len(agents) < 4 {
		return fmt.Errorf("churn needs at least 4 agents, have %d", len(agents))
	}
	pop := []*churnAgent{
		{ag: agents[0], role: roleAuto},
		{ag: agents[1], role: roleAuto},
		{ag: agents[2], role: roleOneShot},
		{ag: agents[3], role: roleUnderfund},
	}
	var latecomer *churnAgent
	if len(agents) > 4 {
		latecomer = &churnAgent{ag: agents[4], role: roleLatecomer}
	}

	// Funding is per role. The underfunded seat gets enough for exactly one stake plus its
	// minimum wallet reserve, so it runs itself broke by playing — rather than being blocked
	// on the first attempt, which would prove nothing about removal DURING churn.
	for _, c := range pop {
		coins := int64(20000)
		if c.role == roleUnderfund {
			coins = 600 // one 500 stake; a loss leaves it unable to afford the next
		}
		// TWO steps, and both are required: fundAgent credits the OWNER's treasury, and
		// allocateToAgent moves it into the agent's own wallet. Skipping the second leaves the
		// agent at balance 0 while the owner looks funded — which is exactly the state that
		// produced "Balance 0 is below the required 550" on the first run of this harness.
		if err := a.fundAgent(c.ag.DashToken, c.ag.AgentID, coins); err != nil {
			return fmt.Errorf("funding %s: %w", c.ag.Persona.Name, err)
		}
		if err := a.allocateToAgent(c.ag.DashToken, c.ag.AgentID, coins); err != nil {
			return fmt.Errorf("allocating to %s: %w", c.ag.Persona.Name, err)
		}
		c.lastBal = coins
		lg.Printf("  %-14s role=%-10s funded %d coins", c.ag.Persona.Name, c.role, coins)
	}
	if latecomer != nil {
		if err := a.fundAgent(latecomer.ag.DashToken, latecomer.ag.AgentID, 20000); err != nil {
			return fmt.Errorf("funding latecomer: %w", err)
		}
		if err := a.allocateToAgent(latecomer.ag.DashToken, latecomer.ag.AgentID, 20000); err != nil {
			return fmt.Errorf("allocating to latecomer: %w", err)
		}
		lg.Printf("  %-14s role=%-10s funded (joins later)", latecomer.ag.Persona.Name, latecomer.role)
	}

	// Autoplay ON for the roles that are supposed to keep going. This is what makes the
	// autoplay half of the test real: without it the harness enqueues once by hand, and an agent
	// that plays a single match is indistinguishable from one that correctly re-enters.
	for _, c := range pop {
		if c.role != roleAuto {
			continue
		}
		code, body, err := a.setAutoplay(c.ag.AgentKey, true, "ranked", 500, []string{"goofspiel"})
		if err != nil {
			return fmt.Errorf("autoplay for %s: %w", c.ag.Persona.Name, err)
		}
		lg.Printf("  %-14s autoplay ON -> HTTP %d %s", c.ag.Persona.Name, code, firstLine(body))
	}

	// Everyone except the latecomer enters now.
	for _, c := range pop {
		code, body, err := a.enqueueRanked(c.ag.AgentKey, tier)
		if err != nil {
			return fmt.Errorf("enqueue %s: %w", c.ag.Persona.Name, err)
		}
		lg.Printf("  %-14s enqueue -> HTTP %d %s", c.ag.Persona.Name, code, firstLine(body))
	}

	joinedLate := false
	for tick := 1; tick <= rounds; tick++ {
		time.Sleep(15 * time.Second)

		// The latecomer arrives once matches are already in flight.
		if latecomer != nil && !joinedLate && tick >= 2 {
			code, body, err := a.enqueueRanked(latecomer.ag.AgentKey, tier)
			if err != nil {
				return fmt.Errorf("latecomer enqueue: %w", err)
			}
			lg.Printf("  LATECOMER %s enqueue -> HTTP %d %s",
				latecomer.ag.Persona.Name, code, firstLine(body))
			pop = append(pop, latecomer)
			joinedLate = true
		}

		lg.Printf("── tick %d ─────────────────────────────────────────────", tick)
		for _, c := range pop {
			st, mid, err := a.queueStatus(c.ag.AgentKey)
			if err != nil {
				lg.Printf("  %-14s queue status error: %v", c.ag.Persona.Name, err)
				continue
			}
			bal, _ := a.agentBalance(c.ag.AgentKey)
			label := st
			if label == "" {
				label = "(not queued)"
			}
			c.seen = append(c.seen, label)
			if st == "matched" && mid != "" {
				if c.matchIDs == nil {
					c.matchIDs = map[string]bool{}
				}
				c.matchIDs[mid] = true
			}
			lg.Printf("  %-14s role=%-10s queue=%-12s balance=%-6d match=%s",
				c.ag.Persona.Name, c.role, label, bal, mid)
			c.lastBal = bal
		}

		// A one-shot agent leaves as soon as it has had its match — the explicit "no more".
		for _, c := range pop {
			if c.role == roleOneShot && len(c.matchIDs) > 0 && !contains(c.seen, "(left)") {
				if err := a.leaveQueue(c.ag.AgentKey); err != nil {
					lg.Printf("  %s could not leave the queue: %v", c.ag.Persona.Name, err)
					continue
				}
				c.seen = append(c.seen, "(left)")
				lg.Printf("  %-14s LEFT the queue after %d match(es)", c.ag.Persona.Name, len(c.matchIDs))
			}
		}
	}

	return reportChurn(a, lg, pop)
}

// reportChurn checks each role against what it was supposed to do, and FAILS on a violation.
func reportChurn(a *api, lg *log.Logger, pop []*churnAgent) error {
	lg.Printf("")
	lg.Printf("══════════════ churn result ══════════════")
	var problems []string

	for _, c := range pop {
		st, _, err := a.queueStatus(c.ag.AgentKey)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: final queue read failed: %v", c.ag.Persona.Name, err))
			continue
		}
		final := st
		if final == "" {
			final = "(not queued)"
		}
		lg.Printf("  %-14s role=%-10s distinct_matches=%d final=%s balance=%d",
			c.ag.Persona.Name, c.role, len(c.matchIDs), final, c.lastBal)
		lg.Printf("      trajectory: %v", c.seen)

		switch c.role {
		case roleOneShot:
			// The load-bearing assertion. A one-shot agent that is queued at the end was put
			// back by the platform, and that is its owner's coins on a match nobody asked for.
			if st == "waiting" {
				problems = append(problems, fmt.Sprintf(
					"%s is a ONE-SHOT agent but is queued as 'waiting' after leaving — it was "+
						"silently re-queued, which stakes its owner on a match they did not request",
					c.ag.Persona.Name))
			}
		case roleAuto:
			// Assert on RE-ENTRY, not on a second completed match.
			//
			// Demanding two distinct matches failed this role while the trajectories showed
			// autoplay working perfectly — a real match takes minutes, so the requirement was
			// really "did two matches finish inside my window", which is a statement about the
			// window. Re-entry is the actual property ("if they want to continue, they do"), it
			// is observable the moment a match ends, and it cannot be faked by a slow queue.
			if !reentered(c.seen) && len(c.matchIDs) < 2 {
				problems = append(problems, fmt.Sprintf(
					"%s is on AUTOPLAY but never re-entered the queue after a match "+
						"(trajectory %v) — a finished autoplay agent must come back on its own",
					c.ag.Persona.Name, c.seen))
			}
		case roleUnderfund:
			// It must not be able to keep staking past its balance. Being idle is fine; being
			// repeatedly matched while unable to cover the stake is not.
			if c.lastBal < 500 && st == "matched" {
				problems = append(problems, fmt.Sprintf(
					"%s holds %d, below the 500 stake, yet is still 'matched'",
					c.ag.Persona.Name, c.lastBal))
			}
		case roleLatecomer:
			// Being ACCEPTED is not the property under test — being PAIRED is. The first version
			// of this check passed on "waiting", so an agent that sat unpaired for the whole run
			// counted as success, which is precisely the starvation it was meant to detect.
			switch {
			case len(c.seen) == 0:
				problems = append(problems, fmt.Sprintf("%s never entered the queue", c.ag.Persona.Name))
			case !contains(c.seen, "waiting") && !contains(c.seen, "matched"):
				problems = append(problems, fmt.Sprintf(
					"%s joined mid-flight but never appeared in the queue", c.ag.Persona.Name))
			case len(c.matchIDs) == 0:
				problems = append(problems, fmt.Sprintf(
					"%s was accepted into the queue but never paired across the whole run — a "+
						"newcomer starving behind incumbents is the failure this role exists to catch",
					c.ag.Persona.Name))
			}
		}
	}

	if len(problems) > 0 {
		lg.Printf("")
		for _, p := range problems {
			lg.Printf("  VIOLATION: %s", p)
		}
		return fmt.Errorf("%d churn violation(s)", len(problems))
	}
	lg.Printf("  all roles behaved as specified")
	return nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

// reentered reports whether an agent went back into the queue AFTER having been in a match.
//
// "claimed" counts alongside "waiting": it is a live queue state on the way to a pairing, so an
// agent sitting in it has plainly re-entered. Treating only "waiting" as re-entry would make the
// check depend on which instant the poll happened to land on.
func reentered(seen []string) bool {
	wasMatched := false
	for _, st := range seen {
		switch st {
		case "matched":
			wasMatched = true
		case "waiting", "claimed":
			if wasMatched {
				return true
			}
		}
	}
	return false
}
