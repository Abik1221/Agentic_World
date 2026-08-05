package devtrace

import (
	"net/http"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the developer's own agent traces. Every route here is
// authenticated and user-scoped; there is no public variant, by design.
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(gr chi.Router) {
		gr.Use(h.authn.Middleware)
		// "My agents' activity", and the same scoped to one agent. Both resolve the
		// caller's identity from the token — an agent id in the path is a filter, not
		// an authorisation.
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/traces", h.activity)
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/agents/{agentID}/traces", h.activity)
		// The paginated game history, and one match in full. Split from /traces because
		// they answer different questions: "what has my agent been doing" is a feed,
		// "what happened in this game and what did it cost" is a record.
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/matches", h.matches)
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/matches/{matchID}", h.matchDetail)
		// Cross-match rollup for one agent (or all of the caller's, with no id).
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/telemetry", h.telemetry)
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/agents/{agentID}/telemetry", h.telemetry)
	})
}

// matches serves one page of the caller's game history, newest first.
//
// The response always carries `total`, `limit` and `offset`. That is not decoration: the
// page this replaced fetched a flat 300 events with no total, so a developer with a
// hundred matches saw four of them and had no way to know the rest existed.
func (h *Handler) matches(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())

	mode, ok := ValidMatchMode(r.URL.Query().Get("mode"))
	if !ok {
		// Not silently widened to "all": a typo'd mode quietly returning real-money
		// matches under a sandbox heading is the one failure this filter must not have.
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_request",
			"mode must be one of: sandbox, competitive (or omitted for all)"))
		return
	}
	limit := 20
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= MaxMatchPageSize {
		limit = n
	}
	offset := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && n > 0 {
		offset = n
	}

	list, total, err := h.svc.Matches(r.Context(), p.UserPublicID, r.URL.Query().Get("agent"), mode, limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"matches": list, "total": total, "limit": limit, "offset": offset,
		"mode": string(mode),
	})
}

// matchDetail serves one match: summary, roster, and the full timeline.
func (h *Handler) matchDetail(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	detail, err := h.svc.MatchDetail(r.Context(), p.UserPublicID, chi.URLParam(r, "matchID"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, detail)
}

func (h *Handler) activity(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())

	limit := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	since := time.Now().UTC().AddDate(0, 0, -7)
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}

	entries, err := h.svc.Activity(r.Context(), p.UserPublicID, chi.URLParam(r, "agentID"), since, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Never cached by a shared cache: the response is scoped to one developer.
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

// telemetry serves the cross-match view for one of the caller's agents.
//
// The agent id is a PATH parameter here but is never trusted: the service intersects it
// with the caller's owned set, so an id belonging to somebody else resolves to an empty
// page rather than to their data — and, deliberately, not to a 403 that would confirm the
// id exists.
func (h *Handler) telemetry(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	days := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil {
		days = n
	}
	out, err := h.svc.AgentTelemetry(r.Context(), p.UserPublicID, chi.URLParam(r, "agentID"), days)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Never cached: one developer's own record.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, out)
}
