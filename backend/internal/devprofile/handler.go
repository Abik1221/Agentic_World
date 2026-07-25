package devprofile

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the public developer profile surface + the authenticated
// username-claim and follow actions.
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

func (h *Handler) Register(r chi.Router) {
	// Public reputation surface.
	r.Get("/v1/leaderboard/developers", h.leaderboard)
	r.Get("/v1/developers/{handle}", h.profile)
	r.Get("/v1/developers/{handle}/pindex", h.pindex)
	r.Get("/v1/developers/{handle}/matches", h.matches)

	// Authenticated developer actions (user scope).
	r.Group(func(gr chi.Router) {
		gr.Use(h.authn.Middleware)
		gr.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/developer/username", h.setUsername)
		gr.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/developers/{handle}/follow", h.follow)
		gr.With(auth.RequireScope(auth.ScopeUser)).Delete("/v1/developers/{handle}/follow", h.unfollow)
	})
}

func (h *Handler) leaderboard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	season, _ := strconv.Atoi(q.Get("season"))
	offset, _ := strconv.Atoi(q.Get("cursor"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	page, err := h.svc.Leaderboard(r.Context(), q.Get("window"), q.Get("segment"), season, limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	httpx.JSON(w, http.StatusOK, page)
}

func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	p, found, err := h.svc.Profile(r.Context(), chi.URLParam(r, "handle"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "not_found", "no such developer"))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	httpx.JSON(w, http.StatusOK, p)
}

func (h *Handler) pindex(w http.ResponseWriter, r *http.Request) {
	view, found, err := h.svc.PIndex(r.Context(), chi.URLParam(r, "handle"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "not_found", "no such developer"))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	httpx.JSON(w, http.StatusOK, view)
}

func (h *Handler) matches(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	rows, next, found, err := h.svc.Matches(r.Context(), chi.URLParam(r, "handle"), limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "not_found", "no such developer"))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	resp := map[string]any{"matches": rows}
	if next > 0 {
		resp["next_cursor"] = next // pass back as ?cursor= for the next page
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (h *Handler) setUsername(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var body struct {
		Username string `json:"username"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.SetUsername(r.Context(), p.UserPublicID, body.Username); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"username": body.Username})
}

func (h *Handler) follow(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if err := h.svc.Follow(r.Context(), p.UserPublicID, chi.URLParam(r, "handle")); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"following": true})
}

func (h *Handler) unfollow(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if err := h.svc.Unfollow(r.Context(), p.UserPublicID, chi.URLParam(r, "handle")); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"following": false})
}
