package userevents

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves GET /v1/events/stream — the signed-in user's own realtime feed.
//
// User scope only, and the identity comes from the token, never from the URL: a
// stream is the most attractive IDOR target on the platform (subscribe once, watch
// someone else's money forever), so there is deliberately no id parameter to
// tamper with.
type Handler struct {
	bus       *Bus
	authn     *auth.Authenticator
	heartbeat time.Duration
	// maxLifetime caps one connection. The dashboard JWT is short-lived and is only
	// checked at connect, so an unbounded stream would outlive the credential that
	// opened it. Closing on a schedule forces EventSource's automatic reconnect,
	// which re-authenticates (and picks up a refreshed token from the BFF).
	maxLifetime time.Duration
}

// NewHandler wires the stream endpoint.
func NewHandler(bus *Bus, authn *auth.Authenticator) *Handler {
	return &Handler{bus: bus, authn: authn, heartbeat: 25 * time.Second, maxLifetime: 30 * time.Minute}
}

// Register mounts the stream under user-scope auth.
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/events/stream", h.stream)
	})
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())

	events, cancel := h.bus.Subscribe(p.UserPublicID)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // ask nginx not to buffer the stream
	// authn.Middleware already set `Cache-Control: no-store, private` + the Vary
	// pair when it accepted the credential. This is one person's money feed; it is
	// restated here so a future refactor of the middleware cannot silently make a
	// private stream cacheable.
	w.Header().Set("Cache-Control", "no-store, private")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)

	// Open with a hello frame. Without it a client behind a buffering proxy sees no
	// bytes until the first real event, which can be hours — and cannot tell a
	// working-but-quiet stream apart from a broken one. It is also what lets the UI
	// switch its indicator from "reconnecting" to "live".
	if !h.write(w, rc, "ready", []byte(`{"ok":true}`)) {
		return
	}

	beat := time.NewTicker(h.heartbeat)
	defer beat.Stop()
	deadline := time.NewTimer(h.maxLifetime)
	defer deadline.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			// Ask the client to come back immediately rather than just dropping it:
			// EventSource would otherwise wait out its own retry delay.
			h.write(w, rc, "reconnect", []byte(`{"reason":"lifetime"}`))
			return
		case ev, ok := <-events:
			if !ok {
				return // subscription closed (Redis gone) — let the client reconnect
			}
			body, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if !h.write(w, rc, "event", body) {
				return
			}
		case <-beat.C:
			httpx.ArmWriteDeadline(w)
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// write emits one SSE frame. Returns false when the connection is gone.
//
// The write error is the connection signal; the flush error deliberately is not.
// A ResponseWriter that does not support flushing returns ErrNotSupported here
// forever, and treating that as a dead peer would close every stream instantly.
func (h *Handler) write(w http.ResponseWriter, rc *http.ResponseController, name string, data []byte) bool {
	httpx.ArmWriteDeadline(w)
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return false
	}
	_ = rc.Flush()
	return true
}
