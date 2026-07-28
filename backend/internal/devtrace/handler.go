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
	})
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
