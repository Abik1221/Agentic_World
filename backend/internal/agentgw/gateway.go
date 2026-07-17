package agentgw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/agent-arena/arena/internal/platform/telemetry"
)

// ErrNotConnected is returned when a decision/notification is requested for an
// agent that has no live socket. Callers treat it like any transport error: the
// engine applies its deterministic fallback and the match keeps moving.
var ErrNotConnected = errors.New("agentgw: agent not connected")

// Authenticator validates the register token for a claimed agent id. It maps the
// developer's credential (Beta: the agent's endpoint secret; later: an OAuth
// access token) to a canonical agent id. Returning ok=false rejects the socket.
type Authenticator interface {
	Authenticate(ctx context.Context, token, agentID string) (resolvedID string, ok bool)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(ctx context.Context, token, agentID string) (string, bool)

func (f AuthenticatorFunc) Authenticate(ctx context.Context, token, agentID string) (string, bool) {
	return f(ctx, token, agentID)
}

// Options tune timeouts. Zero values fall back to sane defaults.
type Options struct {
	// TurnTimeout bounds a synchronous turn/initialize round-trip. On expiry the
	// caller gets a timeout error and the engine falls back. Defaults to 10s.
	TurnTimeout time.Duration
	// HeartbeatInterval is how often the gateway probes an idle socket. Defaults 15s.
	HeartbeatInterval time.Duration
	// LivenessTimeout marks a socket offline after this long with no frame at all.
	// Defaults to 3× HeartbeatInterval.
	LivenessTimeout time.Duration
	// WriteTimeout bounds a single frame write. Defaults 10s.
	WriteTimeout time.Duration
	// AllowInsecureOrigin skips WebSocket origin checking (dev only; the SDK is a
	// non-browser client so there is no meaningful origin, but browsers testing the
	// endpoint would be rejected otherwise).
	AllowInsecureOrigin bool
	// SDKVersionInfo, if set, returns the newest published and minimum-supported
	// SDK version for a client language ("python"/"js"), read live per connect.
	// The gateway echoes `latest` on the registered frame (upgrade nudge) and, if
	// `min` is set and the client is older, refuses the connection. Both empty ⇒
	// no floor and no nudge. Nil ⇒ feature off entirely.
	SDKVersionInfo func(language string) (latest, min string)
	// MaxConns caps total concurrent sockets (pre- and post-auth); MaxConnsPerIP
	// caps them per client IP. Both defend the pre-auth window against a socket
	// flood (each socket holds goroutines + buffers). Defaults 5000 / 20.
	MaxConns      int
	MaxConnsPerIP int
	// ClientIP extracts the real client IP for the per-IP cap. Nil ⇒ RemoteAddr
	// host. Set to middleware.ClientIP so the cap isn't defeated behind the LB.
	ClientIP func(*http.Request) string
	// Emitter ships per-decision telemetry to Pyyol Lens. Nil ⇒ no telemetry.
	// This is THE choke point every agent interaction crosses, so instrumenting
	// it here traces every turn/initialize/event/game-end across all games.
	Emitter *telemetry.Client
}

func (o Options) withDefaults() Options {
	if o.TurnTimeout <= 0 {
		o.TurnTimeout = 10 * time.Second
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = 15 * time.Second
	}
	if o.LivenessTimeout <= 0 {
		o.LivenessTimeout = 3 * o.HeartbeatInterval
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.MaxConns <= 0 {
		o.MaxConns = 5000
	}
	if o.MaxConnsPerIP <= 0 {
		o.MaxConnsPerIP = 20
	}
	return o
}

// Gateway is the connection registry + WSS handler. Safe for concurrent use.
type Gateway struct {
	auth Authenticator
	opts Options
	log  *slog.Logger
	em   *telemetry.Client

	mu    sync.RWMutex
	conns map[string]*conn // agentID -> current live connection (latest wins)

	connMu  sync.Mutex     // guards the accept-slot counters below
	active  int            // total accepted sockets (pre- and post-auth)
	ipConns map[string]int // per-client-IP accepted socket count
}

// AgentStatus is a snapshot of a connected agent for the dashboard / status CLI.
type AgentStatus struct {
	AgentID    string    `json:"agent_id"`
	Name       string    `json:"name"`
	Games      []string  `json:"games"`
	SDKVersion string    `json:"sdk_version"`
	LastSeen   time.Time `json:"last_seen"`
}

// New builds a gateway. auth may be nil only in tests (all tokens accepted).
func New(auth Authenticator, opts Options, log *slog.Logger) *Gateway {
	if log == nil {
		log = slog.Default()
	}
	return &Gateway{
		auth:    auth,
		opts:    opts.withDefaults(),
		log:     log,
		em:      opts.Emitter,
		conns:   make(map[string]*conn),
		ipConns: make(map[string]int),
	}
}

// acquireSlot reserves an accept slot for ip, enforcing the global and per-IP
// caps. It returns false (no slot taken) when either cap is hit.
func (g *Gateway) acquireSlot(ip string) bool {
	g.connMu.Lock()
	defer g.connMu.Unlock()
	if g.active >= g.opts.MaxConns || g.ipConns[ip] >= g.opts.MaxConnsPerIP {
		return false
	}
	g.active++
	g.ipConns[ip]++
	return true
}

func (g *Gateway) releaseSlot(ip string) {
	g.connMu.Lock()
	defer g.connMu.Unlock()
	g.active--
	if g.ipConns[ip]--; g.ipConns[ip] <= 0 {
		delete(g.ipConns, ip)
	}
}

// safeGo runs a background loop with panic recovery: a nil-deref or other panic in
// writeLoop/heartbeatLoop logs and unwinds this connection instead of crashing the
// whole process (only the request goroutine is covered by the HTTP recover mw).
func (g *Gateway) safeGo(wg *sync.WaitGroup, name string, fn func()) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				g.log.Error("agentgw: background loop panicked", "loop", name, "panic", r)
			}
		}()
		fn()
	}()
}

