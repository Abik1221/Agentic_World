package benchmark

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platform/telemetry"
)

func TestEmit_OneBenchmarkEventPerSeat(t *testing.T) {
	var mu sync.Mutex
	var events []telemetry.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []telemetry.Event `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		events = append(events, body.Events...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	em := telemetry.New(telemetry.Config{
		Enabled: true, Endpoint: srv.URL, APIKey: "k", FlushInterval: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	r := NewRecorder("goofspiel", "m9")
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeOK, LatencyMS: 20})
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeOK, LatencyMS: 40})
	r.Record(Decision{Seat: 1, AgentID: "ag1", Outcome: OutcomeTimeout, LatencyMS: 9000})
	Emit(em, r.Summary(), "ranked")
	_ = em.Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("want 2 benchmark events, got %d", len(events))
	}
	byAgent := map[string]telemetry.Event{}
	for _, e := range events {
		if e.EventType != EventBenchmarkRecorded {
			t.Errorf("event_type=%q want %q", e.EventType, EventBenchmarkRecorded)
		}
		if e.TraceID != telemetry.MatchTraceID("m9") || e.RunID != "m9" || e.SessionID != "goofspiel" {
			t.Errorf("correlation wrong: %+v", e)
		}
		byAgent[e.ActorID] = e
	}

	ag0 := byAgent["ag0"]
	if ag0.Status != "ok" {
		t.Errorf("ag0 status=%q want ok (no fallbacks)", ag0.Status)
	}
	if ag0.LatencyMS != 30 { // avg of 20,40
		t.Errorf("ag0 latency_ms=%d want 30", ag0.LatencyMS)
	}
	if fr, _ := ag0.PayloadJSON["fallback_rate"].(float64); fr != 0 {
		t.Errorf("ag0 fallback_rate=%v want 0", fr)
	}

	ag1 := byAgent["ag1"]
	if ag1.Status != "error" {
		t.Errorf("ag1 status=%q want error (had a timeout)", ag1.Status)
	}
	if to, _ := ag1.PayloadJSON["timeouts"].(float64); to != 1 {
		t.Errorf("ag1 timeouts=%v want 1", to)
	}
	if fr, _ := ag1.PayloadJSON["fallback_rate"].(float64); fr != 1 {
		t.Errorf("ag1 fallback_rate=%v want 1", fr)
	}
}

func TestEmit_IncludesWinLossDraw(t *testing.T) {
	var mu sync.Mutex
	var events []telemetry.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []telemetry.Event `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		events = append(events, body.Events...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	em := telemetry.New(telemetry.Config{
		Enabled: true, Endpoint: srv.URL, APIKey: "k", FlushInterval: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	r := NewRecorder("goofspiel", "m1")
	r.Record(Decision{Seat: 0, AgentID: "winner", Outcome: OutcomeOK, LatencyMS: 5})
	r.Record(Decision{Seat: 1, AgentID: "loser", Outcome: OutcomeOK, LatencyMS: 5})
	r.SetResult(0, "winner", ResultWin)
	r.SetResult(1, "loser", ResultLoss)
	Emit(em, r.Summary(), "ranked")
	_ = em.Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	byAgent := map[string]telemetry.Event{}
	for _, e := range events {
		byAgent[e.ActorID] = e
	}
	if w, _ := byAgent["winner"].PayloadJSON["wins"].(float64); w != 1 {
		t.Errorf("winner wins=%v want 1", byAgent["winner"].PayloadJSON["wins"])
	}
	if l, _ := byAgent["loser"].PayloadJSON["losses"].(float64); l != 1 {
		t.Errorf("loser losses=%v want 1", byAgent["loser"].PayloadJSON["losses"])
	}
	if w, _ := byAgent["loser"].PayloadJSON["wins"].(float64); w != 0 {
		t.Errorf("loser wins=%v want 0", w)
	}
}

func TestFlush_DurablePathUsesPersist(t *testing.T) {
	var gotType string
	var gotPayload []byte
	persist := func(_ context.Context, eventType string, payload []byte) error {
		gotType, gotPayload = eventType, payload
		return nil
	}
	r := NewRecorder("mafia", "m1")
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeIllegal, LatencyMS: 5})

	if err := Flush(r, persist, nil, "practice"); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if gotType != "match.benchmark" {
		t.Errorf("event type = %q, want match.benchmark", gotType)
	}
	var dto payloadDTO
	if err := json.Unmarshal(gotPayload, &dto); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if dto.Game != "mafia" || dto.MatchID != "m1" || dto.Mode != "practice" || len(dto.Seats) != 1 {
		t.Errorf("payload wrong: %+v", dto)
	}
}

func TestFlush_EmptyRecorderNoOp(t *testing.T) {
	called := false
	persist := func(context.Context, string, []byte) error { called = true; return nil }
	if err := Flush(NewRecorder("goofspiel", "m2"), persist, nil, "ranked"); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("empty recorder must not persist")
	}
	// Also safe with nil recorder + nil persist + nil emitter.
	if err := Flush(nil, nil, nil, "ranked"); err != nil {
		t.Fatal(err)
	}
}

