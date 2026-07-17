// Package telemetrybridge projects the platform's domain-event outbox
// (internal/events) into Pyyol Lens telemetry. It is registered as a set of
// idempotent outbox handlers, exactly like badges/notifications/analytics — so
// EVERY match, across every game, produces a trace the instant it starts and
// finishes, with zero changes to the game services themselves.
//
// It lives in internal/ (not platform/) because it bridges two internal
// packages; telemetry itself stays a dependency-free leaf.
package telemetrybridge

import (
	"context"
	"encoding/json"

	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/platform/telemetry"
)

// Bridge maps domain events to telemetry events.
type Bridge struct {
	em *telemetry.Client
}

// New returns a bridge over em. A nil/disabled em makes every handler a no-op.
func New(em *telemetry.Client) *Bridge { return &Bridge{em: em} }

// Register wires the bridge onto the event bus. `on` is typically eventBus.On.
// Idempotent: telemetry events reuse the domain event id, so at-least-once
// delivery dedupes at the Lens ingest.
func (b *Bridge) Register(on func(eventType string, h events.Handler)) {
	if b == nil || !b.em.Enabled() {
		return
	}
	on(events.TypeMatchStarted, b.handle)
	on(events.TypeMatchFinished, b.handle)
	// Benchmark facts fan out to one Lens event PER SEAT, so they get a dedicated
	// handler rather than the generic single-event projection.
	on(events.TypeMatchBenchmark, b.handleBenchmark)
	on(events.TypeAgentCertified, b.handle)
	on(events.TypeRatingUpdated, b.handle)
	on(events.TypePIndexUpdated, b.handle)
	on(events.TypeSeasonRolled, b.handle)
	on(events.TypeBadgeAwarded, b.handle)
	on(events.TypeDisputeOpened, b.handle)
	on(events.TypeWithdrawalRequested, b.handle)
}

// handle is the single idempotent projector. It never returns an error: a
// telemetry mapping failure must not wedge the outbox (the fact is already
// persisted; observability is best-effort).
func (b *Bridge) handle(_ context.Context, e events.Event) error {
	if b == nil || !b.em.Enabled() {
		return nil
	}
	var p map[string]any
	if len(e.Payload) > 0 {
		_ = json.Unmarshal(e.Payload, &p)
	}
	matchID := strField(p, "match_id")
	game := strField(p, "game")

	ev := telemetry.Event{
		EventID:     e.ID, // domain event id ⇒ ingest dedupe on re-delivery
		EventType:   telemetry.EventSpanCompleted,
		Status:      "ok",
		StepName:    e.Type,
		SpanType:    "domain_event",
		Operation:   e.Type,
		RunID:       matchID,
		SessionID:   game,
		PayloadJSON: p,
	}
	if !e.CreatedAt.IsZero() {
		ev.EventTime = e.CreatedAt.UTC()
	}
	if matchID != "" {
		ev.TraceID = telemetry.MatchTraceID(matchID)
	}

	switch e.Type {
	case events.TypeMatchStarted:
		ev.EventType = telemetry.EventTraceStarted
		ev.SpanType = "match"
	case events.TypeMatchFinished:
		ev.EventType = telemetry.EventTraceCompleted
		ev.SpanType = "match"
		if w := strField(p, "winner_agent"); w != "" {
			ev.ActorID = w
		}
	case events.TypeDisputeOpened:
		ev.Status = "error"
		ev.EventType = telemetry.EventSpanFailed
	}
	if ev.TraceID == "" {
		// Non-match facts (season/badge/pindex) get their own single-span trace so
		// they remain queryable in the Lens without a parent match.
		ev.TraceID = "evt_" + e.ID
	}
	b.em.EmitEvent(ev)
	return nil
}

// handleBenchmark projects a durable match.benchmark fact into one
// benchmark_recorded Lens event per seat. Never errors (best-effort projection).
func (b *Bridge) handleBenchmark(_ context.Context, e events.Event) error {
	if b == nil || !b.em.Enabled() {
		return nil
	}
	benchmark.EmitFromPayload(b.em, e.Payload)
	return nil
}

func strField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}
