package schema

import "testing"

// The arena is this store's primary producer, and its agent events must be accepted.
//
// They were not. Every one of them was rejected at ingest with 400 "unsupported
// event_type", the arena's emitter retried and dropped the batch, and events_raw never
// received a single agent decision, chat line or connect/disconnect. The read path,
// the query and the ownership gates were all correct and all read an empty table, so
// the failure surfaced to developers as a broken telemetry view with no cause they
// could see or fix.
//
// This test is the tripwire for that class of bug: it fails if a producer's event type
// is removed from the allowlist, which is otherwise a silent, total data loss that no
// unit test on either side of the wire can see.
func TestArenaAgentEventTypesAreAccepted(t *testing.T) {
	// Mirrors the arena's internal/platform/telemetry event-type constants, and is a
	// superset of its DevVisibleEventTypes allowlist (the developer-facing subset).
	arenaEmits := []string{
		"agent_decision",
		"agent_said",
		"agent_say_rejected",
		"agent_connected",
		"agent_disconnected",
		"agent_registered",
		"agent_endpoint_verified",
		"agent_endpoint_failed",
		// Already accepted; listed so this test covers the arena's whole vocabulary
		// rather than only the part that was broken.
		"benchmark_recorded",
		"trace_started",
		"trace_completed",
		"trace_failed",
		"span_started",
		"span_completed",
		"span_failed",
		"model_call_completed",
		"log_record",
	}
	for _, et := range arenaEmits {
		t.Run(et, func(t *testing.T) {
			e := TelemetryEvent{EventType: et, TraceID: "match_mch_1", ActorID: "agt_1"}
			if err := NormalizeAndValidate(&e, "pyyol-arena", "production", MaxTelemetryFreeText); err != nil {
				t.Fatalf("ingest rejects %q (%v) — the arena emits it, so every event of this "+
					"type is dropped and the trace view for it is permanently empty", et, err)
			}
		})
	}
}

// The allowlist is still an allowlist: an unknown type is refused rather than stored,
// so a typo in a producer is loud instead of quietly filling the table.
func TestUnknownEventTypeIsStillRejected(t *testing.T) {
	e := TelemetryEvent{EventType: "agent_desicion", TraceID: "t", ActorID: "a"} // typo on purpose
	if err := NormalizeAndValidate(&e, "pyyol-arena", "production", MaxTelemetryFreeText); err == nil {
		t.Fatal("a misspelled event type was accepted — the allowlist has stopped being one")
	}
}
