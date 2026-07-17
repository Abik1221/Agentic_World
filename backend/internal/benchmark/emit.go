package benchmark

import (
	"context"
	"encoding/json"

	"github.com/agent-arena/arena/internal/platform/telemetry"
)

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
func Emit(em *telemetry.Client, s MatchSummary, mode string) {
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
			PayloadJSON: map[string]any{
				"game":             s.Game,
				"mode":             mode,
				"seat":             seat.Seat,
				"agent_version":    seat.AgentVersion,
				"provider":         seat.Provider,
				"model":            seat.Model,
				"decisions":        seat.Decisions,
				"legal":            seat.Legal,
				"illegal":          seat.Illegal,
				"timeouts":         seat.Timeouts,
				"transport_errors": seat.TransportErrors,
				"disconnects":      seat.Disconnects,
				"errors":           seat.Errors,
				"fallbacks":        seat.Fallbacks,
				"result":           string(seat.Result),
				"wins":             b2i(seat.Result == ResultWin),
				"losses":           b2i(seat.Result == ResultLoss),
				"draws":            b2i(seat.Result == ResultDraw),
				"legal_rate":       seat.LegalRate(),
				"fallback_rate":    seat.FallbackRate(),
				"latency_avg_ms":   seat.AvgLatencyMS(),
				"latency_sum_ms":   seat.LatencySumMS, // lets the Lens sum latency exactly across matches
				"latency_min_ms":   seat.LatencyMinMS,
				"latency_max_ms":   seat.LatencyMaxMS,
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
