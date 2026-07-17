package schema

import "testing"

func TestCoalescePhase10FromPayload_reducerName(t *testing.T) {
	e := &TelemetryEvent{
		EventType: "reducer_started",
		PayloadJSON: map[string]any{
			"reducer_name": "generic.reducer",
			"task_kind":    "reducer",
		},
	}
	coalescePhase10FromPayload(e)
	if e.ReducerName != "generic.reducer" {
		t.Fatalf("reducer_name: got %q", e.ReducerName)
	}
	if e.TaskKind != "reducer" {
		t.Fatalf("task_kind: got %q", e.TaskKind)
	}
}

func TestCoalescePhase10FromPayload_artifactByteSize(t *testing.T) {
	e := &TelemetryEvent{
		EventType: "artifact_written",
		PayloadJSON: map[string]any{
			"byte_size": float64(12345),
		},
	}
	coalescePhase10FromPayload(e)
	if e.OutputBytes != 12345 {
		t.Fatalf("output_bytes: got %d", e.OutputBytes)
	}
}

func TestNormalizeAndValidate_outputQualityEvents(t *testing.T) {
	for _, typ := range []string{
		"output_contract_pass",
		"output_contract_fail",
		"output_evaluator_started",
		"output_evaluator_completed",
	} {
		t.Run(typ, func(t *testing.T) {
			e := &TelemetryEvent{
				EventType: typ,
				TraceID:   "tr_test",
				RequestID: "tr_test",
				RunID:     "tr_test",
				PayloadJSON: map[string]any{
					"ok":               true,
					"contract_version": "2026-05-13",
					"issues":           []any{},
				},
			}
			if err := NormalizeAndValidate(e, "p", "e", 0); err != nil {
				t.Fatal(err)
			}
		})
	}
}
