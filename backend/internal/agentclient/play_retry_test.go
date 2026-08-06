package agentclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPlay_RetriesBillTheDeveloperTwice pins down why a turn push must not retry.
//
// Every attempt carries a FRESH nonce and timestamp (see attempt), which is correct for
// replay defence and fatal for idempotency: the SDK's nonce dedupe cannot recognise the
// second attempt as the same turn, so a real agent runs inference again and the developer
// is billed again. A verification probe may retry — /health is free. A turn may not.
//
// This test asserts the mechanism, not a preference: with Retries=2 the endpoint really is
// asked three times for one turn.
func TestPlay_RetriesBillTheDeveloperTwice(t *testing.T) {
	var calls int32
	var nonces sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		nonces.Store(r.Header.Get("X-Arena-Request-Id"), true)
		// Never answer in time: force the per-attempt deadline to expire.
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"card":1}`))
	}))
	defer srv.Close()

	c := New(Config{
		Timeout: 50 * time.Millisecond, MaxTimeout: 50 * time.Millisecond,
		Retries: 2, AllowPrivate: true,
	})
	var out struct {
		Card int `json:"card"`
	}
	if _, err := c.Play(context.Background(), Target{EndpointURL: srv.URL}, map[string]int{"round": 1}, &out); err == nil {
		t.Fatal("expected the timeout to surface as an error")
	}

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("endpoint called %d times, want 3 (1 + Retries=2)", got)
	}
	distinct := 0
	nonces.Range(func(_, _ any) bool { distinct++; return true })
	if distinct != 3 {
		t.Fatalf("saw %d distinct nonces across 3 attempts, want 3 — if attempts shared a nonce "+
			"an SDK could dedupe them and the billing concern would not apply", distinct)
	}
}

// TestPlay_NoRetryWhenRetriesZero is the regression guard for the live-play wiring.
//
// cmd/server builds a dedicated play client per game (Retries=0, deadline = that game's
// shot clock) instead of reusing the manifest verification probe (5s, 2 retries). Reusing
// the probe capped every decision at ~15s regardless of MOVE_WINDOW_SECONDS and pushed
// each turn three times. This asserts the half that a config change can silently undo.
func TestPlay_NoRetryWhenRetriesZero(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"card":1}`))
	}))
	defer srv.Close()

	c := New(Config{
		Timeout: 50 * time.Millisecond, MaxTimeout: 50 * time.Millisecond,
		Retries: 0, AllowPrivate: true,
	})
	var out struct {
		Card int `json:"card"`
	}
	if _, err := c.Play(context.Background(), Target{EndpointURL: srv.URL}, map[string]int{"round": 1}, &out); err == nil {
		t.Fatal("expected the timeout to surface as an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("endpoint called %d times for ONE turn, want exactly 1 — a retried turn is "+
			"duplicate inference the developer pays for", got)
	}
}

// TestPlay_HonoursMoveWindowDeadline is the other half of the wiring: a per-attempt
// deadline generous enough for a reasoning model. An agent that thinks for 200ms must
// succeed under a 5s window — under the old 5s-probe-with-retries it still would, but
// under any window shorter than the model's latency the platform silently substitutes a
// fallback move, which the ranked integrity gate then reads as "not LLM-backed".
func TestPlay_HonoursMoveWindowDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond) // a model that thinks
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"card":7}`))
	}))
	defer srv.Close()

	c := New(Config{
		Timeout: 5 * time.Second, MaxTimeout: 5 * time.Second,
		Retries: 0, AllowPrivate: true,
	})
	var out struct {
		Card int `json:"card"`
	}
	status, err := c.Play(context.Background(), Target{EndpointURL: srv.URL}, map[string]int{"round": 1}, &out)
	if err != nil {
		t.Fatalf("Play: %v (status %d)", err, status)
	}
	if out.Card != 7 {
		t.Fatalf("card=%d want 7 — the agent's real move must survive, not a fallback", out.Card)
	}
}
