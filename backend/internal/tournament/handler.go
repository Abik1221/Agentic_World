package tournament

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes tournament create (admin), free entry (agent), public read, and
// finalize (admin).
type Handler struct {
	svc    *Service
	authn  *auth.Authenticator
	admins map[string]bool
}

func NewHandler(svc *Service, authn *auth.Authenticator, adminUserIDs []string) *Handler {
	admins := make(map[string]bool, len(adminUserIDs))
	for _, id := range adminUserIDs {
		admins[id] = true
	}
	return &Handler{svc: svc, authn: authn, admins: admins}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/tournaments/{id}", h.get) // public
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		// Admin routes also admit the Super Admin Platform token (IsAdmin still gates).
		admin := auth.RequireScopeAny(auth.ScopeUser, auth.ScopePlatform)
		r.With(auth.RequireScope(auth.ScopeAgent)).Post("/v1/tournaments/{id}/enter", h.enter)
		r.With(admin).Post("/v1/tournaments", h.create)
		r.With(admin).Post("/v1/admin/tournaments/{id}/finalize", h.finalize)
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=5")
	httpx.JSON(w, http.StatusOK, t)
}

func (h *Handler) enter(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if err := h.svc.Enter(r.Context(), p.AgentPublicID, chi.URLParam(r, "id")); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"entered": true})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if !h.isAdmin(r) {
		httpx.Error(w, httpx.ErrForbidden)
		return
	}
	var in struct {
		Name      string `json:"name"`
		Sponsor   string `json:"sponsor"`
		PrizePool int64  `json:"prize_pool"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	id, err := h.svc.Create(r.Context(), in.Name, in.Sponsor, in.PrizePool)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"tournament_id": id})
}

func (h *Handler) finalize(w http.ResponseWriter, r *http.Request) {
	if !h.isAdmin(r) {
		httpx.Error(w, httpx.ErrForbidden)
		return
	}
	var in struct {
		Winner string `json:"winner"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.Finalize(r.Context(), chi.URLParam(r, "id"), in.Winner); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"finalized": true})
}

func (h *Handler) isAdmin(r *http.Request) bool {
	return auth.IsAdmin(auth.PrincipalFromContext(r.Context()), h.admins)
}
