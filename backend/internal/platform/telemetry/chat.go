package telemetry

import "time"

// Agent table talk is a first-class traced event.
//
// Chat was the single largest hole in agent observability: an agent could post
// hundreds of lines across a match and Lens recorded NOTHING — not the line, not the
// rejection, not even a count. That matters more here than in a normal product,
// because in Mafia the talking IS the game: the negotiation, the accusation and the
// bluff are the behaviour a spectator judges and an operator audits after a dispute.
//
// One helper shared by all three games so the event shape cannot drift between them —
// a per-game copy would guarantee three subtly different schemas within a release.
const (
	// EventAgentSaid is one line of public table talk that was accepted.
	EventAgentSaid = "agent_said"
	// EventAgentSayRejected is a line the rules refused (closed floor at night, a dead
	// seat, empty text). Rejections are traced too: "the agent tried to speak and was
	// silenced" is a different fact from "the agent stayed quiet", and only one of
	// them indicates a broken agent.
	EventAgentSayRejected = "agent_say_rejected"
)

// ChatEvent describes one attempted line of table talk.
type ChatEvent struct {
	Game    string // arena: mafia | monopoly | goofspiel
	MatchID string
	AgentID string
	Seat    int
	Kind    string // "say" | "rationale"
	Phase   string // game phase when spoken, when the game has phases
	Text    string
	// Reason is set only for a rejection (e.g. "closed_floor", "not_alive", "empty").
	Reason string
}

// EmitAgentSaid records an accepted line. Safe on a nil/disabled client.
func (c *Client) EmitAgentSaid(ev ChatEvent) {
	c.emitChat(EventAgentSaid, "ok", ev)
}

// EmitAgentSayRejected records a line the rules refused.
func (c *Client) EmitAgentSayRejected(ev ChatEvent) {
	c.emitChat(EventAgentSayRejected, "rejected", ev)
}

func (c *Client) emitChat(eventType, status string, ev ChatEvent) {
	if !c.Enabled() {
		return
	}
	payload := map[string]any{
		"seat": ev.Seat,
		"kind": ev.Kind,
		// The text itself: table talk is PUBLIC by definition (it is broadcast to every
		// spectator), so tracing it leaks nothing that is not already on the wire. It is
		// also the only way a post-match dispute can be reconstructed.
		"text":       ev.Text,
		"text_chars": len(ev.Text),
	}
	if ev.Phase != "" {
		payload["phase"] = ev.Phase
	}
	if ev.Reason != "" {
		payload["reason"] = ev.Reason
	}
	c.EmitEvent(Event{
		// Same trace id as every other event for this match, so a line lands in the
		// match's existing waterfall next to the move it argued for — rather than in a
		// disconnected chat stream nobody correlates.
		TraceID:     MatchTraceID(ev.MatchID),
		EventType:   eventType,
		EventTime:   time.Now().UTC(),
		Status:      status,
		StepName:    "agent.say",
		SpanType:    "chat",
		Operation:   "say",
		ActorID:     ev.AgentID,
		SessionID:   ev.Game,
		RunID:       ev.MatchID,
		PayloadJSON: payload,
	})
}
