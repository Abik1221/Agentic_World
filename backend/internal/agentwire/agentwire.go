// Package agentwire is the transport seam between a match driver and a developer
// agent. A driver doesn't care whether the agent is a LOCAL agent on a live
// WebSocket (the Beta local-runtime model) or a hosted HTTP endpoint (the legacy
// push model) — it just needs to initialize, ask for a turn, and fire
// event/game-end notifications. This package provides one Transport interface
// with both implementations, so the choice ("is this agent connected over the
// socket right now?") is made in exactly one place.
package agentwire

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/webhook"
)

// Transport delivers the push-protocol lifecycle to one agent for one match.
// Game is baked into the concrete value, so callers pass only match-scoped data.
type Transport interface {
	// Initialize notifies the agent a match is starting (best-effort).
	Initialize(ctx context.Context, req agentclient.InitializeRequest) error
	// Turn asks the agent to decide and unmarshals its move into out. Synchronous.
	Turn(ctx context.Context, view, out any) error
	// Event delivers one public game event (one-way).
	Event(ctx context.Context, matchID string, seq int, kind string, payload []byte) error
	// GameEnd delivers the final result (one-way).
	GameEnd(ctx context.Context, matchID string, result []byte) error
	// Socket reports whether this transport is the live WebSocket path.
	Socket() bool
}

// HTTPClient is the subset of agentclient.Client the HTTP transport needs (also
// satisfied by the per-package PushClient interfaces).
type HTTPClient interface {
	Play(ctx context.Context, t agentclient.Target, request, out any) (int, error)
	Initialize(ctx context.Context, t agentclient.Target, req agentclient.InitializeRequest) (agentclient.InitializeResponse, error)
	Event(ctx context.Context, t agentclient.Target, n agentclient.EventNotification) error
	GameEnd(ctx context.Context, t agentclient.Target, n agentclient.GameEndNotification) error
}

// Socket is the local-runtime transport: the platform pushes over the agent's
// live WebSocket. One-way notifications are best-effort — a disconnected agent
// simply misses them (it rebuilds from the next, self-contained turn view).
type SocketTransport struct {
	GW      *agentgw.Gateway
	AgentID string
	Game    string
}

func (s SocketTransport) Initialize(ctx context.Context, req agentclient.InitializeRequest) error {
	return s.GW.Initialize(ctx, s.AgentID, req)
}
func (s SocketTransport) Turn(ctx context.Context, view, out any) error {
	return s.GW.Turn(ctx, s.AgentID, view, out)
}
func (s SocketTransport) Event(ctx context.Context, matchID string, seq int, kind string, payload []byte) error {
	return s.GW.Event(ctx, s.AgentID, s.Game, matchID, seq, kind, json.RawMessage(payload))
}
func (s SocketTransport) GameEnd(ctx context.Context, matchID string, result []byte) error {
	return s.GW.GameEnd(ctx, s.AgentID, s.Game, matchID, json.RawMessage(result))
}
func (s SocketTransport) Socket() bool { return true }

// HTTPTransport is the hosted-endpoint transport. Turns are synchronous POSTs;
// notifications go through the durable webhook queue when configured (signed,
// retried, health-gated) and otherwise a best-effort detached goroutine — the
// engine never blocks on delivery either way. This centralizes the durable-vs-
// inline logic that previously lived in each game's push driver.
type HTTPTransport struct {
	Client  HTTPClient
	Target  agentclient.Target
	Enqueue webhook.Enqueuer // may be nil → inline fallback
	AgentID string
	Game    string
	Log     *slog.Logger
}

func (h HTTPTransport) Initialize(ctx context.Context, req agentclient.InitializeRequest) error {
	_, err := h.Client.Initialize(ctx, h.Target, req)
	return err
}
func (h HTTPTransport) Turn(ctx context.Context, view, out any) error {
	_, err := h.Client.Play(ctx, h.Target, view, out)
	return err
}
func (h HTTPTransport) Event(ctx context.Context, matchID string, seq int, kind string, payload []byte) error {
	if h.Enqueue != nil {
		return h.Enqueue.EnqueueEvent(context.WithoutCancel(ctx), h.AgentID, h.Game, matchID, seq, kind, payload)
	}
	n := agentclient.EventNotification{MatchID: matchID, Game: h.Game, Seq: seq, Type: kind, Payload: payload}
	go func() { _ = h.Client.Event(context.WithoutCancel(ctx), h.Target, n) }()
	return nil
}
func (h HTTPTransport) GameEnd(ctx context.Context, matchID string, result []byte) error {
	if h.Enqueue != nil {
		return h.Enqueue.EnqueueGameEnd(context.WithoutCancel(ctx), h.AgentID, h.Game, matchID, result)
	}
	go func() {
		_ = h.Client.GameEnd(context.WithoutCancel(ctx), h.Target, agentclient.GameEndNotification{
			MatchID: matchID, Game: h.Game, Result: result,
		})
	}()
	return nil
}
func (h HTTPTransport) Socket() bool { return false }
