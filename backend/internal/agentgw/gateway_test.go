package agentgw

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/remoteplay"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// fakeAgent is an in-process developer agent: it dials the gateway over a real
// WebSocket, registers, answers heartbeats, and runs a turn handler — exactly
// what the SDK RuntimeConnector does, but in Go for a self-contained test.
type fakeAgent struct {
	t       *testing.T
	ws      *websocket.Conn
	onTurn  func(view map[string]any) map[string]any
	onInit  func(payload map[string]any)
	events  []Frame
	gameEnd *Frame
}

func dialFakeAgent(t *testing.T, wsURL, agentID, token string, onTurn func(map[string]any) map[string]any) *fakeAgent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	a := &fakeAgent{t: t, ws: ws, onTurn: onTurn}

	// hello then register.
	var hello Frame
	if err := wsjson.Read(ctx, ws, &hello); err != nil || hello.T != FrameHello {
		t.Fatalf("expected hello, got %+v err=%v", hello, err)
	}
	if err := wsjson.Write(ctx, ws, Frame{
		T: FrameRegister, AgentID: agentID, Token: token,
		AgentName: "test-agent", Version: "1.0.0", Games: []string{"goofspiel"}, SDKVersion: "1.0.0",
	}); err != nil {
		t.Fatalf("register write: %v", err)
	}
	var reg Frame
	if err := wsjson.Read(ctx, ws, &reg); err != nil || reg.T != FrameRegistered {
		t.Fatalf("expected registered, got %+v err=%v", reg, err)
	}
	return a
}

// run reads frames until the socket closes, dispatching to handlers. Runs in a
// goroutine for the life of the test.
func (a *fakeAgent) run() {
	ctx := context.Background()
	for {
		var f Frame
		if err := wsjson.Read(ctx, a.ws, &f); err != nil {
			return
		}
		switch f.T {
		case FramePing:
			_ = wsjson.Write(ctx, a.ws, Frame{T: FramePong, ID: f.ID})
		case FrameTurn:
			var view map[string]any
			_ = json.Unmarshal(f.Payload, &view)
			move := a.onTurn(view)
			payload, _ := json.Marshal(move)
			_ = wsjson.Write(ctx, a.ws, Frame{T: FrameResponse, ID: f.ID, Payload: payload})
		case FrameInitialize:
			var p map[string]any
			_ = json.Unmarshal(f.Payload, &p)
			if a.onInit != nil {
				a.onInit(p)
			}
			ack, _ := json.Marshal(map[string]any{"ready": true})
			_ = wsjson.Write(ctx, a.ws, Frame{T: FrameResponse, ID: f.ID, Payload: ack})
		case FrameEvent:
			a.events = append(a.events, f)
		case FrameGameEnd:
			cp := f
			a.gameEnd = &cp
		}
	}
}

func newTestGateway(t *testing.T, auth Authenticator) (*Gateway, string, func()) {
	t.Helper()
	gw := New(auth, Options{
		TurnTimeout:         2 * time.Second,
		HeartbeatInterval:   100 * time.Millisecond,
		LivenessTimeout:     time.Second,
		AllowInsecureOrigin: true,
	}, nil)
	srv := httptest.NewServer(gw.Handler())
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return gw, wsURL, srv.Close
}