func (g *Gateway) clientIP(r *http.Request) string {
	if g.opts.ClientIP != nil {
		return g.opts.ClientIP(r)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// Connected reports whether the agent currently has a live socket.
func (g *Gateway) Connected(agentID string) bool { return g.lookup(agentID) != nil }

// Online returns a snapshot of every connected agent.
func (g *Gateway) Online() []AgentStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]AgentStatus, 0, len(g.conns))
	for _, c := range g.conns {
		out = append(out, AgentStatus{
			AgentID:    c.agentID,
			Name:       c.name,
			Games:      c.games,
			SDKVersion: c.sdkVersion,
			LastSeen:   time.Unix(0, c.lastSeen.Load()),
		})
	}
	return out
}

// ConnectionStatus reports whether an agent is connected and, if so, a snapshot
// for the dashboard / `pyyol status`.
func (g *Gateway) ConnectionStatus(agentID string) (AgentStatus, bool) {
	c := g.lookup(agentID)
	if c == nil {
		return AgentStatus{AgentID: agentID}, false
	}
	return AgentStatus{
		AgentID:    c.agentID,
		Name:       c.name,
		Games:      c.games,
		SDKVersion: c.sdkVersion,
		LastSeen:   time.Unix(0, c.lastSeen.Load()),
	}, true
}

func (g *Gateway) lookup(agentID string) *conn {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.conns[agentID]
}

// Handler upgrades an inbound HTTP request to a WebSocket, runs the register
// handshake, and serves the connection until it closes. Mount it at the agent
// connect route (e.g. GET /v1/agent/connect).
func (g *Gateway) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Enforce connection caps BEFORE upgrading, so a flood is rejected without
		// allocating a socket, goroutines, or buffers.
		ip := g.clientIP(r)
		if !g.acquireSlot(ip) {
			http.Error(w, "too many connections", http.StatusServiceUnavailable)
			return
		}
		defer g.releaseSlot(ip)

		acceptOpts := &websocket.AcceptOptions{}
		if g.opts.AllowInsecureOrigin {
			acceptOpts.InsecureSkipVerify = true
		}
		ws, err := websocket.Accept(w, r, acceptOpts)
		if err != nil {
			g.log.Warn("agentgw: accept failed", "err", err)
			return
		}
		// Small pre-auth read limit: an unauthenticated client only needs to send a
		// tiny register frame. serve() raises it to 1 MiB after a successful
		// register (mafia turn views carry the full transcript).
		ws.SetReadLimit(64 << 10) // 64 KiB
		g.serve(r.Context(), ws)
	}
}

