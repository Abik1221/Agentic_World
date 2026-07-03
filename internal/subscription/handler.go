package subscription

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes Arena Pass subscription endpoints.
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		user := auth.RequireScope(auth.ScopeUser)
		r.With(user).Get("/v1/subscription", h.status)
		r.With(user).Get("/v1/subscription/plans", h.plans)
		r.With(user).Post("/v1/subscription/checkout", h.checkout)
		r.With(user).Post("/v1/subscription/portal", h.portal)
		r.With(user).Post("/v1/subscription/confirm", h.confirm)
	})
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	st, err := h.svc.Status(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

func (h *Handler) plans(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"plans": h.svc.Plans()})
}

func (h *Handler) checkout(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	url, sid, err := h.svc.Checkout(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"checkout_url": url, "session_id": sid})
}

func (h *Handler) portal(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	url, err := h.svc.Portal(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"portal_url": url})
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		SessionID string `json:"session_id"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.ConfirmDev(r.Context(), p.UserPublicID, in.SessionID); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "active"})
}
