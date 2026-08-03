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
		// The READ. Its absence is why the button could never show "Following".
		r.With(user).Get("/v1/agent/{id}/follow", h.followState)
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

// followState serves GET /v1/agent/{id}/follow.
func (h *Handler) followState(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	viewer := ""
	if p != nil {
		viewer = p.UserPublicID
	}
	st, err := h.svc.FollowState(r.Context(), viewer, chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Per-viewer: `following` differs for every caller, so a shared cache would hand
	// one user another's relationship.
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, st)
}

func (h *Handler) follow(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	st, err := h.svc.Follow(r.Context(), p.UserPublicID, chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, st)
}

func (h *Handler) unfollow(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	st, err := h.svc.Unfollow(r.Context(), p.UserPublicID, chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, st)
}
