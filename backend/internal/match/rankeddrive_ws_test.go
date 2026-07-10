package match_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/match"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// wsAgent is a minimal in-process developer agent (what `pyyol run` does, in Go):
// it dials the gateway over a REAL WebSocket, registers for goofspiel, answers
// heartbeats and initialize acks, and on each turn plays the highest legal card.
// Returns a close func that disconnects and waits for the reader to stop.
func wsAgent(t *testing.T, wsURL, agentID string) func() {
	t.Helper()
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("%s dial: %v", agentID, err)
	}
	var hello agentgw.Frame
	if err := wsjson.Read(dialCtx, ws, &hello); err != nil || hello.T != agentgw.FrameHello {
		t.Fatalf("%s expected hello: %+v %v", agentID, hello, err)
	}
	if err := wsjson.Write(dialCtx, ws, agentgw.Frame{
		T: agentgw.FrameRegister, AgentID: agentID, Token: "secret",
		AgentName: agentID, Version: "1.0.0", Games: []string{"goofspiel"}, SDKVersion: "1.0.0",
	}); err != nil {
		t.Fatalf("%s register: %v", agentID, err)
	}
	var reg agentgw.Frame
	if err := wsjson.Read(dialCtx, ws, &reg); err != nil || reg.T != agentgw.FrameRegistered {
		t.Fatalf("%s expected registered: %+v %v", agentID, reg, err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx := context.Background()
		for {
			var f agentgw.Frame
			if err := wsjson.Read(ctx, ws, &f); err != nil {
				return
			}
			switch f.T {
			case agentgw.FramePing:
				_ = wsjson.Write(ctx, ws, agentgw.Frame{T: agentgw.FramePong, ID: f.ID})
			case agentgw.FrameInitialize:
				ack, _ := json.Marshal(map[string]any{"ready": true})
				_ = wsjson.Write(ctx, ws, agentgw.Frame{T: agentgw.FrameResponse, ID: f.ID, Payload: ack})
			case agentgw.FrameTurn:
				var view struct {
					Round int   `json:"round"`
					Legal []int `json:"legal_actions"`
				}
				_ = json.Unmarshal(f.Payload, &view)
				hi := 0
				for _, c := range view.Legal {
					if c > hi {
						hi = c
					}
				}
				move, _ := json.Marshal(map[string]any{"round": view.Round, "card": hi})
				_ = wsjson.Write(ctx, ws, agentgw.Frame{T: agentgw.FrameResponse, ID: f.ID, Payload: move})
			}
		}
	}()
	return func() { _ = ws.Close(websocket.StatusNormalClosure, ""); <-done }
}

// TestRankedAutoDriveTwoLiveSocketAgents is the 2-live-agent proof of the loop:
// two agents connect over REAL WebSockets to a REAL gateway, get paired into a
// staked match, and the platform drives BOTH their seats over their sockets to
// settlement — hands-free, no self-driving. Uses in-memory fakes for the ledger/
// repo, so it needs no Postgres/Redis and runs in the normal suite.
func TestRankedAutoDriveTwoLiveSocketAgents(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw := agentgw.New(nil, agentgw.Options{
		TurnTimeout: 2 * time.Second, HeartbeatInterval: 100 * time.Millisecond,
		LivenessTimeout: time.Second, AllowInsecureOrigin: true,
	}, discard)
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	closeA := wsAgent(t, wsURL, "ag_a")
	defer closeA()
	closeB := wsAgent(t, wsURL, "ag_b")
	defer closeB()

	// Wait for both sockets to register.
	deadline := time.Now().Add(3 * time.Second)
	for (!gw.Connected("ag_a") || !gw.Connected("ag_b")) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !gw.Connected("ag_a") || !gw.Connected("ag_b") {
		t.Fatal("both agents did not connect over the socket")
	}

	svc, _ := newSvcWithRepo()
	svc.EnableRankedDrive(gw, discard)
	ctx := context.Background()

	// Pair them into a staked match — CreatePaired stakes both seats and spawns the
	// auto-driver, which drives each connected agent over its socket.
	id, err := svc.CreatePaired(ctx, "ag_a", "usr_a", "ag_b", "usr_b", 50)
	if err != nil {
		t.Fatalf("CreatePaired: %v", err)
	}

	finish := time.Now().Add(10 * time.Second)
	for time.Now().Before(finish) {
		v, _ := svc.State(ctx, id, "ag_a", false, 0)
		if v.Status == match.StatusFinished {
			if v.Result == nil {
				t.Fatal("finished match has no result")
			}
			if len(v.History) != 13 {
				t.Fatalf("history len = %d, want 13 (full match played over the socket)", len(v.History))
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("two live socket agents were not driven to a finish")
}
