package telemetry

import "time"

// A DECISION is the atomic unit of agent behaviour: the platform asked an agent for a
// move and it answered (or failed to). Everything an operator wants to know about an
// agent — is it legal, is it fast, is it expensive, does it time out — is an aggregate
// over decisions.
//
// Until now a decision existed only inside an in-process benchmark Recorder that was
// flushed ONCE at match end, into a JSON blob capped at 256 moves. Two consequences:
// if the arena died mid-match every decision in it was lost, and a long match silently
// stopped recording after move 256. Neither is acceptable for the record that settles
// a dispute about a staked game.
//
// Emitting here, at the moment the decision resolves, makes it durable independently
// of the match completing, and keeps the per-move detail as typed columns rather than
// buried in a blob. The match-end benchmark summary still ships — this is the
// per-event stream beside it, not a replacement.
const EventAgentDecision = "agent_decision"

// DecisionEvent is one resolved agent turn.
type DecisionEvent struct {
	Game    string
	MatchID string
	AgentID string
	Seat    int
	Round   int
	// Action is the move actually applied (which is the FALLBACK when the agent timed
	// out or answered illegally — see Outcome).
	Action string
	// Outcome: ok | illegal | timeout | transport | error. This is the field that
	// separates "the agent played" from "the platform played for it".
	Outcome   string
	LatencyMS int64
	// Rationale is the agent's own reasoning, when it supplied any.
	Rationale        string
	Provider         string
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	EstimatedCost    float64
	// MeterSource records provenance: "gateway" (server-observed, unfakeable) vs "sdk"
	// (agent self-reported). Without it a self-reported zero-cost run is
	// indistinguishable from a genuinely cheap one.
	MeterSource string
}

// EmitAgentDecision records one resolved turn. Safe on a nil/disabled client.
func (c *Client) EmitAgentDecision(ev DecisionEvent) {
	if !c.Enabled() {
		return
	}
	status := "ok"
	if ev.Outcome != "" && ev.Outcome != "ok" {
		// A fallback move is a FAILED decision even though the match advanced happily.
		// Reporting it as ok is why illegal moves used to look healthy in the UI.
		status = "error"
	}
	payload := map[string]any{
		"seat":    ev.Seat,
		"round":   ev.Round,
		"action":  ev.Action,
		"outcome": ev.Outcome,
	}
	if ev.Rationale != "" {
		payload["rationale"] = ev.Rationale
	}
	c.EmitEvent(Event{
		// Same trace id as the rest of the match, so decisions, chat and lifecycle all
		// land in one waterfall.
		TraceID:          MatchTraceID(ev.MatchID),
		EventType:        EventAgentDecision,
		EventTime:        time.Now().UTC(),
		Status:           status,
		StepName:         "agent.decision",
		SpanType:         "decision",
		Operation:        "decide",
		ActorID:          ev.AgentID,
		SessionID:        ev.Game,
		RunID:            ev.MatchID,
		Provider:         ev.Provider,
		Model:            ev.Model,
		LatencyMS:        ev.LatencyMS,
		PromptTokens:     ev.PromptTokens,
		CompletionTokens: ev.CompletionTokens,
		TotalTokens:      ev.TotalTokens,
		EstimatedCost:    ev.EstimatedCost,
		MeterSource:      ev.MeterSource,
		ErrorType:        ev.Outcome,
		PayloadJSON:      payload,
	})
}
