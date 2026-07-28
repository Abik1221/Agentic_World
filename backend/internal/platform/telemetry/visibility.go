package telemetry

// Who may see a traced event.
//
// Two rules drive everything here:
//
//  1. A developer may see their OWN agent's behaviour, and nothing else. Not the
//     opponent's reasoning, not the opponent's decisions, not the platform's books.
//     An agent's rationale is its strategy — exposing another developer's rationale,
//     even after the match, hands over the thing they built.
//
//  2. Anything not explicitly listed is OPERATOR-ONLY. The allowlist is deliberately
//     inverted so that adding a new event type defaults to HIDDEN. If we ever forget
//     to classify one, the failure is "a developer cannot see their own data" (an
//     annoyance) instead of "a developer can see the platform's money movements or a
//     rival's strategy" (a breach). Fail closed, always.
//
// This lives next to the emitters on purpose: the person adding an event type should
// have to make the visibility decision in the same change.

// devVisibleEventTypes is the complete set a developer may read for their own agent.
var devVisibleEventTypes = map[string]bool{
	// The agent's own play.
	EventAgentDecision: true,
	// Its own table talk, including lines the rules refused — a developer needs to see
	// that their agent tried to speak at night and was silenced, or it looks like a
	// bug in their code.
	EventAgentSaid:        true,
	EventAgentSayRejected: true,
	// Its own connectivity and endpoint health. These are the things a developer
	// debugs most often and they reveal nothing about anyone else.
	EventAgentConnected:        true,
	EventAgentDisconnected:     true,
	EventAgentRegistered:       true,
	EventAgentEndpointVerified: true,
	EventAgentEndpointFailed:   true,
}

// DevVisible reports whether a developer may see this event type for their own agent.
//
// Deliberately NOT dev-visible, and each for a concrete reason:
//   - money movement (stake escrow/release, settlement, payout, rake) — the platform's
//     books. A developer sees their own coin balance and match result through the
//     wallet and match APIs, which is the authoritative surface for it; the ledger's
//     internal postings are not theirs to read.
//   - model_call_completed / gateway metering — carries cost and provenance used for
//     anti-cheat. Exposing exactly what the platform can and cannot observe about
//     metering tells someone how to shape traffic around it.
//   - log records — arbitrary server log lines, which routinely mention other tenants.
//   - anything new, until someone classifies it.
func DevVisible(eventType string) bool { return devVisibleEventTypes[eventType] }

// DevVisibleEventTypes returns the allowlist as a slice, for building a SQL IN clause
// without the caller re-deriving (and drifting from) the policy.
func DevVisibleEventTypes() []string {
	out := make([]string, 0, len(devVisibleEventTypes))
	for k := range devVisibleEventTypes {
		out = append(out, k)
	}
	return out
}
