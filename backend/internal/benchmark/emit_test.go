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
