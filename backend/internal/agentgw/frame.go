// Package agentgw is the Pyyol Beta agent gateway: the inbound WebSocket
// endpoint a developer's LOCAL agent dials out to.
//
// This is the transport the Beta spec calls for — the developer runs their agent
// on their own machine (behind NAT, zero networking config) and the SDK opens a
// single persistent WSS connection OUTBOUND to the platform. The platform then
// pushes match lifecycle down that socket and reads decisions back over it. This
// is the inverse of an inbound HTTP webhook: the platform never dials the
// developer, so a laptop behind a firewall works without a public endpoint.
//
// The gateway owns:
//   - a connection registry (agentID -> live socket),
//   - registration + auth on connect,
//   - heartbeats + liveness (missed heartbeats -> offline),
//   - request/response correlation for the SYNCHRONOUS calls (initialize, turn),
//   - fire-and-forget delivery for one-way notifications (event, game-end).
//
// It intentionally reuses the existing decision seam: SocketDecider satisfies
// remoteplay.Decider, so a socket-connected agent drives the real engine exactly
// like the historical HTTP push client did — the engine stays authoritative and a
// slow/absent agent falls back to a deterministic move.
package agentgw

import "encoding/json"

// ProtocolVersion is echoed on the hello frame so SDKs can branch on it.
const ProtocolVersion = "1.0"

// Frame is the single JSON envelope carried in both directions over the socket.
// A minimal, self-describing frame keeps the wire format easy to reimplement in
// any language and easy to log.
type Frame struct {
	// T is the frame type — one of the Frame* constants below.
	T string `json:"t"`
	// ID correlates a request with its response (turn/initialize). Empty for
	// one-way frames (event/game-end/heartbeat).
	ID string `json:"id,omitempty"`

	// --- register (agent -> gateway, first frame) ---
	AgentID    string   `json:"agent_id,omitempty"`
	Token      string   `json:"token,omitempty"`
	AgentName  string   `json:"agent_name,omitempty"`
	Version    string   `json:"version,omitempty"`
	Games      []string `json:"games,omitempty"`
	SDKVersion string   `json:"sdk_version,omitempty"`

	// --- lifecycle (gateway -> agent) / response (agent -> gateway) ---
	MatchID string          `json:"match_id,omitempty"`
	Game    string          `json:"game,omitempty"`
	Seq     int             `json:"seq,omitempty"`
	Kind    string          `json:"kind,omitempty"` // event sub-type (round_revealed, …)
	Payload json.RawMessage `json:"payload,omitempty"`

	// --- errors / status ---
	Error  string `json:"error,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Frame types. Direction is documented per constant; the wire value is the JSON
// `t`. Keeping them as plain strings (not ints) makes captured traffic readable.
const (
	// gateway -> agent
	FrameHello      = "hello"      // sent right after accept; carries protocol version
	FrameRegistered = "registered" // register accepted; carries resolved agent id
	FramePong       = "pong"       // heartbeat acknowledgement
	FrameInitialize = "initialize" // match starting (synchronous ack expected)
	FrameTurn       = "turn"       // decide a move (synchronous response expected)
	FrameEvent      = "event"      // one-way: a public game event happened
	FrameGameEnd    = "game_end"   // one-way: final result
	FrameError      = "error"      // gateway-level error (e.g. auth rejected)

	// agent -> gateway
	FrameRegister = "register" // first frame; carries token + capabilities
	FramePing     = "ping"     // heartbeat
	FrameResponse = "response" // reply to a turn/initialize (correlated by ID)
	FrameAck      = "ack"      // optional ack for a one-way frame
)
