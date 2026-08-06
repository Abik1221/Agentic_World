package agentclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Deadline propagation: the CALLER decides how long a turn may take, not the client.
//
// This is the standard contract for an RPC transport (gRPC, and every stack modelled on
// it): a deadline travels with the request, and the transport honours it. A client-owned
// timeout is a FALLBACK for callers that set none — never a ceiling that silently overrides
// one they did set.
//
// Getting this backwards is what made the adaptive-window work inert. The play clients were
// constructed once at startup with a fixed Timeout, and attempt() unconditionally applied
// it, so a caller computing a 2m22s window for a slow local model still had its turn cut at
// the configured constant. The window was correct, adaptive, tested — and could not reach
// the code that decides when to give up.
//
// These tests are written at the layer that actually bounds a pushed turn, which is the
// mistake being corrected: the previous round verified the layer that had been changed
// rather than the one that mattered.

func slowEndpoint(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"card":7}`))
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// THE regression. A caller that grants MORE time than the client's configured default must
// get it — that is the whole point of an adaptive window.
func TestCallerDeadlineCanExceedTheConfiguredTimeout(t *testing.T) {
	srv := slowEndpoint(t, 400*time.Millisecond)
	// Configured for a fast agent…
	c := New(Config{Timeout: 100 * time.Millisecond, MaxTimeout: 5 * time.Second, AllowPrivate: true})

	// …but this caller knows the agent is slow and grants it a second.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var out struct {
		Card int `json:"card"`
	}
	if _, err := c.Play(ctx, Target{EndpointURL: srv.URL}, map[string]int{"r": 1}, &out); err != nil {
		t.Fatalf("Play: %v — the client's 100ms default overrode the caller's 1s deadline, so "+
			"an adaptive window can never reach the code that decides when to give up", err)
	}
	if out.Card != 7 {
		t.Fatalf("card=%d want 7", out.Card)
	}
}

// A caller that grants LESS time must also be honoured: a tight deadline is a real
// instruction, not a suggestion the transport may extend.
func TestCallerDeadlineCanBeShorterThanTheConfiguredTimeout(t *testing.T) {
	srv := slowEndpoint(t, 2*time.Second)
	c := New(Config{Timeout: 10 * time.Second, MaxTimeout: 30 * time.Second, AllowPrivate: true})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.Play(ctx, Target{EndpointURL: srv.URL}, map[string]int{"r": 1}, nil); err == nil {
		t.Fatal("a 150ms deadline did not cut off a 2s endpoint")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v to honour a 150ms deadline; the configured 10s timeout won", elapsed)
	}
}

// A caller with NO deadline keeps the old behaviour exactly. This is what makes the change
// safe to land: every existing call site is unaffected until it opts in.
func TestNoCallerDeadlineFallsBackToTheConfiguredTimeout(t *testing.T) {
	srv := slowEndpoint(t, 2*time.Second)
	c := New(Config{Timeout: 120 * time.Millisecond, MaxTimeout: 5 * time.Second, AllowPrivate: true})

	start := time.Now()
	if _, err := c.Play(context.Background(), Target{EndpointURL: srv.URL}, map[string]int{"r": 1}, nil); err == nil {
		t.Fatal("no deadline anywhere and the call still succeeded against a 2s endpoint")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v; the configured 120ms fallback was not applied", elapsed)
	}
}

// MaxTimeout stays an absolute safety ceiling. A caller cannot hold a connection open
// forever by handing in an enormous deadline — a hung endpoint must not be able to pin a
// goroutine and a socket indefinitely just because someone miscomputed a window.
func TestMaxTimeoutStillBoundsAnAbsurdCallerDeadline(t *testing.T) {
	srv := slowEndpoint(t, 5*time.Second)
	c := New(Config{Timeout: time.Second, MaxTimeout: 200 * time.Millisecond, AllowPrivate: true})

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	start := time.Now()
	if _, err := c.Play(ctx, Target{EndpointURL: srv.URL}, map[string]int{"r": 1}, nil); err == nil {
		t.Fatal("an hour-long caller deadline was honoured past the safety ceiling")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %v; MaxTimeout no longer bounds a runaway caller deadline", elapsed)
	}
}
