package profiles

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the public profile and the agent-scoped stats endpoint.
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

func (h *Handler) Register(r chi.Router) {
	// Param name must match other /v1/agent/{id}/… routes (chi requires a single
	// param name per tree position); the value here is the agent's slug.
	r.Get("/v1/agent/{id}/profile", h.profile) // public, SEO-friendly
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(auth.RequireScope(auth.ScopeAgent)).Get("/v1/agent/stats", h.stats)
	})
}

func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	doc, err := h.svc.Profile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	httpx.JSON(w, http.StatusOK, doc)
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	doc, err := h.svc.AgentStats(r.Context(), p.AgentPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, doc)
}
