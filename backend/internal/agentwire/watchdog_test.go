package agentwire

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
)

// watchClient answers Play slowly and lets a test flip the endpoint's health mid-turn.
type watchClient struct {
	playFor time.Duration
	healthy atomic.Bool
	probes  atomic.Int32
}

func (c *watchClient) Play(ctx context.Context, _ agentclient.Target, _, _ any) (int, error) {
	select {
	case <-time.After(c.playFor):
		return 200, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func (c *watchClient) Initialize(context.Context, agentclient.Target, agentclient.InitializeRequest) (agentclient.InitializeResponse, error) {
	return agentclient.InitializeResponse{}, nil
}
func (c *watchClient) Event(context.Context, agentclient.Target, agentclient.EventNotification) error {
	return nil
}
func (c *watchClient) GameEnd(context.Context, agentclient.Target, agentclient.GameEndNotification) error {
	return nil
}
func (c *watchClient) Health(context.Context, agentclient.Target) (agentclient.HealthResult, error) {
	c.probes.Add(1)
	if c.healthy.Load() {
		return agentclient.HealthResult{Status: 200}, nil
	}
	return agentclient.HealthResult{}, errors.New("connection refused")
}

func watchTransport(c *watchClient) HTTPTransport {
	return HTTPTransport{
		Client: c, Target: agentclient.Target{EndpointURL: "https://agent.example.com/turn"},
		AgentID: "ag_w", Game: "goofspiel", Log: slog.New(slog.DiscardHandler),
	}
}

// THE property that makes this safe to ship: a turn that finishes quickly is never probed
// at all. The watchdog must be invisible in the normal case — it exists for dead agents,
// not as a tax on healthy ones.
func TestFastTurnsAreNeverProbed(t *testing.T) {
	c := &watchClient{playFor: 50 * time.Millisecond}
	c.healthy.Store(true)
	if err := watchTransport(c).Turn(context.Background(), map[string]int{}, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if n := c.probes.Load(); n != 0 {
		t.Fatalf("a 50ms turn was probed %d times; the watchdog is taxing healthy agents", n)
	}
}

// The harm to avoid. A single-threaded agent — a beginner Flask handler with one worker —
// may genuinely be unable to answer /health while computing a move. Cancelling it on one
// missed probe would take a decision away from an agent doing nothing wrong.
//
// Verified structurally: it must take SEVERAL consecutive failures, spread over most of a
// minute, before the turn is ended.
func TestASingleMissedProbeNeverKillsATurn(t *testing.T) {
	if watchFailuresToGiveUp < 2 {
		t.Fatalf("watchFailuresToGiveUp is %d — one blocked /health would cancel a turn that "+
			"was going to succeed, punishing a simple single-threaded agent for being "+
			"unsophisticated rather than absent", watchFailuresToGiveUp)
	}
	// And the total silence required must be long enough to outlast a busy handler.
	total := watchGrace + time.Duration(watchFailuresToGiveUp)*watchEvery
	if total < 40*time.Second {
		t.Fatalf("the watchdog gives up after %v of silence; that is short enough to kill a "+
			"legitimately busy agent mid-decision", total)
	}
}

// A turn must never outlive its caller's deadline just because the watchdog is running.
func TestWatchdogDoesNotOutliveTheCallerDeadline(t *testing.T) {
	c := &watchClient{playFor: time.Hour}
	c.healthy.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := watchTransport(c).Turn(ctx, map[string]int{}, nil); err == nil {
		t.Fatal("an hour-long turn completed inside a 200ms deadline")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %v to honour a 200ms deadline; the watchdog goroutine is holding the turn", elapsed)
	}
}

// The watchdog must not leak a goroutine per turn. Every turn on the platform starts one,
// so a leak here is unbounded growth.
func TestWatchdogStopsWithTheTurn(t *testing.T) {
	c := &watchClient{playFor: 20 * time.Millisecond}
	c.healthy.Store(true)
	tr := watchTransport(c)
	for i := 0; i < 50; i++ {
		if err := tr.Turn(context.Background(), map[string]int{}, nil); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	// stopWatch() joins the goroutine before Turn returns, so by here all 50 are done.
	// A leak would show up as the probe counter still climbing after the loop.
	before := c.probes.Load()
	time.Sleep(80 * time.Millisecond)
	if after := c.probes.Load(); after != before {
		t.Fatalf("probes went from %d to %d after every turn finished — watchdog goroutines "+
			"are outliving their turns", before, after)
	}
}

// A verdict reported as "gone" must have been established by a probe, never assumed.
func TestGoneVerdictComesFromRealProbes(t *testing.T) {
	c := &watchClient{playFor: 10 * time.Millisecond}
	c.healthy.Store(false) // endpoint is down, but the turn succeeds before any probe
	if err := watchTransport(c).Turn(context.Background(), map[string]int{}, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := c.probes.Load(); got != 0 {
		t.Fatalf("probed %d times on a turn that completed in 10ms", got)
	}
}