// serve runs the register handshake then the read/write loops for one socket.
func (g *Gateway) serve(parent context.Context, ws *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()

	_ = writeFrame(ctx, ws, g.opts.WriteTimeout, Frame{T: FrameHello, Version: ProtocolVersion})

	// The first frame MUST be a register within a short window.
	regCtx, regCancel := context.WithTimeout(ctx, 10*time.Second)
	var reg Frame
	err := wsjson.Read(regCtx, ws, &reg)
	regCancel()
	if err != nil || reg.T != FrameRegister {
		_ = writeFrame(ctx, ws, g.opts.WriteTimeout, Frame{T: FrameError, Error: "expected_register", Reason: "first frame must be register"})
		_ = ws.Close(websocket.StatusPolicyViolation, "expected register")
		return
	}

	agentID := reg.AgentID
	if g.auth != nil {
		resolved, ok := g.auth.Authenticate(ctx, reg.Token, reg.AgentID)
		if !ok {
			_ = writeFrame(ctx, ws, g.opts.WriteTimeout, Frame{T: FrameError, Error: "unauthorized", Reason: "register token rejected"})
			_ = ws.Close(websocket.StatusPolicyViolation, "unauthorized")
			return
		}
		agentID = resolved
	}
	if agentID == "" {
		_ = writeFrame(ctx, ws, g.opts.WriteTimeout, Frame{T: FrameError, Error: "no_agent_id", Reason: "register did not resolve an agent id"})
		_ = ws.Close(websocket.StatusPolicyViolation, "no agent id")
		return
	}

	// Upgrade nudge / version floor (additive; only when configured). `latest` is
	// echoed on the registered frame so the SDK can suggest an upgrade; if `min`
	// is set and the client is older, the connection is refused with a clear error.
	var latestSDK, minSDK string
	if g.opts.SDKVersionInfo != nil {
		latestSDK, minSDK = g.opts.SDKVersionInfo(reg.SDKLanguage)
		if minSDK != "" && reg.SDKVersion != "" && versionLess(reg.SDKVersion, minSDK) {
			_ = writeFrame(ctx, ws, g.opts.WriteTimeout, Frame{
				T: FrameError, Error: "sdk_too_old",
				Reason: "pyyol >= " + minSDK + " required — upgrade with `pip install -U pyyol` (or `npm update pyyol`)",
			})
			_ = ws.Close(websocket.StatusPolicyViolation, "sdk too old")
			return
		}
	}

	c := &conn{
		gw:         g,
		ws:         ws,
		agentID:    agentID,
		name:       reg.AgentName,
		games:      reg.Games,
		sdkVersion: reg.SDKVersion,
		sendCh:     make(chan Frame, 32),
		pending:    make(map[string]chan Frame),
		closed:     make(chan struct{}),
		cancel:     cancel,
	}
	c.lastSeen.Store(time.Now().UnixNano())

	g.register(c)
	defer g.unregister(c)

	// Authenticated: raise the read limit for large in-game turn views (mafia
	// transcripts). The pre-auth limit was intentionally small (see Handler).
	ws.SetReadLimit(1 << 20) // 1 MiB
	_ = writeFrame(ctx, ws, g.opts.WriteTimeout, Frame{T: FrameRegistered, AgentID: agentID, Version: ProtocolVersion, LatestSDK: latestSDK, MinSDK: minSDK})
	g.log.Info("agentgw: agent connected", "agent", agentID, "name", reg.AgentName, "games", reg.Games, "sdk", reg.SDKVersion, "sdk_lang", reg.SDKLanguage)

	// Writer + heartbeat run in the background; the read loop drives the lifetime.
	var wg sync.WaitGroup
	g.safeGo(&wg, "write", func() { c.writeLoop(ctx) })
	g.safeGo(&wg, "heartbeat", func() { c.heartbeatLoop(ctx) })

	c.readLoop(ctx)
	c.close(websocket.StatusNormalClosure, "")
	wg.Wait()
	g.log.Info("agentgw: agent disconnected", "agent", agentID)
}

// register installs c as the live connection for its agent id, displacing (and
// closing) any stale connection — a reconnect supersedes the old socket.
func (g *Gateway) register(c *conn) {
	g.mu.Lock()
	old := g.conns[c.agentID]
	g.conns[c.agentID] = c
	g.mu.Unlock()
	if old != nil && old != c {
		g.log.Info("agentgw: superseding stale connection", "agent", c.agentID)
		old.close(websocket.StatusNormalClosure, "superseded by reconnect")
	}
}

