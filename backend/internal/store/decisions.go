package store

import (
	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/pricing"
)

// DecisionsFromSeat projects a seat's in-memory decision log onto the persisted per-round
// record.
//
// Per-move provider/model fall back to the seat's resolved attribution: the SDK reports
// usage per call, but a move made without an LLM call (a cached or rule-based turn)
// carries none, and leaving those blank would make the trace look as though the agent
// switched models mid-match.
func DecisionsFromSeat(seat benchmark.SeatSummary, seatProvider, seatModel string) []MatchDecision {
	if len(seat.DecisionLog) == 0 {
		return nil
	}
	out := make([]MatchDecision, 0, len(seat.DecisionLog))
	for i, d := range seat.DecisionLog {
		md := MatchDecision{
			// seq is the log's own order, not the round: Mafia takes several actions in
			// one day, and arenas that do not number rounds report 0 for all of them.
			Seq: i, Round: d.Round, Action: d.Action, Outcome: d.Outcome,
			LatencyMS: d.LatencyMS, Rationale: d.Rationale,
			Provider: seatProvider, Model: seatModel,
			// Already JSON and already size-capped by the Recorder — this layer only
			// carries it, so the cap lives in exactly one place.
			InputJSON: d.Input, InputTruncated: d.InputTruncated,
			StartedAt: d.At,
		}
		if u := d.Usage; u != nil {
			if u.Provider != "" {
				md.Provider = u.Provider
			}
			if u.Model != "" {
				md.Model = u.Model
			}
			md.PromptTokens = u.PromptTokens
			md.CompletionTokens = u.CompletionTokens
			md.ReasoningTokens = u.ReasoningTokens
			md.CachedTokens = u.CachedTokens
			md.TotalTokens = u.TotalTokens
			if md.TotalTokens == 0 {
				md.TotalTokens = u.PromptTokens + u.CompletionTokens + u.ReasoningTokens
			}
			md.EstimatedCost = pricing.EstimateCost(md.Model, u.PromptTokens, u.CompletionTokens,
				u.CachedTokens, u.ReasoningTokens)
		}
		out = append(out, md)
	}
	return out
}
