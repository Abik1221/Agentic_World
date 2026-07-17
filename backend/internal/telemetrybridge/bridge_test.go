package telemetrybridge

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

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/platform/telemetry"
)

type capture struct {
	mu  sync.Mutex
	evs []telemetry.Event
}

func newCapture() (*capture, *httptest.Server) {
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []telemetry.Event `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.evs = append(c.evs, body.Events...)
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	return c, srv
}

func TestBridge_MatchLifecycleBecomesTrace(t *testing.T) {
	cap, srv := newCapture()
	defer srv.Close()
	em := telemetry.New(telemetry.Config{
		Enabled: true, Endpoint: srv.URL, APIKey: "k",
		FlushInterval: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b := New(em)

	started, _ := json.Marshal(map[string]any{"match_id": "m9", "game": "goofspiel", "bid": 100})
	finished, _ := json.Marshal(map[string]any{"match_id": "m9", "game": "goofspiel", "winner_agent": "ag1"})

	_ = b.handle(context.Background(), events.Event{ID: "e1", Type: events.TypeMatchStarted, Payload: started, CreatedAt: time.Now()})
	_ = b.handle(context.Background(), events.Event{ID: "e2", Type: events.TypeMatchFinished, Payload: finished, CreatedAt: time.Now()})
	_ = em.Shutdown(context.Background())

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(cap.evs))
	}
	want := telemetry.MatchTraceID("m9")
	var sawStart, sawEnd bool
	for _, e := range cap.evs {
		if e.TraceID != want {
			t.Errorf("trace_id=%q want %q", e.TraceID, want)
		}
		if e.RunID != "m9" {
			t.Errorf("run_id=%q want m9", e.RunID)
		}
		switch e.EventType {
		case telemetry.EventTraceStarted:
			sawStart = true
			if e.EventID != "e1" {
				t.Errorf("started event_id=%q want e1 (idempotency)", e.EventID)
			}
		case telemetry.EventTraceCompleted:
			sawEnd = true
			if e.ActorID != "ag1" {
				t.Errorf("winner actor_id=%q want ag1", e.ActorID)
			}
		}
	}
	if !sawStart || !sawEnd {
		t.Errorf("missing trace lifecycle: start=%v end=%v", sawStart, sawEnd)
	}
}

func TestBridge_MatchBenchmarkFansOutPerSeat(t *testing.T) {
	cap, srv := newCapture()
	defer srv.Close()
	em := telemetry.New(telemetry.Config{
		Enabled: true, Endpoint: srv.URL, APIKey: "k", FlushInterval: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b := New(em)

	// The exact payload shape produced by benchmark.Payload (match.benchmark).
	payload, _ := json.Marshal(map[string]any{
		"game": "goofspiel", "match_id": "m9", "mode": "ranked",
		"seats": []map[string]any{
			{"seat": 0, "agent_id": "ag0", "decisions": 3, "legal": 3, "fallbacks": 0},
			{"seat": 1, "agent_id": "ag1", "decisions": 3, "legal": 1, "timeouts": 2, "fallbacks": 2},
		},
	})
	_ = b.handleBenchmark(context.Background(), events.Event{ID: "e1", Type: events.TypeMatchBenchmark, Payload: payload})
	_ = em.Shutdown(context.Background())

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.evs) != 2 {
		t.Fatalf("want 2 benchmark_recorded events (one per seat), got %d", len(cap.evs))
	}
	for _, e := range cap.evs {
		if e.EventType != "benchmark_recorded" {
			t.Errorf("event_type=%q want benchmark_recorded", e.EventType)
		}
		if e.TraceID != telemetry.MatchTraceID("m9") || e.RunID != "m9" || e.SessionID != "goofspiel" {
			t.Errorf("correlation wrong: %+v", e)
		}
	}
}

func TestBridge_DisabledEmitterRegistersNothing(t *testing.T) {
	b := New(telemetry.New(telemetry.Config{Enabled: false}, nil))
	var registered int
	b.Register(func(string, events.Handler) { registered++ })
	if registered != 0 {
		t.Errorf("disabled bridge registered %d handlers, want 0", registered)
	}
}
