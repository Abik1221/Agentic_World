package agentwire

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
)

// fakeClient records the HTTP calls the transport makes.
type fakeClient struct {
	mu       sync.Mutex
	played   any
	moveOut  map[string]any
	events   int
	gameEnds int
	initReq  agentclient.InitializeRequest
}

func (f *fakeClient) Play(_ context.Context, _ agentclient.Target, req, out any) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.played = req
	if m, ok := out.(*map[string]any); ok {
		*m = f.moveOut
	}
	return 200, nil
}
func (f *fakeClient) Initialize(_ context.Context, _ agentclient.Target, r agentclient.InitializeRequest) (agentclient.InitializeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initReq = r
	return agentclient.InitializeResponse{Ready: true}, nil
}
func (f *fakeClient) Event(_ context.Context, _ agentclient.Target, _ agentclient.EventNotification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events++
	return nil
}
func (f *fakeClient) GameEnd(_ context.Context, _ agentclient.Target, _ agentclient.GameEndNotification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gameEnds++
	return nil
}

// fakeEnqueuer records durable webhook enqueues.
type fakeEnqueuer struct {
	events   int
	gameEnds int
	fail     bool
}

func (f *fakeEnqueuer) EnqueueEvent(_ context.Context, _, _, _ string, _ int, _ string, _ []byte) error {
	if f.fail {
		return errors.New("enqueue failed")
	}
	f.events++
	return nil
}
func (f *fakeEnqueuer) EnqueueGameEnd(_ context.Context, _, _, _ string, _ []byte) error {
	if f.fail {
		return errors.New("enqueue failed")
	}
	f.gameEnds++
	return nil
}

func TestHTTPTransportTurnCallsPlay(t *testing.T) {
	fc := &fakeClient{moveOut: map[string]any{"card": 7.0}}
	tr := HTTPTransport{Client: fc, Game: "goofspiel", AgentID: "ag"}
	var out map[string]any
	if err := tr.Turn(context.Background(), map[string]any{"round": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if out["card"] != 7.0 {
		t.Fatalf("move not decoded: %+v", out)
	}
	if tr.Socket() {
		t.Fatal("HTTP transport reported Socket()=true")
	}
}

func TestHTTPTransportEventPrefersDurableQueue(t *testing.T) {
	fc := &fakeClient{}
	fq := &fakeEnqueuer{}
	tr := HTTPTransport{Client: fc, Enqueue: fq, Game: "goofspiel", AgentID: "ag"}
	if err := tr.Event(context.Background(), "m1", 1, "round_revealed", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := tr.GameEnd(context.Background(), "m1", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if fq.events != 1 || fq.gameEnds != 1 {
		t.Fatalf("durable queue not used: events=%d gameEnds=%d", fq.events, fq.gameEnds)
	}
	// The direct client must NOT be used when a durable queue is present.
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.events != 0 || fc.gameEnds != 0 {
		t.Fatalf("client used despite durable queue: events=%d gameEnds=%d", fc.events, fc.gameEnds)
	}
}

func TestHTTPTransportEventInlineFallback(t *testing.T) {
	fc := &fakeClient{}
	tr := HTTPTransport{Client: fc, Game: "goofspiel", AgentID: "ag"} // no enqueuer
	_ = tr.Event(context.Background(), "m1", 1, "x", []byte(`{}`))
	_ = tr.GameEnd(context.Background(), "m1", []byte(`{}`))
	// Delivery is a detached goroutine; poll briefly.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		ok := fc.events == 1 && fc.gameEnds == 1
		fc.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("inline fallback did not deliver: events=%d gameEnds=%d", fc.events, fc.gameEnds)
}
