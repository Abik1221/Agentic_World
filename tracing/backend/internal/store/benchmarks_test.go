package store

import (
	"testing"
	"time"

	"github.com/agent-arena/pyyol-lens/backend/internal/schema"
)

func TestBenchmarkRowFromEvent(t *testing.T) {
	// Numbers arrive as float64 after JSON decode (NATS envelope), so mimic that.
	e := schema.TelemetryEvent{
		EventType:      "benchmark_recorded",
		ActorID:        "ag1",
		SessionID:      "goofspiel",
		OrganizationID: "org1",
		ProjectID:      "pyyol-arena",
		Environment:    "prod",
		EventTime:      time.Date(2026, 7, 16, 14, 30, 0, 0, time.UTC),
		PayloadJSON: map[string]any{
			"game":             "goofspiel",
			"mode":             "ranked",
			"decisions":        float64(13),
			"legal":            float64(11),
			"illegal":          float64(1),
			"timeouts":         float64(1),
			"transport_errors": float64(0),
			"disconnects":      float64(0),
			"errors":           float64(0),
			"fallbacks":        float64(2),
			"latency_sum_ms":   float64(2600),
			"wins":             float64(1),
			"losses":           float64(0),
			"draws":            float64(0),
			"agent_version":    "2.1.0",
			"provider":         "openai",
			"model":            "gpt-4o",
		},
	}
	row, ok := BenchmarkRowFromEvent(e)
	if !ok {
		t.Fatal("expected ok for benchmark_recorded")
	}
	if row.Provider != "openai" || row.Model != "gpt-4o" {
		t.Errorf("provider/model wrong: %+v", row)
	}
	if row.Wins != 1 || row.Losses != 0 || row.Draws != 0 {
		t.Errorf("win/loss/draw wrong: %+v", row)
	}
	if row.AgentVersion != "2.1.0" {
		t.Errorf("agent_version=%q want 2.1.0", row.AgentVersion)
	}
	if row.AgentID != "ag1" || row.Game != "goofspiel" || row.Mode != "ranked" {
		t.Errorf("identity wrong: %+v", row)
	}
	if row.OrganizationID != "org1" || row.Environment != "prod" {
		t.Errorf("scope wrong: %+v", row)
	}
	if row.Matches != 1 || row.Decisions != 13 || row.Legal != 11 || row.Fallbacks != 2 || row.LatencySumMS != 2600 {
		t.Errorf("counters wrong: %+v", row)
	}
	if !row.BucketStart.Equal(time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("bucket not day-truncated UTC: %v", row.BucketStart)
	}
}

func TestBenchmarkRowFromEvent_GameFallsBackToSession(t *testing.T) {
	e := schema.TelemetryEvent{
		EventType: "benchmark_recorded", ActorID: "ag1", SessionID: "mafia",
		PayloadJSON: map[string]any{"decisions": float64(4)}, // no "game" key
	}
	row, ok := BenchmarkRowFromEvent(e)
	if !ok || row.Game != "mafia" {
		t.Errorf("game should fall back to session_id: ok=%v game=%q", ok, row.Game)
	}
}

func TestBenchmarkRowFromEvent_RejectsOthers(t *testing.T) {
	for _, e := range []schema.TelemetryEvent{
		{EventType: "span_completed", ActorID: "ag1"},  // wrong type
		{EventType: "benchmark_recorded", ActorID: ""}, // no agent
	} {
		if _, ok := BenchmarkRowFromEvent(e); ok {
			t.Errorf("expected ok=false for %+v", e)
		}
	}
}

func TestPayloadInt_Coercions(t *testing.T) {
	p := map[string]any{"f": float64(7), "i": 9, "i64": int64(11), "s": "x"}
	if payloadInt(p, "f") != 7 || payloadInt(p, "i") != 9 || payloadInt(p, "i64") != 11 {
		t.Error("numeric coercion failed")
	}
	if payloadInt(p, "s") != 0 || payloadInt(p, "missing") != 0 || payloadInt(nil, "x") != 0 {
		t.Error("non-number/absent must be 0")
	}
}
