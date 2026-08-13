package devplatform

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
)

// QueueSelfReader answers "what happened to MY agents in the queue".
//
// # The owner is supplied by the SERVER, never by the request
//
// Every method takes an ownerPublicID that the handler reads from the authenticated
// principal. There is deliberately no method here that accepts an owner from user input, and
// no "all owners" variant: a developer's queue history names their agents, how long each
// waited, and when each went unreachable — which is information about how their agent behaves
// under load. Serving one developer another's history would be a data leak wearing a
// dashboard.
//
// The store's implementations return NOTHING for an empty owner rather than everything. That
// direction is the point: if identity resolution ever fails, the answer must be "you see
// nothing", not "you see everyone".
type QueueSelfReader interface {
	FunnelForOwner(ctx context.Context, ownerPublicID string, since time.Duration) (QueueFunnel, error)
	AgentsForOwner(ctx context.Context, ownerPublicID string, since time.Duration, limit int) ([]QueueAgentRow, error)
	TimelineForOwner(ctx context.Context, ownerPublicID, agentPublicID string, limit int) ([]QueueTimelineRow, error)
}

// QueueFunnel, QueueAgentRow and QueueTimelineRow are the wire shapes for the developer's own
// queue view. Declared here rather than imported from the store for the same reason the admin
// ones are: internal/store already imports this package's siblings, and the dashboard's
// contract should be owned by the API rather than by whatever the repository returns.
type QueueFunnel struct {
	Enqueued         int64 `json:"enqueued"`
	ReadyAsked       int64 `json:"ready_asked"`
	ReadyOK          int64 `json:"ready_ok"`
	Matched          int64 `json:"matched"`
	Dropped          int64 `json:"dropped"`
	Requeued         int64 `json:"requeued"`
	Left             int64 `json:"left"`
	NeverMatched     int64 `json:"never_matched"`
	WaitP50Ms        int64 `json:"wait_p50_ms"`
	WaitP95Ms        int64 `json:"wait_p95_ms"`
	LongestWaitingMs int64 `json:"longest_waiting_ms"`
}

type QueueAgentRow struct {
	AgentPublicID      string `json:"agent_public_id"`
	AgentName          string `json:"agent_name"`
	Game               string `json:"game"`
	Enqueued           int64  `json:"enqueued"`
	Matched            int64  `json:"matched"`
	Dropped            int64  `json:"dropped"`
	Requeued           int64  `json:"requeued"`
	NeverMatched       bool   `json:"never_matched"`
	WorstWaitMs        int64  `json:"worst_wait_ms"`
	LastEventAt        string `json:"last_event_at"`
	CurrentlyWaitingMs int64  `json:"currently_waiting_ms"`
}

type QueueTimelineRow struct {
	Kind          string `json:"kind"`
	Reason        string `json:"reason"`
	MatchPublicID string `json:"match_public_id"`
	WaitedMs      int64  `json:"waited_ms"`
	At            string `json:"at"`
}

// RegisterQueueSelf mounts the developer-facing queue views.
//
// Mounted on the AUTHENTICATED router by the caller. These are not public: the response
// describes one developer's agents, so an unauthenticated request has no correct answer and
// gets a 401 rather than an empty funnel.
func RegisterQueueSelf(r chi.Router, reader QueueSelfReader) {
	h := &queueSelfHandler{reader: reader}

	// USER SCOPE, ENFORCED AT THE ROUTER — not just by reading the principal in the handler.
	//
	// auth.PrincipalFromContext returns a UserPublicID for an AGENT-scope key too, because an
	// agent key resolves to its owner. So without this guard, a key that leaked from a CI
	// runner or a container image could read its owner's entire queue history: every agent
	// name, every wait, every time one went unreachable.
	//
	// That is precisely the shape of the wallet `allocate` bug documented in
	// internal/wallet/handler.go — a handler doing its own ownership reasoning on a router
	// that declared no scope. The lesson recorded there applies unchanged to a READ: an agent
	// key is not the owner.
	//
	// Nothing legitimate loses access. The dashboard sends a user-scope dashboard token; no
	// SDK path reads these routes.
	owner := auth.RequireScope(auth.ScopeUser)
	r.With(owner).Get("/v1/me/queue-health", h.funnel)
	r.With(owner).Get("/v1/me/queue-health/agents", h.agents)
	r.With(owner).Get("/v1/me/queue-health/agents/{agentID}/timeline", h.timeline)
}

