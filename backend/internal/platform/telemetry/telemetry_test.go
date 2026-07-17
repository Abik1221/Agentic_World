package telemetry

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
)

// capture is a fake Pyyol Lens ingest that records every batched event.
type capture struct {
	mu     sync.Mutex
	events []Event
	key    string
	hits   int
}

func newCapture() (*capture, *httptest.Server) {
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.hits++
		c.key = r.Header.Get("X-Pyyol-Key")
		var body struct {
			Events []Event `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.events = append(c.events, body.Events...)
		w.WriteHeader(http.StatusOK)
	}))
	return c, srv
}

func (c *capture) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

func testClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	return New(Config{
		Enabled: true, Endpoint: endpoint, APIKey: "test-key",
		Project: "pyyol-arena", Environment: "test",
		FlushInterval: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestEmit_DeliversBatchWithAuthAndDefaults(t *testing.T) {
	cap, srv := newCapture()
	defer srv.Close()
	c := testClient(t, srv.URL)

	c.EmitEvent(Event{TraceID: "t1", EventType: EventSpanCompleted, StepName: "x"})
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	evs := cap.snapshot()
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if cap.key != "test-key" {
		t.Errorf("X-Pyyol-Key = %q, want test-key", cap.key)
	}
	e := evs[0]
	if e.EventID == "" || e.SchemaVersion == "" || e.SourceService == "" || e.ProjectID != "pyyol-arena" {
		t.Errorf("defaults not applied: %+v", e)
	}
	if e.EventTime.IsZero() {
		t.Error("event_time not defaulted")
	}
}

func TestDisabled_IsNoOp(t *testing.T) {
	c := New(Config{Enabled: false}, nil)
	if c.Enabled() {
		t.Fatal("disabled client reports enabled")
	}
	c.EmitEvent(Event{TraceID: "t", EventType: EventSpanCompleted}) // must not panic
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStartSpan_NestsUnderMatchTrace(t *testing.T) {
	cap, srv := newCapture()
	defer srv.Close()
	c := testClient(t, srv.URL)

	ctx := context.Background()
	parentCtx, endParent := c.StartSpan(ctx, "match", Attrs{SpanType: "match", MatchID: "m123", Game: "goofspiel"})
	_, endChild := c.StartSpan(parentCtx, "agent.turn", Attrs{SpanType: "agent_call", AgentID: "ag1"})
	endChild(nil)
	endParent(nil)

	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	evs := cap.snapshot()
	// 2 spans × (started+completed) = 4 events.
	if len(evs) != 4 {
		t.Fatalf("want 4 events, got %d: %+v", len(evs), evs)
	}
	wantTrace := MatchTraceID("m123")
	var parentSpanID, childParent string
	for _, e := range evs {
		if e.TraceID != wantTrace {
			t.Errorf("event %s trace_id=%q want %q", e.StepName, e.TraceID, wantTrace)
		}
		if e.StepName == "match" && e.EventType == EventSpanStarted {
			parentSpanID = e.SpanID
		}
		if e.StepName == "agent.turn" && e.EventType == EventSpanStarted {
			childParent = e.ParentSpanID
			if e.ActorID != "ag1" {
				t.Errorf("child actor_id=%q want ag1", e.ActorID)
			}
			// game inherited from parent context
			if g, _ := e.PayloadJSON["game"].(string); g != "goofspiel" {
				t.Errorf("child did not inherit game: %v", e.PayloadJSON)
			}
		}
	}
	if parentSpanID == "" || childParent != parentSpanID {
		t.Errorf("child not nested under parent: parent=%q childParent=%q", parentSpanID, childParent)
	}
}

func TestStartSpan_FailureEmitsSpanFailed(t *testing.T) {
	cap, srv := newCapture()
	defer srv.Close()
	c := testClient(t, srv.URL)

	_, done := c.StartSpan(context.Background(), "agent.turn", Attrs{AgentID: "ag1", MatchID: "m1"})
	done(context.DeadlineExceeded)
	_ = c.Shutdown(context.Background())

	var sawFailed bool
	for _, e := range cap.snapshot() {
		if e.EventType == EventSpanFailed {
			sawFailed = true
			if e.Status != "error" || e.ErrorMessage == "" {
				t.Errorf("failed span missing status/error: %+v", e)
			}
		}
	}
	if !sawFailed {
		t.Error("no span_failed emitted on error")
	}
}

func TestSampling_AlwaysKeepsFailuresTerminalsAndBenchmarks(t *testing.T) {
	cap, srv := newCapture()
	defer srv.Close()
	// Rate ~0 → drop virtually all NORMAL-priority events, keep only must-keeps.
	c := New(Config{
		Enabled: true, Endpoint: srv.URL, APIKey: "k",
		FlushInterval: 10 * time.Millisecond, TraceSampleRate: 0.00001,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Normal span on a correlated trace → should be sampled away.
	c.EmitEvent(Event{TraceID: "t-normal", EventType: EventSpanCompleted, StepName: "boring"})
	// Must-keeps on the SAME kind of trace id → always survive.
	c.EmitEvent(Event{TraceID: "t-fail", EventType: EventSpanFailed, Status: "error", StepName: "boom"})
	c.EmitEvent(Event{TraceID: "t-life", EventType: EventTraceStarted, StepName: "match"})
	c.EmitEvent(Event{TraceID: "t-life", EventType: EventTraceCompleted, StepName: "match"})
	c.EmitEvent(Event{TraceID: "t-bench", EventType: "benchmark_recorded", StepName: "bench"})
	_ = c.Shutdown(context.Background())

	kinds := map[string]bool{}
	for _, e := range cap.snapshot() {
		kinds[e.EventType] = true
	}
	if kinds[EventSpanCompleted] {
		t.Error("normal span should have been sampled away")
	}
	for _, must := range []string{EventSpanFailed, EventTraceStarted, EventTraceCompleted, "benchmark_recorded"} {
		if !kinds[must] {
			t.Errorf("must-keep event %q was dropped by sampling", must)
		}
	}
	if c.Sampled() == 0 {
		t.Error("expected sampled counter > 0")
	}
}

func TestSampling_FullRateKeepsEverything(t *testing.T) {
	cap, srv := newCapture()
	defer srv.Close()
	c := New(Config{
		Enabled: true, Endpoint: srv.URL, APIKey: "k",
		FlushInterval: 10 * time.Millisecond, TraceSampleRate: 1.0,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := 0; i < 20; i++ {
		c.EmitEvent(Event{TraceID: "t", EventType: EventSpanCompleted})
	}
	_ = c.Shutdown(context.Background())
	if got := len(cap.snapshot()); got != 20 {
		t.Fatalf("full rate kept %d/20", got)
	}
	if c.Sampled() != 0 {
		t.Errorf("full rate sampled %d, want 0", c.Sampled())
	}
}

func TestSampling_WholeTraceKeptOrDroppedTogether(t *testing.T) {
	// For any given trace id, the keep decision must be identical across its
	// (non-failure) events so a waterfall is never half-sampled.
	c := New(Config{Enabled: true, Endpoint: "http://127.0.0.1:0", APIKey: "k", TraceSampleRate: 0.5}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = c.Shutdown(context.Background()) }()
	for _, id := range []string{"a", "b", "c", "trace-123", "trace-456"} {
		e1 := Event{TraceID: id, EventType: EventSpanStarted}
		e2 := Event{TraceID: id, EventType: EventSpanCompleted, Status: "ok"}
		if c.keep(&e1) != c.keep(&e2) {
			t.Errorf("trace %q: started/completed keep decisions differ", id)
		}
	}
}

func TestLogHandler_MirrorsAtOrAboveLevel(t *testing.T) {
	cap, srv := newCapture()
	defer srv.Close()
	c := testClient(t, srv.URL)

	base := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewLogHandler(base, c, slog.LevelWarn))

	logger.Info("ignored info")         // below threshold → not mirrored
	logger.Error("boom", "code", "E42") // mirrored
	_ = c.Shutdown(context.Background())

	var logs []Event
	for _, e := range cap.snapshot() {
		if e.EventType == EventLogRecord {
			logs = append(logs, e)
		}
	}
	if len(logs) != 1 {
		t.Fatalf("want 1 log_record, got %d", len(logs))
	}
	e := logs[0]
	if e.StepName != "boom" || e.Status != "error" {
		t.Errorf("log_record shape wrong: %+v", e)
	}
	if v, _ := e.PayloadJSON["code"].(string); v != "E42" {
		t.Errorf("log attr not captured: %v", e.PayloadJSON)
	}
}
