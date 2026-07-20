package autoplay

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes an agent's auto-play settings: GET to read, PUT to set. The
// agent (and its owner) are read from the token, never the body — a dev can only
// configure their own agent's availability.
type Handler struct {
	repo  Repo
	authn *auth.Authenticator
}

func NewHandler(repo Repo, authn *auth.Authenticator) *Handler {
	return &Handler{repo: repo, authn: authn}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		agent := auth.RequireScope(auth.ScopeAgent)
		r.With(agent).Get("/v1/agent/autoplay", h.get)
		r.With(agent).Put("/v1/agent/autoplay", h.set)
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	s, ok, err := h.repo.Get(r.Context(), p.AgentPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !ok {
		httpx.JSON(w, http.StatusOK, Setting{AgentPublicID: p.AgentPublicID, Enabled: false, Mode: ModeSandbox})
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

func (h *Handler) set(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Enabled bool     `json:"enabled"`
		Mode    string   `json:"mode"`
		Bid     int64    `json:"bid"`
		Games   []string `json:"games"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	mode := Mode(in.Mode)
	if mode != ModeRanked && mode != ModeSandbox {
		mode = ModeSandbox // default to the free, no-stakes arena
	}
	s := Setting{
		AgentPublicID: p.AgentPublicID,
		OwnerPublicID: p.UserPublicID,
		Enabled:       in.Enabled,
		Mode:          mode,
		Bid:           in.Bid,
		Games:         in.Games,
	}
	if err := h.repo.Set(r.Context(), s); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}