// QueueSelfMount returns the registrar as a value main.go can append to its mounts slice
// without importing chi.
//
// Matches the idiom the other handlers use (depositHandler.Register and friends): expose
// something with the func(chi.Router) signature and pass it by value. main.go builds its
// router from a slice of those and names no router package itself.
func QueueSelfMount(reader QueueSelfReader) func(chi.Router) {
	return func(r chi.Router) { RegisterQueueSelf(r, reader) }
}

type queueSelfHandler struct{ reader QueueSelfReader }

// owner resolves the caller, or writes the error and reports false.
//
// A single place, so no handler here can forget it and accidentally pass "" — which the store
// treats as "no rows" rather than "all rows", but relying on that as the only guard would be
// depending on a downstream default for access control.
func (h *queueSelfHandler) owner(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.reader == nil {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "queue_health_unavailable",
			"Queue reporting is not enabled in this deployment."))
		return "", false
	}
	p := auth.PrincipalFromContext(r.Context())
	if p == nil || p.UserPublicID == "" {
		httpx.Error(w, httpx.ErrUnauthorized)
		return "", false
	}
	return p.UserPublicID, true
}

// funnel: how this developer's agents fared — queued, asked, confirmed, matched, dropped,
// and how many never got a game.
func (h *queueSelfHandler) funnel(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	f, err := h.reader.FunnelForOwner(r.Context(), owner, queueWindow(r))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// no-store: a developer checking whether their agent is stuck right now must not be
	// served a funnel from before they restarted it.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"window_seconds": int64(queueWindow(r).Seconds()),
		"funnel":         f,
	})
}

// agents: which of MY agents is stuck, and where.
func (h *queueSelfHandler) agents(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	rows, err := h.reader.AgentsForOwner(r.Context(), owner, queueWindow(r), 100)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if rows == nil {
		rows = []QueueAgentRow{} // an empty list, never null — the UI maps over this
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"agents": rows})
}

// timeline: the readable log for one of MY agents.
//
// The agent id comes from the URL, and the store checks it against the owner IN THE QUERY.
// That join is the guard — without it, passing someone else's agent id would return their
// agent's queue history, which is the classic shape of this bug.
func (h *queueSelfHandler) timeline(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	agentID := chi.URLParam(r, "agentID")
	if agentID == "" {
		httpx.Error(w, httpx.ErrBadRequest)
		return
	}
	rows, err := h.reader.TimelineForOwner(r.Context(), owner, agentID, 200)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// An agent that is not this owner's returns an EMPTY timeline, not a 404. A 404 would
	// confirm the id exists for somebody, and "does this agent id belong to anyone" is not a
	// question an unrelated developer should be able to ask.
	if rows == nil {
		rows = []QueueTimelineRow{}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"agent_public_id": agentID, "events": rows})
}

// queueWindow reads ?window_seconds=, default 24h for a developer view, capped at 7 days.
//
// A longer default than the admin funnel's hour on purpose: a developer looks at this after
// noticing something, often the next morning, and an hour-long window would show them an
// empty page about a problem that happened last night.
func queueWindow(r *http.Request) time.Duration {
	const def = 24 * time.Hour
	const max = 7 * 24 * time.Hour
	raw := r.URL.Query().Get("window_seconds")
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	if d := time.Duration(n) * time.Second; d <= max {
		return d
	}
	return max
}
