package agentwire

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
)

// probeClient records whether Health was asked, and answers however the test wants.
type probeClient struct {
	playErr      error
	healthStatus int
	healthErr    error
	plays        int32
	healths      int32
}

func (c *probeClient) Play(context.Context, agentclient.Target, any, any) (int, error) {
	atomic.AddInt32(&c.plays, 1)
	if c.playErr != nil {
		return 0, c.playErr
	}
	return 200, nil
}
func (c *probeClient) Initialize(context.Context, agentclient.Target, agentclient.InitializeRequest) (agentclient.InitializeResponse, error) {
	return agentclient.InitializeResponse{}, nil
}
func (c *probeClient) Event(context.Context, agentclient.Target, agentclient.EventNotification) error {
	return nil
}
func (c *probeClient) GameEnd(context.Context, agentclient.Target, agentclient.GameEndNotification) error {
	return nil
}
func (c *probeClient) Health(context.Context, agentclient.Target) (agentclient.HealthResult, error) {
	atomic.AddInt32(&c.healths, 1)
	return agentclient.HealthResult{Status: c.healthStatus}, c.healthErr
}

func probeTransport(c *probeClient, game string) HTTPTransport {
	return HTTPTransport{
		Client: c, Target: agentclient.Target{EndpointURL: "https://agent.example.com/turn"},
		AgentID: "ag_test", Game: game, Log: slog.New(slog.DiscardHandler),
	}
}

// THE regression this file exists for.
//
// The reachability guarantee — "we try to reach the agent before absence can cost it a
// stake" — was originally wired into the SANDBOX driver only. Sandbox is unstaked, so the
// check existed exactly where it could never matter, while Mafia, Monopoly and the
// Goofspiel ranked drive forfeited real money with no probe at all.
//
// The fix was to move it into the transport every hosted-endpoint path shares. This test
// pins that down per game, so wiring a new staked game cannot quietly skip it.
func TestEveryStakedGamePathProbesOnAMissedTurn(t *testing.T) {
	for _, game := range []string{"goofspiel", "mafia", "monopoly"} {
		t.Run(game, func(t *testing.T) {
			c := &probeClient{playErr: context.DeadlineExceeded, healthStatus: 200}
			err := probeTransport(c, game).Turn(context.Background(), map[string]int{"round": 1}, nil)
			if err == nil {
				t.Fatal("a failed turn reported success")
			}
			if got := atomic.LoadInt32(&c.healths); got != 1 {
				t.Fatalf("%s: probed %d times on a missed turn, want exactly 1 — a stake can "+
					"be forfeited on this path without the platform ever checking whether the "+
					"agent was reachable", game, got)
			}
		})
	}
}

// A successful turn must NOT probe. The check is evidence for a forfeit, not a heartbeat:
// probing on every turn would double the request count against every healthy agent.
func TestSuccessfulTurnDoesNotProbe(t *testing.T) {
	c := &probeClient{healthStatus: 200}
	if err := probeTransport(c, "goofspiel").Turn(context.Background(), map[string]int{}, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := atomic.LoadInt32(&c.healths); got != 0 {
		t.Fatalf("probed %d times on a SUCCESSFUL turn — that is a doubled request rate "+
			"against every healthy agent on the platform", got)
	}
}

// The verdict has to survive back to the caller, or it cannot reach the decision record
// and the audit trail behind a forfeit is just a log line.
func TestVerdictReachesTheCaller(t *testing.T) {
	cases := map[string]struct {
		status int
		err    error
		want   Reachability
	}{
		"endpoint answers":     {200, nil, ReachAlive},
		"endpoint refuses":     {0, errors.New("connection refused"), ReachGone},
		"endpoint returns 503": {503, nil, ReachGone},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &probeClient{playErr: context.DeadlineExceeded, healthStatus: tc.status, healthErr: tc.err}
			err := probeTransport(c, "mafia").Turn(context.Background(), map[string]int{}, nil)
			if got := ReachabilityOf(err); got != tc.want {
				t.Fatalf("reachability=%q want %q", got, tc.want)
			}
		})
	}
}

// Wrapping must not hide the ORIGINAL failure. Callers classify a turn as timeout vs
// transport error from this error, and benchmark.ClassifyError drives the outcome written
// to every decision row — swallowing it would mislabel every missed turn on the platform.
func TestOriginalErrorSurvivesWrapping(t *testing.T) {
	c := &probeClient{playErr: context.DeadlineExceeded, healthStatus: 200}
	err := probeTransport(c, "monopoly").Turn(context.Background(), map[string]int{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the deadline error no longer unwraps from %v — a timeout would be "+
			"reclassified as a generic transport failure in every trace", err)
	}
	if err.Error() != context.DeadlineExceeded.Error() {
		t.Errorf("error text changed to %q", err.Error())
	}
}

// An unclassified error must report Unknown, never Gone. Manufacturing "the agent was
// down" for an error nobody probed is exactly the evidence-fabrication this design
// refuses elsewhere.
func TestUnclassifiedErrorsAreUnknown(t *testing.T) {
	if got := ReachabilityOf(errors.New("something else entirely")); got != ReachUnknown {
		t.Fatalf("a plain error reported reachability %q, want unknown", got)
	}
	if got := ReachabilityOf(nil); got != ReachUnknown {
		t.Fatalf("nil reported reachability %q, want unknown", got)
	}
}

// The socket path is deliberately NOT probed: the gateway already knows whether the
// connection is live, so an HTTP health check would be both redundant and wrong (a
// local-runtime agent need not host an endpoint at all).
func TestSocketTransportIsNotAnHTTPProbePath(t *testing.T) {
	var s Transport = SocketTransport{}
	if !s.Socket() {
		t.Fatal("SocketTransport no longer reports itself as the socket path")
	}
	var h Transport = HTTPTransport{}
	if h.Socket() {
		t.Fatal("HTTPTransport reports itself as a socket path")
	}
}
