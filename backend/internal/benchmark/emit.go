package benchmark

import (
	"context"
	"encoding/json"

	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/pricing"
)

// Emitter is the minimal telemetry sink Emit needs. *telemetry.Client satisfies it;
// tests pass a capturing fake. Kept small so emission logic is unit-testable without
// a live ingest server.
type Emitter interface {
	Enabled() bool
	EmitEvent(telemetry.Event)
}

// EventModelCallCompleted is the canonical Pyyol Lens event the cost analytics query
// filters on. The arena emits one per recorded move that reported token usage, so
// per-turn model/token/cost is first-class (not buried in a benchmark payload).
const EventModelCallCompleted = "model_call_completed"

// Persist durably appends a benchmark fact to the outbox (wired from main via
// store.InsertEvent). Nil ⇒ callers fall back to the direct emitter. Shared by
// every drive loop so match summaries take the same durable path.
type Persist func(ctx context.Context, eventType string, payload []byte) error

// Flush emits a recorder's per-match summary at match end: durably via the
// outbox when persist is set (crash-safe, at-least-once), else best-effort via
// the emitter. A match that never asked an agent to move emits nothing. Safe with
// nil persist AND nil/disabled em. Returns the persist error (nil otherwise) so
// the caller can log it.
func Flush(rec *Recorder, persist Persist, em *telemetry.Client, mode string) error {
	if rec == nil || rec.Empty() {
		return nil
	}
	summary := rec.Summary()
	if persist != nil {
		// context.Background(): the match ctx is usually cancelled at match end.
		return persist(context.Background(), "match.benchmark", Payload(summary, mode))
	}
	Emit(em, summary, mode)
	return nil
}

// EventBenchmarkRecorded is the canonical Pyyol Lens event type for a per-match,
// per-seat benchmark fact. Keep in sync with the Lens schema canonicalEventTypes.
const EventBenchmarkRecorded = "benchmark_recorded"

// Emit ships one benchmark_recorded event per seat, correlated to the match
// trace (trace_id = match_<id>), so the benchmark data lands in the same trace as
// the match's spans and is filterable on its own event type. mode is the match
// mode (ranked/sandbox/cert); it is attached for per-mode aggregation.
//
// A nil/disabled emitter makes this a no-op. Volume is one event per seat per
// match (not per turn), so this is safe to treat as must-keep telemetry.
func Emit(em Emitter, s MatchSummary, mode string) {
	if em == nil || !em.Enabled() {
		return
	}
	trace := telemetry.MatchTraceID(s.MatchID)
	for _, seat := range s.Seats {
		em.EmitEvent(telemetry.Event{
			TraceID:   trace,
			EventType: EventBenchmarkRecorded,
			Status:    seatStatus(seat),
			StepName:  "benchmark.match",
			SpanType:  "benchmark",
			Operation: "match_summary",
			ActorID:   seat.AgentID,
			RunID:     s.MatchID,
			SessionID: s.Game,
			Provider:  seat.Provider,
			Model:     seat.Model,
			LatencyMS: int64(seat.AvgLatencyMS()),
			// Surface token economics at the top level too, so Lens's token/cost
			// aggregation picks them up (0/omitted when no move reported usage).
			PromptTokens:     seat.PromptTokens,
			CompletionTokens: seat.CompletionTokens,
			CachedTokens:     seat.CachedTokens,
			ReasoningTokens:  seat.ReasoningTokens,
			TotalTokens:      seat.TotalTokens,
			EstimatedCost:    seat.EstimatedCost,
			PricingVersion:   seat.PricingVersion,
			Currency:         telemetry.CurrencyUSD,
			MeterSource:      telemetry.MeterSourceSDK, // seat aggregate of agent-reported usage
			PayloadJSON: map[string]any{
				"game":              s.Game,
				"mode":              mode,
				"seat":              seat.Seat,
				"agent_version":     seat.AgentVersion,
				"provider":          seat.Provider,
				"model":             seat.Model,
				"decisions":         seat.Decisions,
				"legal":             seat.Legal,
				"illegal":           seat.Illegal,
				"timeouts":          seat.Timeouts,
				"transport_errors":  seat.TransportErrors,
				"disconnects":       seat.Disconnects,
				"errors":            seat.Errors,
				"fallbacks":         seat.Fallbacks,
				"result":            string(seat.Result),
				"wins":              b2i(seat.Result == ResultWin),
				"losses":            b2i(seat.Result == ResultLoss),
				"draws":             b2i(seat.Result == ResultDraw),
				"legal_rate":        seat.LegalRate(),
				"fallback_rate":     seat.FallbackRate(),
				"latency_avg_ms":    seat.AvgLatencyMS(),
				"latency_sum_ms":    seat.LatencySumMS, // lets the Lens sum latency exactly across matches
				"latency_min_ms":    seat.LatencyMinMS,
				"latency_max_ms":    seat.LatencyMaxMS,
				"prompt_tokens":     seat.PromptTokens,
				"completion_tokens": seat.CompletionTokens,
				"reasoning_tokens":  seat.ReasoningTokens,
				"cached_tokens":     seat.CachedTokens,
				"total_tokens":      seat.TotalTokens,
				"estimated_cost":    seat.EstimatedCost,
				"pricing_version":   seat.PricingVersion,
				"decision_log":      seat.DecisionLog, // per-move action/outcome/latency/reasoning/tokens trail
			},
		})
		// Per-turn model_call_completed events: this is what the cost-analytics query
		// filters on, so without these the cost dashboard reports $0 for the arena.
		emitModelCalls(em, trace, s.MatchID, s.Game, mode, seat)
	}
}

