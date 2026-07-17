package agentgw

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

// A disconnected agent still produces a failed span at the gateway choke point,
// carrying the agent id, frame, and match correlation — proving the gateway →
// Pyyol Lens wire end to end.
func TestGateway_EmitsSpanForTurn(t *testing.T) {
	var mu sync.Mutex
	var events []telemetry.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Pyyol-Key"); got != "k" {
			t.Errorf("missing/bad X-Pyyol-Key: %q", got)
		}
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

	gw := New(nil, Options{Emitter: em}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// No socket registered → Turn errors, but the span must still be emitted.
	view := map[string]any{"match_id": "m7", "game": "goofspiel", "round": 3}
	var out map[string]any
	if err := gw.Turn(context.Background(), "ag1", view, &out); err == nil {
		t.Fatal("expected ErrNotConnected for a disconnected agent")
	}
	_ = em.Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	var found bool
	for _, e := range events {
		if e.StepName == "agent.turn" && e.EventType == telemetry.EventSpanFailed {
			found = true
			if e.ActorID != "ag1" {
				t.Errorf("actor_id=%q want ag1", e.ActorID)
			}
			if e.TraceID != telemetry.MatchTraceID("m7") {
				t.Errorf("trace_id=%q want %q", e.TraceID, telemetry.MatchTraceID("m7"))
			}
			if e.RunID != "m7" {
				t.Errorf("run_id=%q want m7", e.RunID)
			}
		}
	}
	if !found {
		t.Fatalf("no agent.turn span_failed emitted; got %d events", len(events))
	}
}