// TestFullGoofspielMatchOverSocket is the end-to-end proof of the architecture:
// a developer agent on a local process dials OUT over WSS, registers, and drives
// a REAL Goofspiel match to completion purely over the socket. The gateway's
// SocketDecider feeds remoteplay.PlayGoofspiel; the engine stays authoritative.
func TestFullGoofspielMatchOverSocket(t *testing.T) {
	gw, wsURL, closeSrv := newTestGateway(t, nil)
	defer closeSrv()

	// Agent strategy: bid the highest legal card (a real, non-fallback decision).
	agent := dialFakeAgent(t, wsURL, "ag_test", "secret", func(view map[string]any) map[string]any {
		legal := toInts(view["legal_actions"])
		hi := legal[0]
		for _, c := range legal {
			if c > hi {
				hi = c
			}
		}
		return map[string]any{"round": int(asFloat(view["round"])), "card": hi}
	})
	go agent.run()

	waitConnected(t, gw, "ag_test")

	// Seat A = the socket agent; seat B = a deterministic house bot.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seatA := GoofspielDecider{GW: gw, AgentID: "ag_test", MatchID: "m1"}
	seatB := remoteplay.NearestPool{}

	res, err := remoteplay.PlayGoofspiel(ctx, seatA, seatB, []byte("seed-socket-e2e"))
	if err != nil {
		t.Fatalf("PlayGoofspiel: %v", err)
	}
	if !res.Finished {
		t.Fatalf("match did not finish: %+v", res)
	}
	// The whole point: the socket agent's real decisions drove the match — no
	// fallbacks means every one of its turns round-tripped over the socket.
	if res.FallbackMoves != 0 {
		t.Fatalf("expected 0 fallbacks (all decisions over socket), got %d", res.FallbackMoves)
	}
	if res.Rounds != goofspiel.DefaultConfig().Rounds {
		t.Fatalf("expected %d rounds, got %d", goofspiel.DefaultConfig().Rounds, res.Rounds)
	}
	t.Logf("socket-driven match finished: winner=%d rounds=%d moves=%d replay=%s",
		res.Winner, res.Rounds, res.Moves, res.ReplayHash[:12])
}

// TestTurnTimeoutFallsBack proves an unresponsive agent times out cleanly so the
// engine can fall back — the match never wedges on a stalled laptop.
func TestTurnTimeoutFallsBack(t *testing.T) {
	gw, wsURL, closeSrv := newTestGateway(t, nil)
	defer closeSrv()

	agent := dialFakeAgent(t, wsURL, "ag_slow", "secret", func(view map[string]any) map[string]any {
		time.Sleep(5 * time.Second) // longer than TurnTimeout
		return map[string]any{"card": 1}
	})
	go agent.run()
	waitConnected(t, gw, "ag_slow")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := remoteplay.PlayGoofspiel(ctx,
		GoofspielDecider{GW: gw, AgentID: "ag_slow", MatchID: "m2"},
		remoteplay.NearestPool{}, []byte("seed-timeout"))
	if err != nil {
		t.Fatalf("PlayGoofspiel: %v", err)
	}
	if !res.Finished {
		t.Fatal("match did not finish despite fallback")
	}
	if res.FallbackMoves == 0 {
		t.Fatal("expected fallbacks from the stalled agent, got 0")
	}
}

// TestNotConnectedDecider proves a decision for an absent agent errors (→ engine
// fallback) rather than blocking.
func TestNotConnectedDecider(t *testing.T) {
	gw, _, closeSrv := newTestGateway(t, nil)
	defer closeSrv()
	d := GoofspielDecider{GW: gw, AgentID: "nobody", MatchID: "m3"}
	_, err := d.Decide(context.Background(), remoteplay.GoofspielView{LegalActions: []int{1, 2, 3}})
	if err == nil {
		t.Fatal("expected error for a disconnected agent")
	}
}

// TestAuthRejection proves the gateway closes a socket whose token is rejected.
func TestAuthRejection(t *testing.T) {
	auth := AuthenticatorFunc(func(_ context.Context, token, id string) (string, bool) {
		return id, token == "good"
	})
	_, wsURL, closeSrv := newTestGateway(t, auth)
	defer closeSrv()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var hello Frame
	_ = wsjson.Read(ctx, ws, &hello)
	_ = wsjson.Write(ctx, ws, Frame{T: FrameRegister, AgentID: "ag_x", Token: "bad"})
	// Expect an error frame then close.
	var resp Frame
	if err := wsjson.Read(ctx, ws, &resp); err != nil {
		t.Fatalf("expected error frame, got read err: %v", err)
	}
	if resp.T != FrameError || resp.Error != "unauthorized" {
		t.Fatalf("expected unauthorized error frame, got %+v", resp)
	}
}

// --- helpers ---

func waitConnected(t *testing.T, gw *Gateway, agentID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if gw.Connected(agentID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent %s never connected", agentID)
}

func toInts(v any) []int {
	arr, _ := v.([]any)
	out := make([]int, 0, len(arr))
	for _, x := range arr {
		out = append(out, int(asFloat(x)))
	}
	return out
}

func asFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}
