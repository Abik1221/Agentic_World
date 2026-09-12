package sandbox

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the agent-facing sandbox API. Play itself reuses the standard
// /v1/match/{id}/state|action|replay|watch endpoints — only starting a practice
// match is new — so a developer's client code is identical to competitive play.
type Handler struct {
	svc    *Service
	authn  *auth.Authenticator
	owners auth.AgentOwner
}

// NewHandler wires the sandbox routes.
func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

// SetPrimaryAgentLookup lets a dashboard JWT sit the agent /v1/me says they own.
func (h *Handler) SetPrimaryAgentLookup(l auth.AgentOwner) { h.owners = l }

// Register mounts the routes (agent scope).
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		sit := auth.RequireScopeAny(auth.ScopeAgent, auth.ScopeUser)
		r.With(sit).Get("/v1/sandbox/opponents", h.opponents)
		r.With(sit).Post("/v1/sandbox/match", h.create)
		r.With(sit).Post("/v1/sandbox/pushplay", h.pushplay)
	})
}

func (h *Handler) opponents(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"opponents": h.svc.Opponents()})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	agentID, err := auth.SittingAgent(r.Context(), p, h.owners)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Difficulty is optional and defaults to medium. Accept it from the query
	// string OR a JSON body, and tolerate no body at all ("just start a match").
	difficulty := r.URL.Query().Get("difficulty")
	if r.ContentLength > 0 {
		var in struct {
			Difficulty string `json:"difficulty"` // easy | medium | hard
		}
		if err := httpx.DecodeJSON(w, r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		if in.Difficulty != "" {
			difficulty = in.Difficulty
		}
	}
	res, err := h.svc.Start(r.Context(), agentID, p.UserPublicID, difficulty)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

// pushplay opens a sandbox match and drives the caller's seat from their hosted
// agent endpoint (manifest push model). Same body as create (optional difficulty);
// the match then plays itself and is watchable at /v1/match/{id}/watch.
func (h *Handler) pushplay(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	agentID, err := auth.SittingAgent(r.Context(), p, h.owners)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	difficulty := r.URL.Query().Get("difficulty")
	if r.ContentLength > 0 {
		var in struct {
			Difficulty string `json:"difficulty"`
		}
		if err := httpx.DecodeJSON(w, r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		if in.Difficulty != "" {
			difficulty = in.Difficulty
		}
	}
	res, err := h.svc.StartPushPlay(r.Context(), agentID, p.UserPublicID, difficulty)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}
