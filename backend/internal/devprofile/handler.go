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
	// Public reputation surface. Note ordering: chi matches static segments before
	// the {handle} wildcard, so /v1/developers/spotlight is not swallowed by it.
	r.Get("/v1/leaderboard/developers", h.leaderboard)
	r.Get("/v1/developers", h.directory)
	r.Get("/v1/developers/spotlight", h.spotlight)
	// Live availability for the sign-up field. Public and unauthenticated on purpose:
	// it is needed BEFORE an account exists, and usernames are already public in the
	// directory, so it discloses nothing that /v1/developers does not.
	r.Get("/v1/developers/username-available", h.usernameAvailable)
	r.Get("/v1/developers/{handle}", h.profile)
	r.Get("/v1/developers/{handle}/pindex", h.pindex)
	r.Get("/v1/developers/{handle}/matches", h.matches)

	// Authenticated developer actions (user scope).
	r.Group(func(gr chi.Router) {
		gr.Use(h.authn.Middleware)
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/me", h.me)
		gr.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/developer/username", h.setUsername)
		// A handle derived from who they already are, so the field is never empty and
		// "skipping" still produces a real @handle rather than a database id.
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/username-suggestion", h.usernameSuggestion)
		gr.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/developers/{handle}/follow", h.follow)
		gr.With(auth.RequireScope(auth.ScopeUser)).Delete("/v1/developers/{handle}/follow", h.unfollow)
	})
}

// usernameAvailable backs the green/red flag under the username input.
func (h *Handler) usernameAvailable(w http.ResponseWriter, r *http.Request) {
	status, err := h.svc.CheckUsername(r.Context(), r.URL.Query().Get("u"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Never cached: a name free a minute ago may not be now, and a stale "available"
	// turns into a failed submit.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, status)
}

// usernameSuggestion proposes a free handle for the signed-in developer.
//
// Derived from their display name, falling back to the email local part — never from
// the public id, since surfacing "usr_01H8XK" as somebody's name is the exact problem
// this exists to prevent.
func (h *Handler) usernameSuggestion(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	id, _, err := h.svc.Me(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Already claimed one → hand it back rather than proposing a second identity.
	if id.Username != "" {
		httpx.JSON(w, http.StatusOK, map[string]any{"username": id.Username, "claimed": true})
		return
	}
	// The auth principal carries no email (it holds ids and scopes only, which is the
	// right shape for a token), so the display name is the input. SuggestUsername
	// falls back to "player<n>" when there is nothing usable — still a real handle.
	suggestion, err := h.svc.SuggestUsername(r.Context(), id.DisplayName, "")
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"username": suggestion, "claimed": false})
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

// directory serves GET /v1/developers?q=&sort=&limit=&cursor= — the searchable public
// developer list. Unlike the leaderboard it includes developers with no P-Index yet.
func (h *Handler) directory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	season, _ := strconv.Atoi(q.Get("season"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("cursor"))
	page, err := h.svc.Directory(r.Context(), q.Get("q"), q.Get("sort"), season, limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	httpx.JSON(w, http.StatusOK, page)
}

// spotlight serves GET /v1/developers/spotlight — the one developer the landing page
// features. 204 when the platform has no public developer at all yet.
func (h *Handler) spotlight(w http.ResponseWriter, r *http.Request) {
	season, _ := strconv.Atoi(r.URL.Query().Get("season"))
	sp, found, err := h.svc.Spotlight(r.Context(), season)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	httpx.JSON(w, http.StatusOK, sp)
}

// me serves GET /v1/developer/me — the caller's own public identity, so the dashboard
// can prefill the handle they already claimed.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	id, found, err := h.svc.Me(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "not_found", "no such developer"))
		return
	}
	httpx.JSON(w, http.StatusOK, id)
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
