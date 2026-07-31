package agentgw

import (
	"context"
	"testing"
	"time"
)

// A brief disconnect must NOT cost a turn. This is the money case: the engine's
// fallback is right for an agent that answers badly, but an agent mid-reconnect has
// not answered at all, and in ranked that fallback can lose a real stake.
func TestTurnWaitsThroughABriefReconnect(t *testing.T) {
	g := &Gateway{opts: Options{ReconnectGrace: 3 * time.Second}.withDefaults(), conns: map[string]*conn{}}

	// The agent is gone when the turn starts, and comes back shortly after.
	go func() {
		time.Sleep(250 * time.Millisecond)
		g.mu.Lock()
		g.conns["ag_1"] = &conn{}
		g.mu.Unlock()
	}()

	start := time.Now()
	c := g.awaitConn(context.Background(), "ag_1")
	if c == nil {
		t.Fatal("a 250ms blip cost the turn — the agent reconnected well inside the grace")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %v for a reconnect that took 250ms", elapsed)
	}
}

// An agent that is genuinely gone must not stall the table. The grace is a bound,
// not a promise to wait forever.
func TestTurnGivesUpOnAnAgentThatNeverReturns(t *testing.T) {
	g := &Gateway{opts: Options{ReconnectGrace: 400 * time.Millisecond}.withDefaults(), conns: map[string]*conn{}}

	start := time.Now()
	if c := g.awaitConn(context.Background(), "ag_missing"); c != nil {
		t.Fatal("returned a connection for an agent that never registered")
	}
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("took %v to give up on a 400ms grace — the table would stall", elapsed)
	}
}

// A connected agent must not pay any latency for this. The fast path has to stay
// a plain map lookup.
func TestConnectedAgentIsNotDelayed(t *testing.T) {
	g := &Gateway{opts: Options{ReconnectGrace: 10 * time.Second}.withDefaults(),
		conns: map[string]*conn{"ag_here": {}}}

	start := time.Now()
	if c := g.awaitConn(context.Background(), "ag_here"); c == nil {
		t.Fatal("lost a connected agent")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("connected agent waited %v — the fast path regressed", elapsed)
	}
}

// The caller's deadline still wins: a move window shorter than the grace must cut
// the wait, or the grace could outlive the turn it was protecting.
func TestCallerDeadlineBeatsTheGrace(t *testing.T) {
	g := &Gateway{opts: Options{ReconnectGrace: 30 * time.Second}.withDefaults(), conns: map[string]*conn{}}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	if c := g.awaitConn(ctx, "ag_gone"); c != nil {
		t.Fatal("returned a connection that never existed")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("ignored the caller's 300ms deadline, waited %v", elapsed)
	}
}

// A thinking agent sends nothing. The liveness timeout must therefore be governed by
// how long a MODEL takes, not by the ping cadence — at 3x15s it was 45s, the same
// order as the Goofspiel move budget and shorter than Monopoly's, so a healthy agent
// that took 40s to decide was closed as dead. That produced repeated mid-match
// "no close frame received or sent" reconnects.
func TestLivenessOutlastsTheLongestDecision(t *testing.T) {
	o := Options{}.withDefaults()

	const longestMoveWindow = 60 * time.Second // Monopoly
	if o.LivenessTimeout <= longestMoveWindow {
		t.Fatalf("liveness %v does not outlast a %v decision — a thinking agent would be "+
			"closed as dead", o.LivenessTimeout, longestMoveWindow)
	}
	if o.LivenessTimeout < 2*longestMoveWindow {
		t.Fatalf("liveness %v leaves no margin over a %v decision", o.LivenessTimeout, longestMoveWindow)
	}
}

// An explicitly configured value is still honoured, but never below 3 heartbeats —
// otherwise the socket could be reaped before a single ping had a chance to land.
func TestLivenessNeverDropsBelowThreeHeartbeats(t *testing.T) {
	o := Options{HeartbeatInterval: 30 * time.Second, LivenessTimeout: 10 * time.Second}.withDefaults()
	if o.LivenessTimeout < 90*time.Second {
		t.Fatalf("liveness %v is under 3 heartbeats (%v)", o.LivenessTimeout, 90*time.Second)
	}
}
