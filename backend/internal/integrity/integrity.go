// Package integrity decides whether a seat may be PAID from a staked table.
//
// It exists because the same rule was needed in three engines with three different
// settlement shapes, and the rule is subtle enough that three copies would drift. The
// Goofspiel path votes on the whole match (two seats, void or settle); Mafia and Monopoly
// pay a map of seats out of a pot, so the same question has a different answer there — see
// FilterPayable.
//
// THE RULE. A seat that cannot show a single one of its decisions was made by an LLM does
// not get paid from a table where money is at stake. Pyyol is an arena for AI agents, and
// a hand-written script taking coins from developers who are genuinely paying for
// inference is the thing this stops.
//
// WHY IT IS RELATIVE, AND NOT "zero proofs ⇒ never paid". The proof pipeline is real —
// the arena mints a per-turn token, ships it in the view, the SDK returns it as
// X-Pyyol-Proof on every routed model call, and the gateway records a bound decision — but
// it only produces evidence when PYYOL_LLM_GATEWAY_ENABLED is on AND the agent routes its
// client through pyyol.route(). Neither is guaranteed: the gateway is off by default, and
// an agent that calls its provider directly is unverified but not dishonest.
//
// So an absolute rule would refuse to pay honest developers for reasons outside their
// control. Requiring that some OTHER seat at the SAME TABLE proved its work makes this
// inert wherever the pipeline is not running, and self-arming wherever it is — with no
// threshold to tune and no deploy.
//
// One proof anywhere on the table is evidence the pipeline was reachable for that match.
// Against that, a seat with none is an outlier rather than a victim of an unshipped
// feature.
//
// WHAT IT DELIBERATELY DOES NOT CATCH: a table where nobody proves anything — two scripts
// playing each other, or a table where the gateway is off or nobody routed their client.
// It cannot, without refusing to pay honest players. That gap narrows as gateway routing
// becomes the norm; it closes only if routing is made mandatory for staked play, which is
// a product decision rather than something this file can enforce.
package integrity

import (
	"context"
	"log/slog"
)

// Checker reports how many decisions an agent PROVED were LLM-backed in a match —
// decisions whose model call carried a proof token the platform minted for that exact
// turn. Satisfied by the P-Index repo.
type Checker interface {
	BoundDecisions(ctx context.Context, matchID, agentPublicID string) (int, error)
}

// Verdict is the outcome for one table.
type Verdict struct {
	// Unproven lists the agents that proved nothing while at least one other seat did.
	// Empty when the rule does not bite — which is the normal case today.
	Unproven map[string]bool
	// Armed reports whether ANY seat proved a decision. False means the rule was inert
	// for this table, which is worth logging: it is the difference between "everyone was
	// clean" and "nothing was measured".
	Armed bool
}

// Blocked reports whether this agent must not be paid.
func (v Verdict) Blocked(agentPublicID string) bool { return v.Unproven[agentPublicID] }

// Evaluate reads every seat's proof count and applies the rule.
//
// FAILS OPEN on any read error: a table that cannot be judged is settled normally,
// because refusing to pay on a database hiccup would withhold real winnings from honest
// players in bulk during an outage. A cheat that slips through is still recorded and
// reviewable; a wrongly withheld payout is a support incident and a broken product.
// Evaluate reads every seat's proof count and applies the rule.
//
// absent names seats the platform had to act for more often than not. They are EXEMPT:
// a seat that never answered proves nothing for the obvious reason, and that is not
// evidence it played without an LLM. Withholding there would take the winnings of an
// agent that went dark near the end of a match it had already won — the arena's rule is
// that absence costs you the GAME, not your prize when you win anyway. Pass nil when
// attendance is unknown and every seat is judged, which is the pre-existing behaviour.
func Evaluate(ctx context.Context, c Checker, matchID string, agents []string, absent map[string]bool, log *slog.Logger) Verdict {
	if c == nil || len(agents) == 0 {
		return Verdict{}
	}
	bound := make(map[string]int, len(agents))
	total := 0
	for _, a := range agents {
		n, err := c.BoundDecisions(ctx, matchID, a)
		if err != nil {
			if log != nil {
				log.Warn("integrity: proof count unavailable; settling normally",
					"match", matchID, "agent", a, "error", err)
			}
			return Verdict{}
		}
		bound[a] = n
		total += n
	}
	if total == 0 {
		// Nothing was measured at this table. Inert by design.
		return Verdict{}
	}
	unproven := map[string]bool{}
	for _, a := range agents {
		if bound[a] != 0 {
			continue
		}
		if absent[a] {
			if log != nil {
				log.Info("integrity: seat exempt — it was ABSENT, so zero proofs is explained; it forfeits on the board instead",
					"match", matchID, "agent", a)
			}
			continue
		}
		unproven[a] = true
	}
	return Verdict{Unproven: unproven, Armed: true}
}

// FilterPayable removes unproven seats from a payout map and reports what was withheld.
//
// The multi-seat answer is deliberately DIFFERENT from Goofspiel's. There, a bad seat
// voids the match and both stakes go back — sound for two players. Doing that on an
// eleven-seat Mafia table would hand one cheater a way to cancel everybody else's game,
// which is a griefing tool, not a control. So the table settles for everyone who did
// prove their work, and only the unproven seat is not paid.
//
// The withheld share needs no new money path: settlement already posts anything unpaid as
// the floor-division remainder to platform revenue, with escrow balancing. That is why
// this returns a filtered map rather than trying to redistribute — redistributing would
// mean honest players profit from an accusation, which is a bad incentive to build in.
func FilterPayable(payouts map[string]int64, v Verdict, matchID string, log *slog.Logger) (map[string]int64, int64) {
	if !v.Armed || len(payouts) == 0 {
		return payouts, 0
	}
	out := make(map[string]int64, len(payouts))
	var withheld int64
	for agent, amount := range payouts {
		if v.Blocked(agent) {
			withheld += amount
			if log != nil {
				log.Warn("integrity: WITHHELD a payout — seat could not prove any decision was LLM-backed",
					"match", matchID, "agent", agent, "withheld_coins", amount)
			}
			continue
		}
		out[agent] = amount
	}
	return out, withheld
}
