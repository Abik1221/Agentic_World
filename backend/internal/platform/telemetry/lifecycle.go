package telemetry

import "time"

// Agent LIFECYCLE events — the things that happen to an agent outside a single turn.
//
// These were the largest remaining blind spot. Socket connect/disconnect existed only
// as log.Info lines, and the Lens log tee defaults to WARN, so they were dropped
// before they ever left the process. The practical effect: the two questions an
// operator asks first during an incident — "was the agent even connected?" and "when
// did it drop?" — were unanswerable from the observability product built to answer
// them.
//
// Unlike a decision or a chat line, most of these are NOT scoped to a match (an agent
// connects once and plays many), so they carry an agent-scoped trace id instead. That
// keeps a match waterfall clean while still making the agent's own history queryable.
const (
	EventAgentConnected    = "agent_connected"
	EventAgentDisconnected = "agent_disconnected"
	EventAgentRegistered   = "agent_registered"
	// EventAgentEndpointVerified / Failed: a FAILED verification is the more
	// interesting of the two and previously produced no record at all — a developer
	// whose endpoint never passed had nothing to look at.
	EventAgentEndpointVerified = "agent_endpoint_verified"
	EventAgentEndpointFailed   = "agent_endpoint_failed"
)

// AgentTraceID is the stable trace id for an agent's own lifecycle, mirroring
// MatchTraceID. Lifecycle events are long-lived and cross-match, so grouping them
// under the agent (rather than any one match) is what makes "show me this agent's
// history" a single trace lookup.
func AgentTraceID(agentPublicID string) string { return "agent_" + agentPublicID }

// LifecycleEvent describes one agent lifecycle transition.
type LifecycleEvent struct {
	AgentID string
	// Detail carries event-specific context (sdk version, close reason, endpoint host,
	// failure reason). Kept as a free map because the useful fields differ per event
	// and inventing a union type would be worse than the map.
	Detail map[string]any
	// Reason explains a failure/disconnect. Empty on a success.
	Reason string
}

// EmitAgentLifecycle records a lifecycle transition. Safe on a nil/disabled client.
func (c *Client) EmitAgentLifecycle(eventType string, ev LifecycleEvent) {
	if !c.Enabled() || ev.AgentID == "" {
		return
	}
	status := "ok"
	if eventType == EventAgentEndpointFailed {
		status = "error"
	}
	payload := map[string]any{}
	for k, v := range ev.Detail {
		payload[k] = v
	}
	if ev.Reason != "" {
		payload["reason"] = ev.Reason
	}
	c.EmitEvent(Event{
		TraceID:     AgentTraceID(ev.AgentID),
		EventType:   eventType,
		EventTime:   time.Now().UTC(),
		Status:      status,
		StepName:    "agent.lifecycle",
		SpanType:    "lifecycle",
		Operation:   eventType,
		ActorID:     ev.AgentID,
		ErrorType:   ev.Reason,
		PayloadJSON: payload,
	})
}