// unregister removes c only if it is still the current connection (a newer
// reconnect must not be evicted by an older socket's teardown).
func (g *Gateway) unregister(c *conn) {
	g.mu.Lock()
	if g.conns[c.agentID] == c {
		delete(g.conns, c.agentID)
	}
	g.mu.Unlock()
}

// --- synchronous + one-way calls (the transport surface used by drivers) ---

// Initialize notifies the agent a match is starting and waits for its ack. A
// non-connected agent or timeout returns an error; callers treat it as best-effort.
func (g *Gateway) Initialize(ctx context.Context, agentID string, req any) error {
	_, err := g.request(ctx, agentID, FrameInitialize, req)
	return err
}

// Turn asks the agent to decide and unmarshals its move into out. Synchronous,
// bounded by Options.TurnTimeout. On any error the caller falls back.
func (g *Gateway) Turn(ctx context.Context, agentID string, view any, out any) error {
	raw, err := g.request(ctx, agentID, FrameTurn, view)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Event delivers one public game event (one-way). Best-effort: a disconnected
// agent simply misses it (it can rebuild from the next turn view).
func (g *Gateway) Event(ctx context.Context, agentID, game, matchID string, seq int, kind string, payload json.RawMessage) (err error) {
	_, done := g.em.StartSpan(ctx, "agent.event", telemetry.Attrs{
		SpanType: "agent_notify", Operation: "event", AgentID: agentID, MatchID: matchID, Game: game,
		Payload: map[string]any{"kind": kind, "seq": seq},
	})
	defer func() { done(err) }()
	c := g.lookup(agentID)
	if c == nil {
		return ErrNotConnected
	}
	return c.send(ctx, Frame{T: FrameEvent, Game: game, MatchID: matchID, Seq: seq, Kind: kind, Payload: payload})
}

// GameEnd delivers the final result (one-way).
func (g *Gateway) GameEnd(ctx context.Context, agentID, game, matchID string, result json.RawMessage) (err error) {
	_, done := g.em.StartSpan(ctx, "agent.game_end", telemetry.Attrs{
		SpanType: "agent_notify", Operation: "game_end", AgentID: agentID, MatchID: matchID, Game: game,
	})
	defer func() { done(err) }()
	c := g.lookup(agentID)
	if c == nil {
		return ErrNotConnected
	}
	return c.send(ctx, Frame{T: FrameGameEnd, Game: game, MatchID: matchID, Payload: result})
}

// request sends a correlated frame and waits for the matching response. Every
// agent decision crosses this path, so it is instrumented once here: a span per
// round-trip, correlated to the match trace when the body carries a match id.
func (g *Gateway) request(ctx context.Context, agentID, frameType string, body any) (raw json.RawMessage, err error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	// Best-effort match context from the already-marshaled body (no extra cost on
	// the hot path beyond one small unmarshal), so gateway spans nest under the
	// match's trace across every game.
	var meta struct {
		MatchID string `json:"match_id"`
		Round   int    `json:"round"`
		Game    string `json:"game"`
	}
	_ = json.Unmarshal(payload, &meta)
	_, done := g.em.StartSpan(ctx, "agent."+frameType, telemetry.Attrs{
		SpanType: "agent_call", Operation: frameType, AgentID: agentID,
		MatchID: meta.MatchID, Game: meta.Game,
		Payload: map[string]any{"frame": frameType, "round": meta.Round},
	})
	defer func() { done(err) }()

	c := g.lookup(agentID)
	if c == nil {
		return nil, ErrNotConnected
	}
	id := newID()
	ch := make(chan Frame, 1)
	c.addPending(id, ch)
	defer c.removePending(id)

	if err := c.send(ctx, Frame{T: frameType, ID: id, Payload: payload}); err != nil {
		return nil, err
	}

	tctx, cancel := context.WithTimeout(ctx, g.opts.TurnTimeout)
	defer cancel()
	select {
	case resp := <-ch:
		if resp.Error != "" {
			return nil, errors.New("agentgw: agent error: " + resp.Error)
		}
		return resp.Payload, nil
	case <-tctx.Done():
		return nil, tctx.Err()
	case <-c.closed:
		return nil, ErrNotConnected
	}
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeFrame(ctx context.Context, ws *websocket.Conn, timeout time.Duration, f Frame) error {
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return wsjson.Write(wctx, ws, f)
}

// atomicInt64 alias for readability in conn.lastSeen.
type atomicInt64 = atomic.Int64
