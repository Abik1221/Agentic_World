package social

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the follow/unfollow endpoints (user scope).
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
		r.With(user).Post("/v1/agent/{id}/follow", h.follow)
		r.With(user).Delete("/v1/agent/{id}/follow", h.unfollow)
		// Persisted notification feed (match results, deposits, withdrawals).
		r.With(user).Get("/v1/notifications/feed", h.feed)
		r.With(user).Post("/v1/notifications/read", h.markRead)
	})
}

func (h *Handler) feed(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.svc.Notifications(r.Context(), p.UserPublicID, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if items == nil {
		items = []Notification{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"notifications": items})
}

func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	n, err := h.svc.MarkRead(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"marked_read": n})
}

func (h *Handler) follow(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if err := h.svc.Follow(r.Context(), p.UserPublicID, chi.URLParam(r, "id")); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"following": true})
}

func (h *Handler) unfollow(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if err := h.svc.Unfollow(r.Context(), p.UserPublicID, chi.URLParam(r, "id")); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"following": false})
}
