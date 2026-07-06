package agentgw

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/remoteplay"
)

// pyAgentScript is a minimal developer agent using the real Python SDK. It dials
// the gateway over WSS, registers, and bids the highest legal card each turn.
const pyAgentScript = `
import os, sys, logging
logging.basicConfig(level=logging.INFO)
sys.path.insert(0, os.environ["ONAVION_SDK"])
from onavion import Agent

agent = Agent(supported_games=["goofspiel"], name="py-e2e")

seen_events = {"count": 0}
game_over = {"done": False}

@agent.on_turn("goofspiel")
def decide(v):
    return {"round": v.round, "card": max(v.legal_actions)}

@agent.on_event
def on_event(n):
    seen_events["count"] += 1

@agent.on_game_end
def on_end(n):
    game_over["done"] = True
    print("GAME_END_RECEIVED", flush=True)

agent.run(url=os.environ["ONAVION_URL"], agent_id="ag_py", token="secret",
          heartbeat_interval=0.5, reconnect=False)
`

// TestCrossLangPythonAgentDrivesMatch is the true end-to-end proof: the REAL
// Python SDK connector, running as a separate OS process (a developer's laptop),
// dials the Go gateway over a WebSocket and drives a full Goofspiel match. It
// verifies the Go and Python wire formats interoperate exactly.
//
// Skips cleanly if python3 / the websockets lib is unavailable (e.g. minimal CI).
func TestCrossLangPythonAgentDrivesMatch(t *testing.T) {
	py := pythonOrSkip(t)
	sdkPath := pythonSDKPath(t)

	gw, wsURL, closeSrv := newTestGateway(t, nil)
	// Slower heartbeat so a cross-process turn never trips liveness.
	gw.opts.HeartbeatInterval = 300 * time.Millisecond
	gw.opts.LivenessTimeout = 3 * time.Second
	defer closeSrv()

	// Write the agent script to a temp file and launch it.
	scriptPath := filepath.Join(t.TempDir(), "agent.py")
	if err := os.WriteFile(scriptPath, []byte(pyAgentScript), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, scriptPath)
	cmd.Env = append(os.Environ(), "ONAVION_URL="+wsURL, "ONAVION_SDK="+sdkPath, "PYTHONUNBUFFERED=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start python agent: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	waitConnected(t, gw, "ag_py")

	// Lifecycle: initialize (synchronous ack over the socket).
	if err := gw.Initialize(ctx, "ag_py", map[string]any{
		"protocol": "1.0", "match_id": "m-cross", "game": "goofspiel", "seat": 0, "players": 2,
	}); err != nil {
		t.Fatalf("initialize over socket: %v", err)
	}

	// Drive a real match: seat A is the Python agent over the socket.
	res, err := remoteplay.PlayGoofspiel(ctx,
		GoofspielDecider{GW: gw, AgentID: "ag_py", MatchID: "m-cross"},
		remoteplay.NearestPool{}, []byte("seed-cross-lang"))
	if err != nil {
		t.Fatalf("PlayGoofspiel: %v", err)
	}
	if !res.Finished || res.FallbackMoves != 0 {
		t.Fatalf("expected clean socket-driven match, got finished=%v fallbacks=%d", res.Finished, res.FallbackMoves)
	}

	// Exercise one-way frames: an event and the final game-end.
	ev, _ := json.Marshal(map[string]any{"round": 0, "winner": res.Winner})
	_ = gw.Event(ctx, "ag_py", "goofspiel", "m-cross", 1, "round_revealed", ev)
	result, _ := json.Marshal(map[string]any{"winner": res.Winner, "scores": []int{1, 0}})
	_ = gw.GameEnd(ctx, "ag_py", "goofspiel", "m-cross", result)

	// Give the agent a moment to receive game-end, then let it exit.
	time.Sleep(300 * time.Millisecond)
	t.Logf("cross-lang match ok: winner=%d rounds=%d (python SDK over WSS)", res.Winner, res.Rounds)
}

// jsAgentScript is a minimal developer agent using the real JS/TS SDK (built
// dist). It dials the gateway over WSS via the connector and bids high.
const jsAgentScript = `
const { Agent } = await import(process.env.ONAVION_JS);
const agent = new Agent({ supportedGames: ["goofspiel"], name: "js-e2e" });
agent.onTurn("goofspiel", (v) => ({ round: v.round, card: Math.max(...v.legal_actions) }));
agent.onGameEnd(() => { console.log("GAME_END_RECEIVED"); });
await agent.run({ url: process.env.ONAVION_URL, agentId: "ag_js", token: "secret", heartbeatMs: 500, reconnect: false });
`

// TestCrossLangJSAgentDrivesMatch is the JS counterpart of the Python e2e: the
// REAL JS SDK connector, in a separate Node process, dials the Go gateway over a
// WebSocket and drives a full Goofspiel match. Skips if node / the built dist are
// unavailable.
func TestCrossLangJSAgentDrivesMatch(t *testing.T) {
	node := nodeOrSkip(t)
	distIndex := jsSDKDist(t)

	gw, wsURL, closeSrv := newTestGateway(t, nil)
	gw.opts.HeartbeatInterval = 300 * time.Millisecond
	gw.opts.LivenessTimeout = 3 * time.Second
	defer closeSrv()

	scriptPath := filepath.Join(t.TempDir(), "agent.mjs")
	if err := os.WriteFile(scriptPath, []byte(jsAgentScript), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, scriptPath)
	cmd.Env = append(os.Environ(), "ONAVION_URL="+wsURL, "ONAVION_JS="+distIndex)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start node agent: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	waitConnected(t, gw, "ag_js")

	if err := gw.Initialize(ctx, "ag_js", map[string]any{
		"protocol": "1.0", "match_id": "m-js", "game": "goofspiel", "seat": 0, "players": 2,
	}); err != nil {
		t.Fatalf("initialize over socket: %v", err)
	}
	res, err := remoteplay.PlayGoofspiel(ctx,
		GoofspielDecider{GW: gw, AgentID: "ag_js", MatchID: "m-js"},
		remoteplay.NearestPool{}, []byte("seed-cross-js"))
	if err != nil {
		t.Fatalf("PlayGoofspiel: %v", err)
	}
	if !res.Finished || res.FallbackMoves != 0 {
		t.Fatalf("expected clean socket-driven match, got finished=%v fallbacks=%d", res.Finished, res.FallbackMoves)
	}
	result, _ := json.Marshal(map[string]any{"winner": res.Winner})
	_ = gw.GameEnd(ctx, "ag_js", "goofspiel", "m-js", result)
	time.Sleep(300 * time.Millisecond)
	t.Logf("cross-lang JS match ok: winner=%d rounds=%d (JS SDK over WSS)", res.Winner, res.Rounds)
}

func nodeOrSkip(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping JS cross-language e2e")
	}
	return node
}

func jsSDKDist(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	p, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "sdk", "js", "dist", "index.js"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("JS SDK dist not built at %s (run npm run build); skipping", p)
	}
	return p
}

func pythonOrSkip(t *testing.T) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping cross-language e2e")
	}
	// Require the websockets lib (the connector's one dependency).
	if err := exec.Command(py, "-c", "import websockets").Run(); err != nil {
		t.Skip("python `websockets` lib not installed; skipping cross-language e2e")
	}
	return py
}

func pythonSDKPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	// backend/internal/agentgw/crosslang_test.go -> ../../../sdk/python
	p, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "sdk", "python"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p, "onavion", "runtime.py")); err != nil {
		t.Skipf("python SDK not found at %s; skipping cross-language e2e", p)
	}
	return p
}
