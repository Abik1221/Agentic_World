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
	// The two numbers on every profile, now openable. Public, exactly like the counts they
	// expand — see internal/devprofile/follows.go on why.
	r.Get("/v1/developers/{handle}/followers", h.followersList)
	r.Get("/v1/developers/{handle}/following", h.followingList)
	// Agent followers use a different table (user → agent) but return the same row shape,
	// so one client component renders all three lists.
	r.Get("/v1/agents/{agent_id}/followers", h.agentFollowers)
	r.Get("/v1/developers/{handle}/matches", h.matches)

	// Authenticated developer actions (user scope).
	r.Group(func(gr chi.Router) {
		gr.Use(h.authn.Middleware)
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/me", h.me)
		// Profile completion, derived from the database. See completion.go for why it
		// cannot live in the browser.
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/completion", h.completion)
		gr.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/developer/username", h.setUsername)
		// The developer's public identity. These columns were readable since 0019 and
		// had no writer, so the client kept them in localStorage — one identity in your
		// own browser, an empty one for everybody else.
		gr.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/developer/profile", h.setProfile)
		// A handle derived from who they already are, so the field is never empty and
		// "skipping" still produces a real @handle rather than a database id.
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developer/username-suggestion", h.usernameSuggestion)
		// Read the relationship + counts. Authenticated so `following` is answerable;
		// the counts alone are also on the public profile.
		gr.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/developers/{handle}/follow", h.followState)
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
	// ?self=<developer id> — the visitor saying which row is theirs. Their row comes back
	// as `self` and is left out of `entries` and `count`, so a signed-in developer is not
	// listed among the people they are browsing, and the paging still adds up.
	//
	// This does NOT make the endpoint authenticated, and must not: the id is already
	// public in this very listing, everything returned is public, and the response stays
	// a pure function of the URL — which is what keeps the shared-cache directive below
	// correct (the id is part of the cache key).
	page, err := h.svc.Directory(r.Context(), q.Get("q"), q.Get("sort"), season, limit, offset, q.Get("self"))
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

// completion serves GET /v1/developer/completion — the onboarding checklist, derived
// from account state so it is identical on every device the developer signs in from.
func (h *Handler) completion(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	c, err := h.svc.Completion(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Per-user and changes the moment they upload or connect: never cached.
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, c)
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

// setProfile is a PATCH in POST's clothing: every field is a pointer, and an omitted
// field is left exactly as it was.
//
// This used to take plain strings, where an omitted field decoded as "" and "" meant
// "clear it". That turned a partial write into a destructive one, and the client made
// exactly that write: editing only the display name posted the whole identity, with an
// empty avatar_url whenever the browser had no stored URL to send. So renaming your
// agent on a laptop deleted the photo you had uploaded from a phone. Ownership of "which
// fields am I changing" belongs to the caller; ownership of "an absent field changes
// nothing" belongs here, where no client can get it wrong.
//
// Clearing a field is still possible — send it explicitly as "" — because a developer
// must be able to remove a bio or a photo, not only replace it.
func (h *Handler) setProfile(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var body struct {
		DisplayName *string `json:"display_name"`
		Bio         *string `json:"bio"`
		AvatarURL   *string `json:"avatar_url"`
	}
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		httpx.Error(w, err)
		return
	}
	id, err := h.svc.SetProfile(r.Context(), p.UserPublicID, body.DisplayName, body.Bio, body.AvatarURL)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Echo the STORED identity — read back after the write, not reflected from the
	// request. A client that redisplays this response shows the values that actually
	// persisted, including the ones it did not send.
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, id)
}

// followState serves GET /v1/developers/{handle}/follow — "do I follow them, and how
// many followers do they have".
//
// This route did not exist, and its absence is the whole bug: with no way to READ the
// relationship, the client initialised its button to "Follow" every time, so following
// somebody worked and then appeared to undo itself on the next page load.
func (h *Handler) followState(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	viewer := ""
	if p != nil {
		viewer = p.UserPublicID
	}
	st, err := h.svc.FollowState(r.Context(), viewer, chi.URLParam(r, "handle"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Per-viewer, so never shared-cacheable: `following` differs for every caller and a
	// shared cache would hand one developer another's relationship.
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, st)
}

// followList serves both /followers and /following. The direction is taken from the ROUTE
// rather than a query parameter, so the two lists have their own URLs and can be linked,
// shared and cached separately — a `?dir=` would have made them one page pretending to be
// two.
func (h *Handler) followersList(w http.ResponseWriter, r *http.Request) {
	h.followList(w, r, DirFollowers)
}

func (h *Handler) followingList(w http.ResponseWriter, r *http.Request) {
	h.followList(w, r, DirFollowing)
}

// followList backs both routes. The direction is passed in by the route that matched rather
// than read back off the URL: sniffing `path.Base` would quietly serve the wrong list for a
// trailing slash or an unexpected mount prefix, and these two answer opposite questions.
func (h *Handler) followList(w http.ResponseWriter, r *http.Request, dir FollowDirection) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))

	page, found, err := h.svc.DeveloperFollowList(r.Context(), chi.URLParam(r, "handle"), dir, limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "not_found", "no such developer"))
		return
	}
	// Briefly shared-cacheable: everything here is public and identical for every viewer,
	// unlike followState, which is per-viewer and must never be cached.
	w.Header().Set("Cache-Control", "public, max-age=15")
	httpx.JSON(w, http.StatusOK, page)
}

// agentFollowers serves GET /v1/agents/{id}/followers.
func (h *Handler) agentFollowers(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	page, err := h.svc.AgentFollowerList(r.Context(), chi.URLParam(r, "agent_id"), limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	httpx.JSON(w, http.StatusOK, page)
}

func (h *Handler) follow(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	st, err := h.svc.Follow(r.Context(), p.UserPublicID, chi.URLParam(r, "handle"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// The new count travels with the new state, so the button and the number beside it
	// update from one response and cannot drift apart.
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, st)
}

func (h *Handler) unfollow(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	st, err := h.svc.Unfollow(r.Context(), p.UserPublicID, chi.URLParam(r, "handle"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, st)
}
