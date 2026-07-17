package schema

import "testing"

func TestNormalize_LogRecordWithoutTraceIDGetsSelfID(t *testing.T) {
	e := &TelemetryEvent{EventType: "log_record", StepName: "boot"}
	if err := NormalizeAndValidate(e, "proj", "dev", 0); err != nil {
		t.Fatalf("log_record without trace_id should be accepted, got: %v", err)
	}
	if e.TraceID == "" || e.TraceID != e.EventID {
		t.Errorf("trace_id should self-assign to event_id, got trace=%q event=%q", e.TraceID, e.EventID)
	}
	// A span still requires correlation.
	s := &TelemetryEvent{EventType: "span_completed"}
	if err := NormalizeAndValidate(s, "proj", "dev", 0); err == nil {
		t.Error("span_completed without trace_id must still be rejected")
	}
}