// emitModelCalls emits one model_call_completed per recorded move that reported
// token usage, pricing each with the versioned table (real per-move model when the
// SDK reported it, else the seat's manifest model). High priority so cost facts are
// never sampled away. Bounded by the decision-log cap (maxDecisionLog) per seat.
func emitModelCalls(em Emitter, trace, matchID, game, mode string, seat SeatSummary) {
	for _, d := range seat.DecisionLog {
		u := d.Usage
		if u == nil {
			continue
		}
		if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.ReasoningTokens == 0 && u.Model == "" {
			continue // nothing billable/attributable
		}
		model := u.Model
		if model == "" {
			model = seat.Model
		}
		provider := u.Provider
		if provider == "" {
			provider = seat.Provider
		}
		cost := pricing.EstimateCost(model, u.PromptTokens, u.CompletionTokens,
			u.CachedTokens, u.CachedWriteTokens, u.ReasoningTokens)
		em.EmitEvent(telemetry.Event{
			TraceID:          trace,
			EventType:        EventModelCallCompleted,
			Status:           "ok",
			StepName:         "agent.model_call",
			SpanType:         "model_call",
			Operation:        "model_call",
			ActorID:          seat.AgentID,
			RunID:            matchID,
			SessionID:        game,
			Provider:         provider,
			Model:            model,
			PromptTokens:     int64(u.PromptTokens),
			CompletionTokens: int64(u.CompletionTokens),
			CachedTokens:     int64(u.CachedTokens),
			ReasoningTokens:  int64(u.ReasoningTokens),
			TotalTokens:      int64(u.total()),
			EstimatedCost:    cost,
			PricingVersion:   pricing.Version,
			Currency:         telemetry.CurrencyUSD,
			MeterSource:      telemetry.MeterSourceSDK, // agent self-reported (sandbox tier)
			Priority:         telemetry.PriorityHigh,
			PayloadJSON: map[string]any{
				"game":  game,
				"mode":  mode,
				"seat":  seat.Seat,
				"round": d.Round,
			},
		})
	}
}

// payloadDTO is the wire/outbox shape of a match benchmark summary (mode folded
// in). Kept internal; use Payload / EmitFromPayload.
type payloadDTO struct {
	Game    string        `json:"game"`
	MatchID string        `json:"match_id"`
	Mode    string        `json:"mode"`
	Seats   []SeatSummary `json:"seats"`
}

// Payload serializes a summary + mode for the durable outbox (match.benchmark).
// Never returns an error in practice (fixed struct); a marshal failure yields "{}".
func Payload(s MatchSummary, mode string) []byte {
	b, err := json.Marshal(payloadDTO{Game: s.Game, MatchID: s.MatchID, Mode: mode, Seats: s.Seats})
	if err != nil {
		return []byte("{}")
	}
	return b
}

// EmitFromPayload decodes a match.benchmark outbox payload and emits the per-seat
// benchmark_recorded events. Used by the telemetry bridge. Malformed payloads are
// ignored (the fact is already durably stored; projection is best-effort).
// DecodePayload restores a match.benchmark outbox payload to its MatchSummary +
// mode. Used by projections (e.g. the P-Index benchmark aggregate) that need the
// per-seat data, not just the telemetry fan-out.
func DecodePayload(raw []byte) (MatchSummary, string, error) {
	var dto payloadDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return MatchSummary{}, "", err
	}
	return MatchSummary{Game: dto.Game, MatchID: dto.MatchID, Seats: dto.Seats}, dto.Mode, nil
}

func EmitFromPayload(em *telemetry.Client, raw []byte) {
	if em == nil || !em.Enabled() {
		return
	}
	var dto payloadDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return
	}
	Emit(em, MatchSummary{Game: dto.Game, MatchID: dto.MatchID, Seats: dto.Seats}, dto.Mode)
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// seatStatus flags a seat as degraded when the engine had to substitute any move,
// so a benchmark query can filter unhealthy agents by status alone.
func seatStatus(s SeatSummary) string {
	if s.Fallbacks > 0 {
		return "error"
	}
	return "ok"
}
