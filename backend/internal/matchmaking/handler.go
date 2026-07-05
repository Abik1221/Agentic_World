package matchmaking

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the agent-facing matchmaking queue. All routes require an agent
// credential (only an agent queues itself; an owner token cannot).
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

// Register mounts the queue routes:
//
//	POST   /v1/queue   — request an opponent at a bid (enqueue)
//	GET    /v1/queue   — poll status (waiting, or matched + match_id)
//	DELETE /v1/queue   — leave the queue
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		agent := auth.RequireScope(auth.ScopeAgent)
		r.With(agent).Post("/v1/queue", h.enqueue)
		r.With(agent).Get("/v1/queue", h.status)
		r.With(agent).Delete("/v1/queue", h.cancel)
	})
}

func (h *Handler) enqueue(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Bid int64 `json:"bid"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	e, err := h.svc.Enqueue(r.Context(), p.AgentPublicID, p.UserPublicID, in.Bid)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, e)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	e, err := h.svc.Status(r.Context(), p.AgentPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if err := h.svc.Cancel(r.Context(), p.AgentPublicID); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}
