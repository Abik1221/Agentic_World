package agentwire

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
)

type fakeProber struct {
	status int
	err    error
	calls  int
	// ctxErrAtCall is the context's error AT THE MOMENT OF THE CALL. Recording the
	// context itself would be useless: ConfirmReachability defers its cancel, so by the
	// time a test inspected it every context would read as cancelled.
	ctxErrAtCall error
	// hadDeadline proves the probe is bounded rather than able to hang forever.
	hadDeadline bool
}

func (f *fakeProber) Health(ctx context.Context, _ agentclient.Target) (agentclient.HealthResult, error) {
	f.calls++
	f.ctxErrAtCall = ctx.Err()
	_, f.hadDeadline = ctx.Deadline()
	return agentclient.HealthResult{Status: f.status}, f.err
}

var target = agentclient.Target{EndpointURL: "https://agent.example.com/turn"}

// An endpoint that answers /health was UP — it just did not decide in time. Recording
// that as "gone" would tell a developer their deployment was down when the real bug is
// in their turn handler, and it would be the wrong evidence behind a forfeit.
func TestAliveEndpointIsNotReportedGone(t *testing.T) {
	p := &fakeProber{status: 200}
	if got := ConfirmReachability(context.Background(), p, target, time.Second, nil); got != ReachAlive {
		t.Fatalf("reachability=%q want %q", got, ReachAlive)
	}
	if p.calls != 1 {
		t.Fatalf("probed %d times, want exactly 1 — this runs on every missed turn", p.calls)
	}
}

func TestUnreachableEndpointIsReportedGone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		err    error
	}{
		{"connection refused", 0, errors.New("dial tcp: connection refused")},
		{"5xx", 503, nil},
		{"4xx", 404, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProber{status: tc.status, err: tc.err}
			if got := ConfirmReachability(context.Background(), p, target, time.Second, nil); got != ReachGone {
				t.Fatalf("reachability=%q want %q", got, ReachGone)
			}
		})
	}
}

// "We did not look" must never be recorded as "we looked and it was down". The whole
// point of the check is that a forfeit has evidence behind it; a default of ReachGone
// would manufacture that evidence for seats nobody ever probed.
func TestNoProbeTargetIsUnknownNotGone(t *testing.T) {
	if got := ConfirmReachability(context.Background(), nil, target, time.Second, nil); got != ReachUnknown {
		t.Fatalf("with no prober: %q, want %q", got, ReachUnknown)
	}
	p := &fakeProber{status: 200}
	if got := ConfirmReachability(context.Background(), p, agentclient.Target{}, time.Second, nil); got != ReachUnknown {
		t.Fatalf("with no endpoint: %q, want %q", got, ReachUnknown)
	}
	if p.calls != 0 {
		t.Fatal("probed an agent with no endpoint URL")
	}
}

// The regression this is built to prevent: the turn's context is ALREADY past its
// deadline by the time we probe — that is why we are probing. Inheriting it would abort
// the probe before it left the process and report every slow agent as unreachable,
// which is precisely the misclassification that would cost a developer their stake.
func TestProbeSurvivesAnAlreadyCancelledTurnContext(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel() // the turn has already timed out

	p := &fakeProber{status: 200}
	got := ConfirmReachability(dead, p, target, time.Second, nil)
	if got != ReachAlive {
		t.Fatalf("reachability=%q want %q — the probe inherited the dead turn context", got, ReachAlive)
	}
	if p.ctxErrAtCall != nil {
		t.Fatalf("the prober was handed an already-cancelled context (%v), so the probe would "+
			"never reach the network and every slow agent would be recorded as gone", p.ctxErrAtCall)
	}
	// Detached, but still bounded: a hanging endpoint must not outlive the turn it lost.
	if !p.hadDeadline {
		t.Fatal("the probe context has no deadline; a hanging endpoint could block indefinitely")
	}
}