func TestEmit_DisabledEmitterNoPanic(t *testing.T) {
	em := telemetry.New(telemetry.Config{Enabled: false}, nil)
	Emit(em, MatchSummary{Game: "goofspiel", MatchID: "m", Seats: []SeatSummary{{Seat: 0}}}, "ranked")
	Emit(nil, MatchSummary{}, "ranked")
}

// captureEmitter records emitted events synchronously (no ingest server / flush
// timing), for asserting the cost + model_call_completed behavior deterministically.
type captureEmitter struct{ events []telemetry.Event }

func (c *captureEmitter) Enabled() bool               { return true }
func (c *captureEmitter) EmitEvent(e telemetry.Event) { c.events = append(c.events, e) }
func (c *captureEmitter) byType(t string) []telemetry.Event {
	var out []telemetry.Event
	for _, e := range c.events {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

const eps = 1e-9

func TestRecord_AccumulatesCostFromUsage(t *testing.T) {
	r := NewRecorder("goofspiel", "m1")
	r.SetAgentMeta(0, "ag0", AgentMeta{Provider: "openai", Model: "gpt-4o"})
	// No per-move model → priced with the seat's manifest model (gpt-4o).
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeOK, Round: 1,
		Usage: &TokenUsage{PromptTokens: 1000, CompletionTokens: 500}})
	s := r.Summary().Seats[0]
	want := (1000*2.50 + 500*10.00) / 1_000_000
	if abs(s.EstimatedCost-want) > eps {
		t.Errorf("seat cost = %v, want %v", s.EstimatedCost, want)
	}
	if s.PricingVersion == "" {
		t.Error("pricing_version must be stamped when usage is present")
	}
}

func TestEmit_ModelCallEventsWithCost(t *testing.T) {
	r := NewRecorder("goofspiel", "m7")
	r.SetAgentMeta(0, "ag0", AgentMeta{Provider: "openai", Model: "gpt-4o"})
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeOK, Round: 1,
		Usage: &TokenUsage{PromptTokens: 1000, CompletionTokens: 500}}) // seat model gpt-4o
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeOK, Round: 2,
		Usage: &TokenUsage{PromptTokens: 100, CompletionTokens: 10, Model: "claude-sonnet-4-5", Provider: "anthropic"}})

	cap := &captureEmitter{}
	Emit(cap, r.Summary(), "ranked")

	// benchmark_recorded carries seat cost + pricing_version.
	bench := cap.byType(EventBenchmarkRecorded)
	if len(bench) != 1 {
		t.Fatalf("want 1 benchmark event, got %d", len(bench))
	}
	if bench[0].EstimatedCost <= 0 {
		t.Errorf("benchmark estimated_cost = %v, want > 0", bench[0].EstimatedCost)
	}
	if pv, _ := bench[0].PayloadJSON["pricing_version"].(string); pv == "" {
		t.Error("benchmark payload missing pricing_version")
	}

	// One model_call_completed per decision with usage, priced per-move.
	calls := cap.byType(EventModelCallCompleted)
	if len(calls) != 2 {
		t.Fatalf("want 2 model_call_completed events, got %d", len(calls))
	}
	byModel := map[string]telemetry.Event{}
	for _, e := range calls {
		if e.TraceID != telemetry.MatchTraceID("m7") || e.RunID != "m7" || e.SessionID != "goofspiel" {
			t.Errorf("model_call correlation wrong: %+v", e)
		}
		if e.Priority != telemetry.PriorityHigh {
			t.Errorf("model_call must be high-priority (never sampled), got %v", e.Priority)
		}
		byModel[e.Model] = e
	}

	gpt := byModel["gpt-4o"]
	if wantGpt := (1000*2.50 + 500*10.00) / 1_000_000; abs(gpt.EstimatedCost-wantGpt) > eps {
		t.Errorf("gpt-4o cost = %v, want %v", gpt.EstimatedCost, wantGpt)
	}
	if gpt.Provider != "openai" {
		t.Errorf("gpt-4o provider = %q, want openai (from seat)", gpt.Provider)
	}
	claude := byModel["claude-sonnet-4-5"]
	if wantClaude := (100*3.00 + 10*15.00) / 1_000_000; abs(claude.EstimatedCost-wantClaude) > eps {
		t.Errorf("claude cost = %v, want %v", claude.EstimatedCost, wantClaude)
	}
	if claude.Provider != "anthropic" {
		t.Errorf("claude provider = %q, want anthropic (per-move override)", claude.Provider)
	}
	if rd, _ := claude.PayloadJSON["round"].(int); rd != 2 {
		t.Errorf("claude round = %v, want 2", claude.PayloadJSON["round"])
	}
}

func TestEmit_NoModelCallWhenNoUsage(t *testing.T) {
	r := NewRecorder("goofspiel", "m8")
	r.SetAgentMeta(0, "ag0", AgentMeta{Model: "gpt-4o"})
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeOK, Round: 1}) // no usage
	cap := &captureEmitter{}
	Emit(cap, r.Summary(), "ranked")

	if n := len(cap.byType(EventModelCallCompleted)); n != 0 {
		t.Errorf("want 0 model_call events without usage, got %d", n)
	}
	if n := len(cap.byType(EventBenchmarkRecorded)); n != 1 {
		t.Errorf("want 1 benchmark event, got %d", n)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
